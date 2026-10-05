package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/TheEditor/volley/internal/agent"
	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/delivery"
	"github.com/TheEditor/volley/internal/ops"
	"github.com/TheEditor/volley/internal/review"
)

type commandOutput struct {
	Data      any
	Raw       []byte
	Warnings  []contract.Warning
	Protected []string
}

func environment(opts Options) []string {
	if opts.RunOptions != nil {
		return opts.RunOptions.Env
	}
	return os.Environ()
}
func help(r *contract.Registry, command string) map[string]any {
	text := usage()
	if command != "" {
		cmd := r.Commands[command]
		text = "USAGE: volley " + command
		for _, p := range cmd.Positionals {
			if p.Required {
				text += " " + p.Name
			} else {
				text += " [" + p.Name + "]"
			}
		}
		text += " [FLAGS]\n"
		flags := append(append([]contract.Flag{}, r.GlobalFlags...), cmd.Flags...)
		for i, f := range flags {
			if i%3 == 0 {
				text += "\n"
			}
			text += f.Name
			if f.Arity > 0 {
				text += " VALUE"
			}
			text += "  "
		}
		text += "\n\nAutomation: volley capabilities --json; volley robot-docs guide\n"
		if command == "run" || command == "runs resume" {
			text += "--wait-timeout is a per-turn execution budget. Questions have no time limit.\n"
		}
		if command == "runs get" || command == "runs events" {
			text += "--wait-timeout limits inspection. It does not stop the owner.\n"
		}
	}
	return map[string]any{"usage": text, "capabilities_command": "volley capabilities --json", "default_action": nil}
}
func version(r *contract.Registry) map[string]any {
	build := map[string]any{"commit": "", "date": "", "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "modified": nil}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				build["commit"] = s.Value
			case "vcs.time":
				build["date"] = s.Value
			case "vcs.modified":
				build["modified"] = s.Value == "true"
			}
		}
	}
	return map[string]any{"contract_version": r.ContractVersion, "build": build}
}
func handlerKind(command string) string {
	switch command {
	case "run", "runs resume":
		return "engine"
	case "plan":
		return "plan"
	case "human answer", "human steer", "human skip":
		return "human"
	case "workspace legacy-report", "runs resolve":
		return "migration"
	case "config show", "config get", "config validate", "config schema", "config set", "config patch", "config edit":
		return "config"
	case "status", "doctor", "runs list", "runs get", "runs stop", "runs events", "runs prune", "human questions":
		return "ops"
	case "capabilities", "schema", "robot-docs guide", "feedback", "conformance":
		return "meta"
	}
	return ""
}
func metadata(ctx context.Context, x Invocation, r *contract.Registry, opts Options) (any, error) {
	switch x.Command {
	case "capabilities":
		v, err := contract.RawRegistry()
		if err != nil {
			return nil, err
		}
		commands := v["commands"].(map[string]any)
		for name := range commands {
			if handlerKind(name) == "" {
				delete(commands, name)
			}
		}
		if reads := testEnvironmentReads(); len(reads) > 0 {
			items := v["environment_reads"].([]any)
			for _, read := range reads {
				items = append(items, read)
			}
			v["environment_reads"] = items
		}
		return v, nil
	case "schema":
		name := "envelope"
		if len(x.Positionals) > 0 {
			name = x.Positionals[0]
		}
		if strings.ContainsAny(name, "/\\") {
			return nil, r.Error("INVALID_INPUT", "Schema name must not contain a path")
		}
		b, err := contract.Schema(name)
		if err != nil {
			return nil, r.Error("NOT_FOUND", "Unknown schema")
		}
		return decodeObject(b)
	case "robot-docs guide":
		return map[string]any{"guide": guide(), "capabilities_command": "volley capabilities --json"}, nil
	case "feedback":
		key, _ := x.Values["--idempotency-key"].(string)
		data, e := delivery.Feedback(ctx, ops.StateDir(environment(opts)), x.Positionals[0], key)
		return data, translateError(r, e)
	case "conformance":
		if conformanceDefect(environment(opts)) {
			c := r.Commands["run"]
			c.ParserPath = ""
			r.Commands["run"] = c
		}
		return generatedConformance(ctx, r)
	}
	return nil, r.Error("UNKNOWN_COMMAND", "Handler is not available")
}
func handleConfig(ctx context.Context, x Invocation, r *contract.Registry, opts Options) (any, error) {
	env := environment(opts)
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	named, _ := x.Values["--config"].(string)
	o := config.ResolveOptions{Cwd: cwd, Home: envValue(env, "HOME"), XDGRoot: envValue(env, "XDG_CONFIG_HOME"), NamedFile: named, Flags: x.Settings, Mutating: x.Command == "config set" || x.Command == "config patch" || x.Command == "config edit"}
	if x.Command == "config validate" && len(x.Positionals) > 0 {
		o.NamedFile = x.Positionals[0]
	}
	request := config.CommandRequest{Command: x.Command, Options: o, Stdin: opts.Input}
	if request.Stdin == nil {
		request.Stdin = os.Stdin
	}
	if len(x.Positionals) > 0 && x.Command != "config validate" {
		request.Key = x.Positionals[0]
	}
	if len(x.Positionals) > 1 {
		request.Value = x.Positionals[1]
	}
	if x.Command == "config edit" {
		terminal := false
		var tty *os.File
		if f, ok := request.Stdin.(*os.File); ok {
			tty = f
			terminal = isTerminal(f)
		}
		if opts.Terminal != nil {
			terminal = *opts.Terminal
		}
		editor := envValue(env, "VISUAL")
		if editor == "" {
			editor = envValue(env, "EDITOR")
		}
		request.Editor = config.EditorOptions{Terminal: terminal, CI: envValue(env, "CI") != "", Editor: editor, Env: env, UserTTY: tty, Stdout: io.Discard, Stderr: io.Discard}
		if opts.RunOptions != nil {
			request.Editor.Runner = opts.RunOptions.Runner
		}
	}
	result, err := config.Handle(ctx, request)
	if err != nil {
		var edit *config.EditError
		var invalid *config.Invalid
		var read *config.ReadFailure
		code := "INVALID_CONFIG"
		if errors.As(err, &read) {
			code = "CONFIG_READ_FAILED"
		} else if errors.As(err, &edit) {
			code = edit.Code
		} else if x.Command == "config get" || x.Command == "config set" || x.Command == "config patch" {
			code = "INVALID_INPUT"
		}
		e := r.Error(code, err.Error())
		if errors.As(err, &invalid) {
			e.Path = &invalid.File
		}
		return nil, e
	}
	raw := []byte(nil)
	if x.Values["--toml"] == true {
		raw = result.TOML
	}
	selected := o.NamedFile
	if selected == "" {
		base := o.XDGRoot
		if base == "" {
			base = filepath.Join(o.Home, ".config")
		}
		selected = filepath.Join(base, "volley/config.toml")
	}
	if !filepath.IsAbs(selected) {
		selected = filepath.Join(cwd, selected)
	}
	return commandOutput{Data: result.Data, Raw: raw, Warnings: result.Warnings, Protected: []string{selected}}, nil
}
func plan(ctx context.Context, x Invocation, r *contract.Registry, opts Options) (any, error) {
	env := environment(opts)
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	workspace, err := filepath.Abs(x.Positionals[0])
	if err != nil {
		return nil, err
	}
	named, _ := x.Values["--config"].(string)
	resolved, warnings, err := config.Resolve(config.ResolveOptions{Cwd: cwd, Home: envValue(env, "HOME"), XDGRoot: envValue(env, "XDG_CONFIG_HOME"), NamedFile: named, Workspace: workspace, Flags: x.Settings})
	if err != nil {
		return nil, configFailure(r, err)
	}
	settings := resolved.Settings
	dependencies := []any{}
	arguments := []any{}
	messages := []string{"Preview starts no process and creates no run. Prompt text, exact session identity, inherited permissions and executable bindings are checked at the turn boundary."}
	critic := "codex"
	if settings.Planner == "codex" {
		critic = "claude"
	}
	for _, entry := range []struct{ provider, role string }{{settings.Planner, "planner"}, {critic, "critic"}} {
		exe := settings.ClaudeBin
		if entry.provider == "codex" {
			exe = settings.CodexBin
		}
		dependencies = append(dependencies, map[string]any{"path": exe, "sha256": "", "version": "not_checked"})
		displayExe := exe
		if !filepath.IsAbs(displayExe) {
			displayExe = filepath.Join(workspace, "<resolve-"+entry.provider+"-from-PATH>")
		}
		q := review.TurnRequest{Provider: entry.provider, Role: entry.role, Purpose: "draft", Workspace: workspace, Prompt: []byte("<rendered at the turn boundary>")}
		if entry.role == "critic" {
			q.Purpose = "critique"
		}
		options := agent.ArgumentOptions{Request: q, Settings: settings, Executable: displayExe, ReplyTemp: filepath.Join(workspace, "state/turns/<turn-id>/final.txt"), InheritedChecked: true}
		var argv []string
		if settings.Backend == "cli" {
			argv, _, err = agent.BuildDirectArguments(options)
		} else {
			argv, _, err = agent.BuildGashkiArguments(options)
		}
		if err != nil {
			return nil, r.Error("INVALID_INPUT", err.Error())
		}
		arguments = append(arguments, map[string]any{"provider": entry.provider, "role": entry.role, "argv": argv, "cwd": workspace, "stdin": "closed"})
	}
	for _, w := range warnings {
		messages = append(messages, w.Message)
	}
	command := []string{"volley", "run", workspace}
	if named != "" {
		command = append(command, "--config", named)
	}
	for _, setting := range r.Settings {
		if override, ok := x.Settings[setting.Key]; ok {
			value := fmt.Sprint(override.Value)
			if setting.Type == "array" {
				b, e := contract.Canonical(override.Value)
				if e != nil {
					return nil, e
				}
				value = string(b)
			}
			command = append(command, setting.Flag, value)
		}
	}
	for _, flag := range []string{"--brief", "--seed", "--idempotency-key"} {
		if value, ok := x.Values[flag].(string); ok {
			command = append(command, flag, value)
		}
	}
	action := map[string]any{"command": ops.Command(command...), "rationale": "Start a new review after checking the saved inputs and explicit settings.", "is_destructive": false, "alternatives": []any{}}
	return commandOutput{Data: map[string]any{"workspace": workspace, "settings": settings, "sources": resolved.Sources, "transitions": []string{"prepare", "draft if no seed spec", "critique", "revise when required", "commit exact final approval or retain handover"}, "paths": map[string]any{"brief": filepath.Join(workspace, "BRIEF.md"), "spec": filepath.Join(workspace, "SPEC.md"), "state": filepath.Join(workspace, "state"), "volley_config": filepath.Join(workspace, "volley.config.toml"), "gashki_config": filepath.Join(workspace, "gashki.config.toml")}, "dependencies": dependencies, "arguments": arguments, "warnings": messages, "recommended_action": action}, Warnings: warnings, Protected: []string{resolved.SelectedFile, filepath.Join(workspace, "state/manifest.json"), filepath.Join(workspace, "volley.config.toml"), filepath.Join(workspace, "gashki.config.toml")}}, nil
}
