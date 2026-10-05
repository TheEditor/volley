package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/TheEditor/volley/internal/contract"
)

func TestAPACK02LegacyExitMapping(t *testing.T) {
	r, err := contract.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"volley.sh", "cc-volley", "codex-volley"} {
		for _, native := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 130, 143} {
			var out, stderr bytes.Buffer
			opts := fixed()
			opts.Entrypoint = entry
			opts.Command = func(context.Context, []string, *contract.Registry) (any, error) {
				if native == 0 {
					return map[string]any{"checked": true}, nil
				}
				for _, declared := range r.Codes {
					if declared.Exit == native {
						return nil, r.Error(declared.Code, "Owned error boundary")
					}
				}
				t.Fatalf("No declared exit %d", native)
				return nil, nil
			}
			got := Execute(context.Background(), []string{"--json"}, &out, &stderr, opts)
			want := native
			if native == 7 {
				want = 2
			} else if native != 0 && native != 130 && native != 143 {
				want = 1
			}
			var v contract.Result
			if err := json.Unmarshal(out.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			if got != want || v.Meta.Entrypoint != entry || v.Meta.ExitSemantics != "legacy" {
				t.Fatal(entry, native, got, out.String())
			}
			if native != 0 && (len(v.Errors) != 1 || v.Errors[0].Exit != native) {
				t.Fatal("Native evidence lost", out.String())
			}
			t.Logf("A-PACK-02 entry=%s native=%d legacy=%d response=%s", entry, native, got, out.String())
		}
	}
}

func TestAPACK01NativeWrapperGrammar(t *testing.T) {
	for _, entry := range []string{"volley.sh", "cc-volley", "codex-volley"} {
		for _, args := range [][]string{{"--help", "--json"}, {"status", "missing", "--help", "--json"}, {"runs", "get", "missing", "--help", "--json"}, {"plan", "missing", "--help", "--json"}, {"run", "missing", "--help", "--json"}, {"--", "missing"}} {
			r, _ := contract.Load()
			normalized, err := launcherArgs(args, r, Options{Entrypoint: entry})
			if err != nil {
				t.Fatal(entry, args, err)
			}
			x, err := Parse(normalized, r)
			if err != nil {
				t.Fatal(entry, normalized, err)
			}
			if args[0] == "--" && x.Values["--wait"] != true {
				t.Fatal("Legacy shorthand did not wait")
			}
			if args[0] != "--" {
				var out, stderr bytes.Buffer
				if code := Execute(context.Background(), args, &out, &stderr, Options{Entrypoint: entry}); code != 0 {
					t.Fatal(entry, args, code, out.String())
				}
			}
		}
	}
	for _, args := range [][]string{{"--planner", "claude", "plan", "ws", "--planner=codex"}, {"--planner", "claude", "plan", "ws", "--planner"}, {"--planner", "claude", "plan", "ws", "--planner="}} {
		var out, stderr bytes.Buffer
		if code := Execute(context.Background(), append(args, "--json"), &out, &stderr, Options{Entrypoint: "cc-volley"}); code != 1 {
			t.Fatal(args, code, out.String())
		}
	}
}
