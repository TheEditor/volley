package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/TheEditor/volley/internal/conformance"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/engine"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ParserManifest is the surface actually consumed by the generic parser. A
// declaration without a supported parser node or typed handler is rejected.
func ParserManifest(r *contract.Registry) (map[string]any, error) {
	commands := map[string]contract.Command{}
	check := func(f contract.Flag) error {
		if f.ParserPath == "" || f.Arity < 0 || f.Arity > 1 {
			return fmt.Errorf("Missing flag parser: %s", f.Name)
		}
		switch f.Type {
		case "string", "path", "executable", "duration", "integer", "boolean", "array":
			return nil
		}
		return fmt.Errorf("Unsupported flag type: %s", f.Type)
	}
	for _, f := range r.GlobalFlags {
		if err := check(f); err != nil {
			return nil, err
		}
	}
	for name, c := range r.Commands {
		if c.ParserPath == "" || c.HandlerPath == "" || handlerKind(name) == "" {
			return nil, fmt.Errorf("Missing command parser or handler: %s", name)
		}
		for _, f := range c.Flags {
			if err := check(f); err != nil {
				return nil, err
			}
		}
		for _, constraint := range c.Constraints {
			switch constraint["kind"] {
			case "required", "requires", "excludes", "excludes-stdout", "requires-one", "exactly-one":
			default:
				return nil, fmt.Errorf("Unsupported constraint on %s", name)
			}
		}
		commands[name] = c
	}
	return map[string]any{"commands": commands, "global_flags": r.GlobalFlags}, nil
}
func generatedConformance(ctx context.Context, r *contract.Registry) (any, error) {
	probe := func(c conformance.Case, id string) (conformance.Observation, error) {
		var out, stderr bytes.Buffer
		args := append([]string{}, c.Args...)
		if !c.Human && !MachineMode(args) {
			args = append([]string{"--json"}, args...)
		}
		env := []string{"TERM=dumb", "NO_COLOR=1"}
		if c.Target.Shape == "raw" || c.Target.Verb == "feedback" {
			root, err := os.MkdirTemp("", "volley-conformance-")
			if err != nil {
				return conformance.Observation{}, err
			}
			defer os.RemoveAll(root)
			path := filepath.Join(root, "settings.toml")
			if err = os.WriteFile(path, []byte("# Owned raw-mode probe\n"), 0600); err != nil {
				return conformance.Observation{}, err
			}
			if c.Target.Shape == "raw" {
				args = append(args, "--config", path)
			}
			env = append(env, "HOME="+root, "XDG_CONFIG_HOME="+root, "XDG_STATE_HOME="+root, "PATH="+root)
		}
		if c.Fault != "" {
			env = append(env, "VOLLEY_TEST_FAULT="+c.Fault)
		}
		if c.ID == "X-03" {
			args = []string{"--json", "--help"}
			env = append(env, "VOLLEY_TEST_FAULT=entry")
		}
		opts := Options{Now: func() time.Time { return time.Unix(100, 0) }, RequestID: func() (string, error) { return id, nil }, Input: strings.NewReader(""), RunOptions: &engine.Options{Env: env}}
		exit := Execute(ctx, args, &out, &stderr, opts)
		observed := conformance.Observation{Exit: exit, OK: exit == 0}
		if c.Human {
			if out.Len() == 0 || strings.Contains(out.String(), "\"request_id\"") {
				return observed, fmt.Errorf("Human response is absent or an envelope")
			}
			if exit != 0 {
				observed.Code = c.Want.Code
				if !strings.Contains(stderr.String(), c.Want.Code) {
					return observed, fmt.Errorf("Human error absent from stderr")
				}
			}
			return observed, nil
		}
		var result contract.Result
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			return observed, fmt.Errorf("Machine response cannot be decoded: %v", err)
		}
		observed.OK = result.OK
		if len(result.Errors) > 0 {
			observed.Code = result.Errors[0].Code
			if !strings.Contains(stderr.String(), result.Errors[0].Message) {
				return observed, fmt.Errorf("Error is absent from stderr")
			}
		}
		if c.Hint && (len(result.Errors) == 0 || result.Errors[0].DidYouMean == nil) {
			return observed, fmt.Errorf("Near-match hint absent")
		}
		if err := contract.Validate("envelope.json", result); err != nil {
			return observed, err
		}
		if result.Meta.RequestID != id && c.Fault != "entry" {
			return observed, fmt.Errorf("Probe request ID differs")
		}
		if strings.ContainsAny(out.String(), "\x1b\a") {
			return observed, fmt.Errorf("Response contains terminal controls")
		}
		return observed, nil
	}
	parity := func() error {
		manifest, err := ParserManifest(r)
		if err != nil {
			return err
		}
		raw, err := contract.RawRegistry()
		if err != nil {
			return err
		}
		declared := map[string]any{"commands": raw["commands"], "global_flags": raw["global_flags"]}
		a, err := contract.Canonical(manifest)
		if err != nil {
			return err
		}
		b, err := contract.Canonical(declared)
		if err != nil {
			return err
		}
		if !bytes.Equal(a, b) {
			return fmt.Errorf("Parser manifest differs from capabilities")
		}
		return nil
	}
	return conformance.Run(r, fullCI(), probe, parity)
}
