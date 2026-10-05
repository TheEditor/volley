//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/engine"
	"github.com/TheEditor/volley/internal/human"
	"github.com/TheEditor/volley/internal/migration"
	"github.com/TheEditor/volley/internal/ops"
	"github.com/TheEditor/volley/internal/store"
	"golang.org/x/sys/unix"
)

type Invocation struct {
	Command     string
	Positionals []string
	Values      map[string]any
	Settings    map[string]config.Override
}

// ParseRun uses the declared grammar for this runnable slice. The remaining
// command tree and output delivery are completed in T20.
func ParseRun(args []string, r *contract.Registry) (Invocation, error) {
	x := Invocation{Values: make(map[string]any), Settings: make(map[string]config.Override)}
	known := make(map[string]contract.Flag)
	for _, f := range r.GlobalFlags {
		if f.Name == "--json" || f.Name == "--config" || f.Name == "--help" {
			known[f.Name] = f
		}
	}
	seen := make(map[string]bool)
	literal := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !utf8.ValidString(arg) {
			return x, r.Error("INVALID_INPUT", "Argument must be valid UTF-8")
		}
		if !literal && arg == "--" {
			literal = true
			continue
		}
		if !literal && strings.HasPrefix(arg, "-") {
			name, raw, attached := strings.Cut(arg, "=")
			if name == "-h" {
				name = "--help"
			}
			f, ok := known[name]
			if !ok {
				return x, r.Error("UNKNOWN_FLAG", "Unknown flag: "+name)
			}
			if seen[name] && !f.Repeatable {
				return x, r.Error("INVALID_INPUT", "Repeated flag: "+name)
			}
			seen[name] = true
			var value any = true
			if f.Arity == 0 {
				if attached {
					return x, r.Error("INVALID_INPUT", name+" takes no value")
				}
			} else {
				if !attached {
					if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
						return x, r.Error("INVALID_INPUT", "Missing value for "+name)
					}
					i++
					raw = args[i]
				}
				if raw == "" && !f.AllowEmpty {
					return x, r.Error("INVALID_INPUT", "Empty value for "+name)
				}
				if !utf8.ValidString(raw) {
					return x, r.Error("INVALID_INPUT", "Flag value must be valid UTF-8")
				}
				value = raw
				switch f.Type {
				case "integer":
					n, err := strconv.Atoi(raw)
					if err != nil {
						return x, r.Error("INVALID_INPUT", "Invalid integer for "+name)
					}
					value = n
				case "boolean":
					if raw != "true" && raw != "false" {
						return x, r.Error("INVALID_INPUT", "Boolean setting requires true or false")
					}
					value = raw == "true"
				case "array":
					var values []any
					d := json.NewDecoder(strings.NewReader(raw))
					if err := d.Decode(&values); err != nil {
						return x, r.Error("INVALID_INPUT", "Array flag requires a JSON array")
					}
					if values == nil {
						return x, r.Error("INVALID_INPUT", "Array flag requires a JSON array, not null")
					}
					var extra any
					if d.Decode(&extra) != io.EOF {
						return x, r.Error("INVALID_INPUT", "Array flag has trailing data")
					}
					value = values
				}
				if len(f.Enum) > 0 {
					valid := false
					for _, v := range f.Enum {
						if v == raw {
							valid = true
						}
					}
					if !valid {
						return x, r.Error("INVALID_INPUT", "Invalid value for "+name+"; allowed: "+strings.Join(f.Enum, ", "))
					}
				}
			}
			if strings.ContainsRune(raw, 0) {
				return x, r.Error("INVALID_INPUT", "Flag value contains NUL")
			}
			x.Values[name] = value
			if f.Setting != "" {
				for _, s := range r.Settings {
					if s.Key == f.Setting {
						v, err := config.Validate(s, value)
						if err != nil {
							return x, r.Error("INVALID_INPUT", err.Error())
						}
						value = v
					}
				}
				x.Settings[f.Setting] = config.Override{Value: value, Flag: name, Position: i + 1}
			}
			continue
		}
		if x.Command == "" {
			x.Command = arg
			if arg == "runs" || arg == "human" || arg == "workspace" {
				if i+1 >= len(args) {
					return x, r.Error("UNKNOWN_COMMAND", "Subcommand is required")
				}
				i++
				x.Command += " " + args[i]
			}
			cmd, ok := r.Commands[x.Command]
			if !ok {
				return x, r.Error("UNKNOWN_COMMAND", "Unknown command: "+x.Command)
			}
			for _, f := range cmd.Flags {
				known[f.Name] = f
			}
		} else {
			if arg == "" {
				return x, r.Error("INVALID_INPUT", "Empty workspace")
			}
			x.Positionals = append(x.Positionals, arg)
		}
	}
	if x.Values["--help"] == true {
		return x, nil
	}
	cmd := r.Commands[x.Command]
	minimum := 0
	for _, p := range cmd.Positionals {
		if p.Required {
			minimum++
		}
	}
	if len(x.Positionals) < minimum || len(x.Positionals) > len(cmd.Positionals) {
		return x, r.Error("INVALID_INPUT", "Invalid number of positional arguments")
	}
	if x.Values["--interactive"] == true && (x.Values["--wait"] != true || x.Values["--json"] == true) {
		return x, r.Error("INVALID_INPUT", "Interactive requires --wait and human rendering")
	}
	if x.Values["--brief"] != nil && x.Values["--seed"] != nil {
		return x, r.Error("INVALID_INPUT", "Brief and seed are mutually exclusive")
	}
	if x.Command == "human answer" && ((x.Values["--from-stdin"] == true) == (x.Values["--file"] != nil)) {
		return x, r.Error("INVALID_INPUT", "Answer requires exactly one of --from-stdin or --file")
	}
	if (x.Command == "human answer" || x.Command == "human skip") && x.Values["--question-id"] == nil {
		return x, r.Error("INVALID_INPUT", "Question ID is required")
	}
	if x.Command == "human steer" && x.Values["--from-stdin"] != true {
		return x, r.Error("INVALID_INPUT", "Steering requires --from-stdin")
	}
	if x.Command == "runs resolve" {
		if x.Values["--yes"] != true {
			return x, r.Error("ACK_REQUIRED", "Resolution requires --yes")
		}
		if x.Values["--turn"] == nil || x.Values["--from-stdin"] != true {
			return x, r.Error("MISSING_REQUIRED", "Resolution requires --turn and --from-stdin")
		}
	}
	if x.Command == "human skip" && x.Values["--yes"] != true {
		return x, r.Error("ACK_REQUIRED", "Question withdrawal requires --yes")
	}
	return x, nil
}

