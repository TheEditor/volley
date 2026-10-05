//go:build darwin || linux

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
)

func TestALOOP07SharedGuard(t *testing.T) {
	for _, planner := range []string{"claude", "codex"} {
		for _, purpose := range []string{"draft", "revision", "directive", "critique", "reminder"} {
			for _, failed := range []bool{false, true} {
				for _, path := range []string{"BRIEF.md", "volley.config.toml", "rounds/r99.critique.md", "state/control/foreign.json", "@stdout"} {
					t.Run(fmt.Sprintf("%s/%s/failure-%v/%s", planner, purpose, failed, path), func(t *testing.T) {
						role := "planner"
						if purpose == "critique" || purpose == "reminder" {
							role = "critic"
						}
						plan := AgentPlan{MutationPath: path, MutationRole: role, MutationPurpose: purpose, Fail: failed}
						if purpose == "revision" {
							plan.Critiques = []string{"Review\nVERDICT: REVISE\n"}
						}
						if purpose == "reminder" {
							plan.Critiques = []string{"No final verdict\n"}
						}
						f := newFixture(t, planner, false, purpose != "draft", plan)
						if purpose == "directive" {
							if err := os.WriteFile(filepath.Join(f.Workspace, "HUMAN.md"), []byte("Apply this directive.\n"), 0600); err != nil {
								t.Fatal(err)
							}
						}
						_, err := f.run(t)
						code := "PLANNER_MUTATION"
						if role == "critic" {
							code = "CRITIC_MUTATION"
						}
						requireCode(t, err, code)
						var typed *contract.Error
						if errors.As(err, &typed) && typed.Exit != 8 {
							t.Fatal(typed)
						}
						calls := f.calls()
						if len(calls) == 0 || !strings.Contains(calls[len(calls)-1], role+" "+purpose) {
							t.Fatal(calls)
						}
					})
				}
			}
		}
	}
}

func TestALOOP07PrecallConfig(t *testing.T) {
	for _, planner := range []string{"claude", "codex"} {
		t.Run(planner, func(t *testing.T) {
			f := newFixture(t, planner, false, false, AgentPlan{})
			f.Options.Hook = func(name string, s *store.Store) error {
				if name == "engine.intent.committed" {
					return os.WriteFile(filepath.Join(s.Path, "volley.config.toml"), []byte("changed config\n"), 0600)
				}
				return nil
			}
			_, err := f.run(t)
			requireCode(t, err, "ARTIFACT_CHANGED")
			if len(f.calls()) != 0 {
				t.Fatal("mismatched config launched agent")
			}
		})
	}
}

func TestALOOP07CriticalProtectedFiles(t *testing.T) {
	for _, planner := range []string{"claude", "codex"} {
		for _, role := range []string{"planner", "critic"} {
			paths := []string{"state/manifest.json", "CONSTRAINTS.md", "HUMAN.md"}
			if role == "critic" {
				paths = append(paths, "SPEC.md", "QUESTIONS.md")
			}
			for _, path := range paths {
				t.Run(planner+"/"+role+"/"+path, func(t *testing.T) {
					purpose := "draft"
					if role == "critic" {
						purpose = "critique"
					}
					f := newFixture(t, planner, false, role == "critic", AgentPlan{MutationPath: path, MutationRole: role, MutationPurpose: purpose})
					_, err := f.run(t)
					code := "PLANNER_MUTATION"
					if role == "critic" {
						code = "CRITIC_MUTATION"
					}
					requireCode(t, err, code)
					if len(f.calls()) != 1 {
						t.Fatal("mutation allowed another turn", f.calls())
					}
				})
			}
		}
	}
}

func TestAHUMAN05WaitingOwnsNoExtraTurn(t *testing.T) {
	f := newFixture(t, "codex", false, false, AgentPlan{QuestionPurpose: "draft"})
	_, err := f.run(t)
	requireCode(t, err, "ANSWER_REQUIRED")
	f.Request.Resolved = nil
	f.Request.Resume = true
	f.Request.Wait = true
	ctx, cancel := context.WithCancel(context.Background())
	f.Options.Hook = func(name string, s *store.Store) error {
		if name == "engine.answer.wait" {
			cancel()
		}
		return nil
	}
	_, err = Run(ctx, f.Request, f.Options)
	requireCode(t, err, "CONTROLLER_INTERRUPTED")
	if len(f.calls()) != 1 {
		t.Fatal("wait launched another turn")
	}
}
