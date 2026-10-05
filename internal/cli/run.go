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
	"strings"
	"time"

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
	Near        string
	Positionals []string
	Values      map[string]any
	Settings    map[string]config.Override
}

func handleInvocation(ctx context.Context, x Invocation, r *contract.Registry, opts Options) (any, error) {
	var err error
	if x.Values["--version"] == true {
		return version(r), nil
	}
	if x.Command == "" || x.Values["--help"] == true {
		return help(r, x.Command), nil
	}
	if x.Near != "" {
		if _, e := os.Stat(x.Positionals[0]); os.IsNotExist(e) {
			e := r.Error("UNKNOWN_COMMAND", "No workspace exists for the near command spelling")
			e.DidYouMean = &x.Near
			command := ops.Command("volley", x.Near)
			e.Remediation = &command
			return nil, e
		} else if e != nil {
			return nil, r.Error("INVALID_INPUT", e.Error())
		}
	}
	switch handlerKind(x.Command) {
	case "meta":
		return metadata(ctx, x, r, opts)
	case "plan":
		return plan(ctx, x, r, opts)
	case "config":
		return handleConfig(ctx, x, r, opts)
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
	if (x.Command == "run" || x.Command == "runs resume") && unsupportedProcessEnvironment(env) {
		return nil, r.Error("UNSUPPORTED_PLATFORM", "Unix process control is unavailable")
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
			return nil, configFailure(r, err)
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
					return nil, configFailure(r, err)
				}
				explicit[key] = resolved
			}
		}
		if configPath != "" {
			resolved, _, err := config.Resolve(config.ResolveOptions{Cwd: cwd, Home: envValue(options.Env, "HOME"), NamedFile: configPath, Workspace: workspace, Mutating: true, Flags: x.Settings})
			if err != nil {
				return nil, configFailure(r, err)
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

func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), terminalRequest())
	return err == nil
}

func configFailure(r *contract.Registry, err error) error {
	var read *config.ReadFailure
	if errors.As(err, &read) {
		return r.Error("CONFIG_READ_FAILED", err.Error())
	}
	return r.Error("INVALID_CONFIG", err.Error())
}