func dispatch(ctx context.Context, args []string, r *contract.Registry, opts Options) (any, error) {
	isRun := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			if arg == "--config" {
				i++
			}
			continue
		}
		isRun = arg == "run" || arg == "runs" || arg == "human" || arg == "status" || arg == "doctor" || arg == "workspace"
		break
	}
	if !isRun {
		return initialCommand(ctx, args, r)
	}
	x, err := ParseRun(args, r)
	if err != nil {
		return nil, err
	}
	if x.Values["--help"] == true {
		text := "volley " + x.Command
		for _, p := range r.Commands[x.Command].Positionals {
			text += " " + p.Name
		}
		text += " [FLAGS]\n"
		if x.Command == "run" || x.Command == "runs resume" {
			text += "--wait-timeout is a per-turn execution budget. Unanswered questions have no time limit.\n"
		}
		if x.Command == "runs get" || x.Command == "runs events" {
			text += "--wait-timeout limits inspection. It does not stop the owner.\n"
		}
		return map[string]any{"usage": text, "capabilities_command": "volley capabilities --json", "default_action": nil}, nil
	}
	if x.Command == "workspace legacy-report" {
		d, e := migration.Report(x.Positionals[0])
		return d, translateError(r, e)
	}
	var resolution migration.Resolution
	if x.Command == "runs resolve" {
		in := opts.Input
		if in == nil {
			in = os.Stdin
		}
		resolution, err = migration.ParseResolution(in)
		if err != nil {
			return nil, translateError(r, err)
		}
	}
	if x.Command != "runs resolve" && x.Command != "run" && x.Command != "runs resume" && x.Command != "human answer" && x.Command != "human steer" && x.Command != "human skip" {
		return handleOps(ctx, x, r, opts)
	}
	env := os.Environ()
	if opts.RunOptions != nil {
		env = opts.RunOptions.Env
	}
	operator := ops.Options{IndexDir: ops.StateDir(env)}
	selected := x.Positionals[0]
	if x.Command != "run" {
		selected, err = operator.Resolve(selected)
	}
	if err != nil {
		return nil, translateError(r, err)
	}
	workspace, err := filepath.Abs(selected)
	if err != nil {
		return nil, r.Error("INVALID_INPUT", err.Error())
	}
	if err = migration.CheckStart(workspace); err != nil {
		return nil, translateError(r, err)
	}
	if x.Command != "runs resolve" && x.Command != "run" && x.Command != "runs resume" && !strings.HasPrefix(x.Command, "human ") {
		return nil, r.Error("UNKNOWN_COMMAND", "Handler is not available")
	}
	input := opts.Input
	if input == nil {
		input = os.Stdin
	}
	if strings.HasPrefix(x.Command, "human ") {
		return handleHuman(ctx, x, workspace, input, r)
	}
	options := engine.Options{Env: os.Environ()}
	if opts.RunOptions != nil {
		options = *opts.RunOptions
	}
	if x.Command == "runs resolve" {
		turn, _ := x.Values["--turn"].(string)
		data, e := engine.Resolve(ctx, workspace, turn, resolution, options)
		return data, translateError(r, e)
	}
	if options.Register == nil {
		options.Register = operator.Register
	}
	terminal := false
	if f, ok := input.(*os.File); ok {
		_, err := unix.IoctlGetTermios(int(f.Fd()), terminalRequest())
		terminal = err == nil
	}
	if opts.Terminal != nil {
		terminal = *opts.Terminal
	}
	explicit := make(map[string]any)
	for key, v := range x.Settings {
		explicit[key] = v.Value
	}
	request := engine.Request{Workspace: workspace, Explicit: explicit, Resume: x.Command == "runs resume", Wait: x.Values["--wait"] == true, Interactive: x.Values["--interactive"] == true, Terminal: terminal}
	if request.Interactive {
		request.Input = input
	}
	request.Brief, _ = x.Values["--brief"].(string)
	request.Seed, _ = x.Values["--seed"].(string)
	request.Key, _ = x.Values["--idempotency-key"].(string)
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	configPath, _ := x.Values["--config"].(string)
	_, manifestErr := os.Stat(filepath.Join(workspace, "state/manifest.json"))
	if os.IsNotExist(manifestErr) {
		resolved, _, err := config.Resolve(config.ResolveOptions{Cwd: cwd, Home: envValue(options.Env, "HOME"), XDGRoot: envValue(options.Env, "XDG_CONFIG_HOME"), NamedFile: configPath, Workspace: workspace, Mutating: true, Flags: x.Settings})
		if err != nil {
			return nil, r.Error("INVALID_CONFIG", err.Error())
		}
		request.Resolved = &resolved
	} else {
		for key, value := range explicit {
			if key == "context_dir" || key == "gashki_config" {
				if path, ok := value.(string); ok && path != "" {
					if !filepath.IsAbs(path) {
						path = filepath.Join(cwd, path)
					}
					resolved, err := config.Physical(path)
					if err != nil {
						return nil, err
					}
					explicit[key] = resolved
				}
			}
			if key == "claude_bin" || key == "codex_bin" || key == "gashki_bin" {
				resolved, err := config.LookupExecutable(fmt.Sprint(value))
				if err != nil {
					return nil, r.Error("INVALID_CONFIG", err.Error())
				}
				explicit[key] = resolved
			}
		}
		if configPath != "" {
			resolved, _, err := config.Resolve(config.ResolveOptions{Cwd: cwd, Home: envValue(options.Env, "HOME"), NamedFile: configPath, Workspace: workspace, Mutating: true, Flags: x.Settings})
			if err != nil {
				return nil, r.Error("INVALID_CONFIG", err.Error())
			}
			b, _ := contract.Canonical(resolved.Settings)
			values, _ := decodeObject(b)
			for key, value := range values {
				if resolved.Sources[key].Source == "default" {
					continue
				}
				if _, ok := explicit[key]; !ok {
					explicit[key] = value
				}
			}
		}
	}
	data, err := engine.Run(ctx, request, options)
	return data, translateError(r, err)
}

