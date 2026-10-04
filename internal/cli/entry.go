// Package cli owns parsing, handlers and output at the application boundary.
package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/contract"
)

type Options struct {
	Now       func() time.Time
	RequestID func() (string, error)
	// Hooks are explicit unit seams. No release environment variable enables them.
	Entry   func()
	Stage   func(string)
	Command func(context.Context, []string, *contract.Registry) (any, error)
}

// MachineMode is the complete infallible bootstrap: lexical mode selection,
// with no command/config/filesystem work and no validation.
func MachineMode(args []string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--json" || strings.HasPrefix(a, "--json=") {
			return true
		}
	}
	return false
}

// Execute installs its recovery handler before any fallible tool-owned work.
// All public output is written here. Deep packages return values and errors.
func Execute(ctx context.Context, args []string, out, stderr io.Writer, opts Options) (exit int) {
	machine := MachineMode(args)
	var reg *contract.Registry
	start := time.Now()
	result := contract.Result{Warnings: []contract.Warning{}, Commands: []string{}, Errors: []*contract.Error{}}
	defer func() {
		if p := recover(); p != nil {
			if reg == nil {
				reg, _ = contract.Load()
			}
			// The embedded registry is checked by the build. Do not expose a panic
			// value that could contain an input or a credential.
			result.OK = false
			result.Data = nil
			result.Errors = []*contract.Error{reg.Error("INTERNAL", "Unexpected internal failure")}
			exit = result.Errors[0].Exit
		}
		if reg == nil {
			reg, _ = contract.Load()
		}
		result.ToolVersion = reg.ToolVersion
		result.Meta.ContractVersion = reg.ContractVersion
		result.Meta.SchemaVersion = "1"
		result.Meta.ElapsedMS = time.Since(start).Milliseconds()
		if result.Meta.Time == "" {
			result.Meta.Time = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if result.Meta.RequestID == "" {
			result.Meta.RequestID = "unavailable"
		}
		canonical, err := contract.Canonical(result.Data)
		if err != nil {
			result.OK = false
			result.Data = nil
			result.Errors = []*contract.Error{reg.Error("INTERNAL", "Cannot encode response")}
			exit = result.Errors[0].Exit
			canonical = []byte("null")
		}
		result.Meta.DataHash = "sha256:" + contract.HashBytes(canonical)
		if len(result.Errors) > 0 {
			fmt.Fprintln(stderr, result.Errors[0].Error())
		}
		if machine {
			enc := json.NewEncoder(out)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(result); err != nil {
				fmt.Fprintln(stderr, "Cannot write response")
				exit = reg.Error("INTERNAL", "Cannot write response").Exit
			}
		} else {
			var text string
			if result.OK {
				if m, ok := result.Data.(map[string]any); ok {
					if usage, ok := m["usage"].(string); ok {
						text = usage
					}
				}
				if text == "" {
					b, err := json.MarshalIndent(result.Data, "", "  ")
					if err == nil {
						text = string(b)
					}
				}
			} else if len(result.Errors) > 0 {
				text = result.Errors[0].Error()
			}
			if _, err := fmt.Fprintln(out, text); err != nil {
				fmt.Fprintln(stderr, "Cannot write response")
				exit = reg.Error("INTERNAL", "Cannot write response").Exit
			}
		}
	}()
	if opts.Entry != nil {
		opts.Entry()
	}
	var err error
	reg, err = contract.Load()
	if err != nil {
		panic(err)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	stamp := now().UTC()
	if value, exists := os.LookupEnv("SOURCE_DATE_EPOCH"); exists {
		n, e := strconv.ParseInt(value, 10, 64)
		if e != nil || n < 0 {
			err = reg.Error("INVALID_INPUT", "Invalid SOURCE_DATE_EPOCH")
		} else {
			stamp = time.Unix(n, 0).UTC()
		}
	}
	result.Meta.Time = stamp.Format(time.RFC3339Nano)
	id := opts.RequestID
	if id == nil {
		id = func() (string, error) { var b [16]byte; _, e := rand.Read(b[:]); return hex.EncodeToString(b[:]), e }
	}
	requestID, errID := id()
	result.Meta.RequestID = requestID
	if errID != nil && err == nil {
		err = reg.Error("INTERNAL", "Cannot allocate request identity")
	}
	if err == nil {
		for _, stage := range reg.DiagnosisOrder {
			if opts.Stage != nil {
				opts.Stage(stage)
			}
		}
		command := opts.Command
		if command == nil {
			command = initialCommand
		}
		result.Data, err = command(ctx, args, reg)
	}
	if err != nil {
		var declared *contract.Error
		if !errors.As(err, &declared) {
			declared = reg.Error("INTERNAL", "Unexpected internal failure")
		}
		result.OK = false
		result.Errors = []*contract.Error{declared}
		exit = declared.Exit
	} else {
		result.OK = true
	}
	return
}

func usage() string {
	return "volley — specification review\n\nUSAGE: volley [GLOBAL_FLAGS] COMMAND\n\nThe Go implementation is under construction.\nAvailable: capabilities, schema, --help, --version.\nAutomation: volley capabilities --json\n"
}

// Initial handlers expose only the working declaration/asset boundary.
// Final command parsing and execution are supplied by later task handlers.
func initialCommand(_ context.Context, args []string, r *contract.Registry) (any, error) {
	filtered := []string{}
	for _, a := range args {
		if a == "--json" {
			continue
		}
		if strings.HasPrefix(a, "--json=") {
			return nil, r.Error("INVALID_INPUT", "--json takes no value")
		}
		filtered = append(filtered, a)
	}
	if len(filtered) == 0 || filtered[0] == "--help" || filtered[0] == "-h" {
		return map[string]any{"usage": usage(), "capabilities_command": "volley capabilities --json", "default_action": nil}, nil
	}
	if filtered[0] == "--version" {
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
		return map[string]any{"contract_version": r.ContractVersion, "build": build}, nil
	}
	switch filtered[0] {
	case "capabilities":
		if len(filtered) != 1 {
			return nil, r.Error("UNKNOWN_FLAG", "Unsupported capabilities argument")
		}
		v, err := contract.RawRegistry()
		if err != nil {
			return nil, err
		}
		commands := v["commands"].(map[string]any)
		for name := range commands {
			if name != "capabilities" && name != "schema" {
				delete(commands, name)
			}
		}
		return v, nil
	case "schema":
		name := "envelope"
		if len(filtered) > 1 {
			name = filtered[1]
		}
		if len(filtered) > 2 {
			return nil, r.Error("INVALID_INPUT", "Too many schema arguments")
		}
		if strings.ContainsAny(name, "/\\") {
			return nil, r.Error("INVALID_INPUT", "Schema name must not contain a path")
		}
		b, err := contract.Schema(name)
		if err != nil {
			return nil, r.Error("NOT_FOUND", "Unknown schema")
		}
		var v any
		d := json.NewDecoder(strings.NewReader(string(b)))
		d.UseNumber()
		err = d.Decode(&v)
		return v, err
	}
	return nil, r.Error("UNKNOWN_COMMAND", "This command has no Go handler yet")
}
