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
	"strconv"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/engine"
)

type Options struct {
	Entrypoint string
	Now        func() time.Time
	RequestID  func() (string, error)
	// Hooks are explicit unit seams. No release environment variable enables them.
	Entry      func()
	Stage      func(string)
	Command    func(context.Context, []string, *contract.Registry) (any, error)
	Input      io.Reader
	Terminal   *bool
	RunOptions *engine.Options
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
	wrapper := legacyEntrypoint(opts.Entrypoint)
	var raw []byte
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
		if wrapper {
			result.Meta.Entrypoint = opts.Entrypoint
			result.Meta.ExitSemantics = "legacy"
		}
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
		} else if result.OK && raw != nil {
			if _, err := out.Write(raw); err != nil {
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
					if text == "" {
						text = finalMessage(m)
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
		if wrapper && exit != 0 && exit != 130 && exit != 143 {
			if exit == 7 {
				exit = 2
			} else {
				exit = 1
			}
		}
	}()
	injectFault("entry", environment(opts))
	if opts.Entry != nil {
		opts.Entry()
	}
	ctx, stopSignals := controllerContext(ctx)
	defer stopSignals()
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
		command := opts.Command
		if command == nil {
			command = func(ctx context.Context, args []string, r *contract.Registry) (any, error) {
				return dispatch(ctx, args, r, opts)
			}
		}
		result.Data, err = command(ctx, args, reg)
		if output, ok := result.Data.(rawOutput); ok {
			result.Data = output.Data
			raw = output.Bytes
			result.Warnings = append(result.Warnings, output.Warnings...)
		}
		if data, ok := result.Data.(map[string]any); ok {
			if records, ok := data["warning_records"]; ok {
				b, e := json.Marshal(records)
				if e == nil {
					var warnings []contract.Warning
					if json.Unmarshal(b, &warnings) == nil {
						result.Warnings = append(result.Warnings, warnings...)
					}
				}
			}

			if warnings, ok := data["warnings"].([]string); ok {
				for _, code := range warnings {
					message := ""
					switch code {
					case "INDEX_UNAVAILABLE":
						message = "Run index registration failed; workspace reads remain available"
					case "ENV_SETTING_IGNORED":
						message = "Retired setting variable is ignored"
					}
					if message != "" {
						exists := false
						for _, w := range result.Warnings {
							if w.Code == code {
								exists = true
								break
							}
						}
						if exists {
							continue
						}
						result.Warnings = append(result.Warnings, contract.Warning{Code: code, Message: message, Evidence: map[string]any{"reason": "Command observation"}})
					}
				}
			}
		}
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
	return "volley — specification review\n\nUSAGE: volley [GLOBAL_FLAGS] COMMAND\n       volley WORKSPACE [RUN_FLAGS]\n\nplan WORKSPACE: preview; run WORKSPACE: execute; status WORKSPACE: inspect\nruns list/get/resume/stop/resolve/events/prune\nhuman questions/answer/steer/skip\nworkspace legacy-report WORKSPACE\nconfig show/get/validate/schema/set/patch/edit\ndoctor --workspace WORKSPACE; feedback TEXT\ncapabilities; schema [NAME]; robot-docs guide; conformance\n--help; --version\n\nAutomation: volley capabilities --json; volley schema; volley robot-docs guide\nBoolean settings take true or false. Selectors take no value.\nRun/resume --wait-timeout limits each turn. Get/events use a read-wait budget.\n"
}