func handleOps(ctx context.Context, x Invocation, r *contract.Registry, opts Options) (any, error) {
	env := os.Environ()
	if opts.RunOptions != nil {
		env = opts.RunOptions.Env
	}
	named, _ := x.Values["--config"].(string)
	o := ops.Options{IndexDir: ops.StateDir(env), Now: opts.Now, Env: env, ConfigFile: named}
	request := ops.Request{Command: x.Command, Wait: x.Values["--wait"] == true, Probe: x.Values["--probe"] == true, DryRun: x.Values["--dry-run"] == true, Yes: x.Values["--yes"] == true, Limit: 25}
	if len(x.Positionals) > 0 {
		request.Selector = x.Positionals[0]
	}
	if v, ok := x.Values["--workspace"].(string); ok {
		request.Selector = v
	}
	for flag, target := range map[string]*string{"--since-cursor": &request.Cursor, "--cursor": &request.Cursor, "--fields": &request.Fields} {
		if v, ok := x.Values[flag].(string); ok {
			*target = v
		}
	}
	if v, ok := x.Values["--limit"].(int); ok {
		request.Limit = v
		if v < 1 || v > 100 {
			return nil, r.Error("INVALID_INPUT", "Limit must be 1 through 100")
		}
	}
	for flag, target := range map[string]*time.Duration{"--wait-timeout": &request.Timeout, "--older-than": &request.OlderThan} {
		if v, ok := x.Values[flag].(string); ok {
			duration, e := time.ParseDuration(v)
			if e != nil || duration < 0 {
				return nil, r.Error("INVALID_INPUT", "Invalid duration for "+flag)
			}
			*target = duration
		}
	}
	value, err := o.Handle(ctx, request)
	if ctx.Err() != nil {
		code := "CONTROLLER_INTERRUPTED"
		if fmt.Sprint(context.Cause(ctx)) == "SIGTERM" {
			code = "CONTROLLER_STOPPED"
		}
		return value, r.Error(code, "Inspection was interrupted; the owner was not stopped")
	}
	return value, translateError(r, err)
}

