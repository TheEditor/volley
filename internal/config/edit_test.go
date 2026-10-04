//go:build darwin || linux

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/process"
)

func TestACFG07MutationGuards(t *testing.T) {
	ctx := context.Background()
	o := EditOptions{Resolve: fixtureOptions(t)}
	result, err := PatchJSON(ctx, o, strings.NewReader(`{}`))
	if err != nil || !result.Created || !result.Changed {
		t.Fatalf("Empty creation: %+v %v", result, err)
	}
	b, err := os.ReadFile(result.Path)
	if err != nil || len(b) != 0 {
		t.Fatalf("Empty file: %q %v", b, err)
	}
	original, _ := os.Stat(result.Path)
	result, err = PatchJSON(ctx, o, strings.NewReader(`{}`))
	after, _ := os.Stat(result.Path)
	if err != nil || result.Created || result.Changed || !os.SameFile(original, after) {
		t.Fatalf("Empty no-op: %+v %v", result, err)
	}
	path := result.Path
	writeConfig(t, path, "# exact\r\nmax_rounds=0x8 # keep\r\nplanner='claude'\r\n")
	original, _ = os.Stat(path)
	result, err = Set(ctx, o, "max_rounds", "8")
	after, _ = os.Stat(path)
	if err != nil || result.Changed || !os.SameFile(original, after) {
		t.Fatalf("Same value: %+v %v", result, err)
	}
	result, err = Set(ctx, o, "planner", "codex")
	if err != nil || !result.Changed {
		t.Fatalf("Scalar set: %+v %v", result, err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != "# exact\r\nmax_rounds=0x8 # keep\r\nplanner='codex'\r\n" {
		t.Fatalf("Unrelated bytes: %q", b)
	}
	for _, text := range []string{`{"unknown":1}`, `{"max_rounds":"9"}`, `{"max_rounds":9.5}`, `{"max_rounds":null}`, `{"planner":"claude","planner":"codex"}`, `{} {}`, `{"persistent":true,"backend":"gashki"}`} {
		before, _ := os.ReadFile(path)
		if _, err := PatchJSON(ctx, o, strings.NewReader(text)); err == nil {
			t.Fatalf("Bad patch accepted: %s", text)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(before) {
			t.Fatal("Invalid patch changed file")
		}
	}
	// Compare original hash and inode; retain each losing candidate.
	for _, replace := range []bool{false, true} {
		t.Run("conflict-"+map[bool]string{false: "same-inode", true: "new-inode"}[replace], func(t *testing.T) {
			writeConfig(t, path, "planner='claude'\n")
			o.BeforeCompare = func(path string) error {
				if replace {
					temp := path + ".external"
					if err := os.WriteFile(temp, []byte("planner='claude'\n"), 0600); err != nil {
						return err
					}
					return os.Rename(temp, path)
				}
				return os.WriteFile(path, []byte("planner='claude' # other writer\n"), 0600)
			}
			result, err := Set(ctx, o, "planner", "codex")
			var conflict *EditError
			if !errors.As(err, &conflict) || conflict.Code != "OUTPUT_CONFLICT" || conflict.CandidatePath == "" {
				t.Fatalf("Conflict: %+v %v", result, err)
			}
			candidate, e := os.ReadFile(conflict.CandidatePath)
			if e != nil || string(candidate) != "planner='codex'\n" {
				t.Fatalf("Retained candidate: %q %v", candidate, e)
			}
			after, _ := os.ReadFile(path)
			expected := "planner='claude' # other writer\n"
			if replace {
				expected = "planner='claude'\n"
			}
			if string(after) != expected {
				t.Fatal("Competing writer was overwritten")
			}
		})
	}
	o.BeforeCompare = nil
	link := filepath.Join(o.Resolve.Cwd, "config-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	o.Resolve.NamedFile = link
	if _, err := Set(ctx, o, "planner", "claude"); err == nil {
		t.Fatal("Symlink mutation accepted")
	}
	if _, _, err := Resolve(o.Resolve); err != nil {
		t.Fatalf("Read-only link failed: %v", err)
	}
	t.Log("Empty creation/no-op, typed rejection, CRLF preservation, target-link refusal, and retained concurrent-edit candidates passed")
}
func TestACFG07LockIdentityAndTimeout(t *testing.T) {
	ctx := context.Background()
	o := EditOptions{Resolve: fixtureOptions(t), LockWait: 30 * time.Millisecond}
	path, _, _ := selectFile(o.Resolve)
	writeConfig(t, path, "")
	lock, err := lockConfig(ctx, path+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Set(ctx, o, "planner", "codex"); err == nil {
		t.Fatal("Concurrent lock accepted")
	}
	_ = lock.Close()
	// Replacement of a fixed lock inode is a conflict, not a bypass.
	o.BeforeCompare = func(path string) error { return os.Rename(path+".lock", path+".lock-moved") }
	result, err := Set(ctx, o, "planner", "codex")
	var conflict *EditError
	if !errors.As(err, &conflict) || conflict.Code != "OUTPUT_CONFLICT" || result.CandidatePath == "" {
		t.Fatalf("Changed lock: %+v %v", result, err)
	}
}

type editorSpy struct {
	t      *testing.T
	args   []string
	change string
	target string
}

func (s *editorSpy) Run(_ context.Context, r process.Request) (process.Result, error) {
	s.args = append([]string{r.Path}, r.Args...)
	candidate := r.Args[len(r.Args)-1]
	if err := os.WriteFile(candidate, []byte(s.change), 0600); err != nil {
		s.t.Fatal(err)
	}
	if s.target != "" {
		if err := os.WriteFile(s.target, []byte("planner='codex' # competing edit\n"), 0600); err != nil {
			s.t.Fatal(err)
		}
	}
	return process.Result{Outcome: process.Exited, Exit: 0, Settled: true}, nil
}
func TestACFG08EditorGrammarAndCandidates(t *testing.T) {
	args, err := EditorArgs(`'/owned/editor with spaces' --literal '$(touch marker)' "two words" empty\ value ''`)
	want := []string{"/owned/editor with spaces", "--literal", "$(touch marker)", "two words", "empty value", ""}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("Editor argv: %q %v", args, err)
	}
	for _, text := range []string{"'unclosed", `"unclosed`, "editor trailing\\", "''"} {
		if _, err := EditorArgs(text); err == nil {
			t.Fatalf("Bad editor grammar: %q", text)
		}
	}
	if args, err := EditorArgs(""); err != nil || !reflect.DeepEqual(args, []string{"vi"}) {
		t.Fatalf("Default editor: %q %v", args, err)
	}
	o := EditorOptions{EditOptions: EditOptions{Resolve: fixtureOptions(t)}, Terminal: true, Editor: `'/owned/editor with spaces' '$(touch marker)'`}
	path, _, _ := selectFile(o.Resolve)
	writeConfig(t, path, "planner='claude'\n")
	spy := &editorSpy{t: t, change: "planner='codex'\n"}
	o.Runner = spy
	result, err := Edit(context.Background(), o)
	if err != nil || !result.Changed {
		t.Fatalf("Editor result: %+v %v", result, err)
	}
	if len(spy.args) != 3 || spy.args[0] != "/owned/editor with spaces" || spy.args[1] != "$(touch marker)" {
		t.Fatalf("Editor boundary: %q", spy.args)
	}
	if _, err := os.Stat(filepath.Join(o.Resolve.Cwd, "marker")); !os.IsNotExist(err) {
		t.Fatal("Shell substitution ran")
	}
	for _, variant := range []string{"invalid", "competing"} {
		t.Run(variant, func(t *testing.T) {
			writeConfig(t, path, "planner='claude'\n")
			spy.change = "max_rounds=0\n"
			spy.target = ""
			if variant == "competing" {
				spy.change = "planner='claude' # edited\n"
				spy.target = path
			}
			_, err := Edit(context.Background(), o)
			var retained *EditError
			if !errors.As(err, &retained) || retained.CandidatePath == "" {
				t.Fatalf("Candidate not retained: %v", err)
			}
			if _, err := os.Stat(retained.CandidatePath); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(path)
			expected := "planner='claude'\n"
			if variant == "competing" {
				expected = "planner='codex' # competing edit\n"
			}
			if string(b) != expected {
				t.Fatalf("Original replaced: %q", b)
			}
		})
	}
	o.Terminal = false
	if _, err := Edit(context.Background(), o); err == nil {
		t.Fatal("Nonterminal editor accepted")
	}
	o.Terminal = true
	o.CI = true
	if _, err := Edit(context.Background(), o); err == nil {
		t.Fatal("CI editor accepted")
	}
	t.Log("Editor grammar has no shell expansion; invalid and competing copies retained; terminal and CI guards passed")
}
