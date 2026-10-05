//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/engine"
	"github.com/TheEditor/volley/internal/process"
)

type noAgent struct{ calls int }

func (r *noAgent) Run(ctx context.Context, q process.Request) (process.Result, error) {
	r.calls++
	return process.Result{}, nil
}
func TestAMIG01LegacyEntrypoints(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "state"), 0700)
	os.Mkdir(filepath.Join(root, "rounds"), 0700)
	os.WriteFile(filepath.Join(root, "state/run"), []byte("uncertain old run"), 0600)
	os.WriteFile(filepath.Join(root, "rounds/r01.critique.md"), []byte("APPROVE\n"), 0600)
	runner := &noAgent{}
	for _, entry := range []string{"volley", "cc-volley", "codex-volley"} {
		t.Run(entry, func(t *testing.T) {
			opts := Options{Entrypoint: entry, RunOptions: &engine.Options{Env: []string{"HOME=" + root, "PATH=" + root}, Runner: runner}}
			args := []string{root, "--json"}
			expected := 1
			if entry == "volley" {
				args = append([]string{"run"}, args...)
				expected = 8
			}
			var out, stderr bytes.Buffer
			exit := Execute(context.Background(), args, &out, &stderr, opts)
			var result contract.Result
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if exit != expected || len(result.Errors) != 1 || result.Errors[0].Code != "LEGACY_BOUNDARY_UNVERIFIED" || result.Errors[0].Exit != 8 || result.Errors[0].Remediation == nil {
				t.Fatal(exit, out.String())
			}
			if entry != "volley" && result.Meta.Entrypoint != entry {
				t.Fatal(result.Meta)
			}
			if runner.calls != 0 {
				t.Fatal("Legacy run called a dependency")
			}
			if _, err := os.Stat(filepath.Join(root, "state/manifest.json")); !os.IsNotExist(err) {
				t.Fatal("Legacy run wrote native state")
			}
			b, _ := os.ReadFile(filepath.Join(root, "state/run"))
			if string(b) != "uncertain old run" {
				t.Fatal("Old marker changed")
			}
		})
	}
}
func TestAMIG02MigrationGrammar(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"workspace", "legacy-report", "absent", "--yes"}, "UNKNOWN_FLAG"},
		{[]string{"workspace", "migrate", "absent"}, "UNKNOWN_COMMAND"},
		{[]string{"workspace", "legacy-report", "absent", "--dry-run"}, "UNKNOWN_FLAG"},
		{[]string{"runs", "resolve", "absent", "--turn=t", "--from-stdin"}, "ACK_REQUIRED"},
		{[]string{"runs", "resolve", "absent", "--yes"}, "MISSING_REQUIRED"},
		{[]string{"runs", "resolve", "absent", "--yes=true"}, "INVALID_INPUT"},
		{[]string{"runs", "resolve", "absent", "--turn="}, "INVALID_INPUT"},
		{[]string{"runs", "resolve", "absent", "--turn=t", "--turn=u", "--yes"}, "INVALID_INPUT"},
		{[]string{"runs", "resolve", "absent", "--turn"}, "INVALID_INPUT"},
	} {
		var out, stderr bytes.Buffer
		Execute(context.Background(), append(tc.args, "--json"), &out, &stderr, fixed())
		var v contract.Result
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if len(v.Errors) != 1 || v.Errors[0].Code != tc.code {
			t.Fatal(tc.args, out.String())
		}
	}
}

func TestAMIG03ResolutionObjectGrammar(t *testing.T) {
	valid := `{"resolution":"abandon","artifact_paths":[],"evidence_paths":[],"note":"keep evidence"}`
	inputs := []string{`{}`, `null`, `[]`, `{"resolution":"abandon","artifact_paths":[],"evidence_paths":[]}`, `{"resolution":"retry","artifact_paths":[],"evidence_paths":[],"note":"x"}`, `{"resolution":"abandon","artifact_paths":null,"evidence_paths":[],"note":"x"}`, `{"resolution":"abandon","artifact_paths":[1],"evidence_paths":[],"note":"x"}`, `{"resolution":"abandon","artifact_paths":["a","a"],"evidence_paths":[],"note":"x"}`, `{"resolution":"abandon","artifact_paths":[],"evidence_paths":[],"note":"x","extra":1}`, `{"resolution":"abandon","resolution":"abandon","artifact_paths":[],"evidence_paths":[],"note":"x"}`, valid + ` {}`, `{"resolution":"abandon","artifact_paths":[],"evidence_paths":[],"note":"\u0000"}`}
	for _, input := range inputs {
		opts := fixed()
		opts.Input = bytes.NewBufferString(input)
		var out, stderr bytes.Buffer
		Execute(context.Background(), []string{"runs", "resolve", "absent", "--turn=t", "--from-stdin", "--yes", "--json"}, &out, &stderr, opts)
		var result contract.Result
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Errors) != 1 || result.Errors[0].Code != "INVALID_INPUT" {
			t.Fatal(input, out.String())
		}
	}
}

func TestAMIG02WrapperHelp(t *testing.T) {
	for _, entry := range []string{"cc-volley", "codex-volley"} {
		for _, args := range [][]string{{"--json"}, {"--help", "--json"}} {
			var out, errout bytes.Buffer
			exit := Execute(context.Background(), args, &out, &errout, Options{Entrypoint: entry})
			if exit != 0 {
				t.Fatal(entry, exit, out.String())
			}
		}
	}
}