func envValue(env []string, key string) string {
	for _, value := range env {
		if strings.HasPrefix(value, key+"=") {
			return strings.TrimPrefix(value, key+"=")
		}
	}
	return ""
}
func decodeObject(b []byte) (map[string]any, error) {
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	err := d.Decode(&m)
	return m, err
}
func translateError(r *contract.Registry, err error) error {
	if err == nil {
		return nil
	}
	var typed *contract.Error
	if errors.As(err, &typed) {
		return err
	}
	var stored *store.Error
	if errors.As(err, &stored) {
		e := r.Error(stored.Code, stored.Message)
		e.Path = &stored.Path
		return e
	}
	return r.Error("INTERNAL", err.Error())
}

func handleHuman(ctx context.Context, x Invocation, workspace string, input io.Reader, r *contract.Registry) (any, error) {
	s, err := store.Open(workspace)
	if err != nil {
		return nil, r.Error("NOT_FOUND", "Workspace is absent")
	}
	defer s.Close()
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return nil, translateError(r, err)
	}
	question, _ := x.Values["--question-id"].(string)
	var sub human.Submission
	if x.Command == "human skip" {
		g, err := human.LoadGeneration(s, question)
		if err != nil {
			return nil, translateError(r, err)
		}
		sub, err = human.Withdraw(ctx, s, *g, true)
		if err != nil {
			return nil, translateError(r, err)
		}
	} else {
		var in human.Input
		if path, ok := x.Values["--file"].(string); ok {
			in, err = human.FileInput(path)
		} else {
			in, err = human.ParseInput(input)
		}
		if err != nil {
			return nil, translateError(r, err)
		}
		kind := "answer"
		if x.Command == "human steer" {
			kind = "steer"
		}
		sub, err = human.Submit(ctx, s, m.String("run_id"), question, kind, "command", in, nil, false, "")
		if err != nil {
			return nil, translateError(r, err)
		}
	}
	data := map[string]any{"run_id": m["run_id"], "workspace": s.Path, "receipt_id": sub.Entry.ReceiptHash, "receipt_path": sub.Entry.ReceiptPath, "applied": false, "duplicate": sub.Duplicate}
	if x.Command != "human steer" {
		data["question_id"] = question
	}
	return data, nil
}
