//go:build darwin || linux

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheEditor/volley/internal/human"
	"github.com/TheEditor/volley/internal/store"
)

func submit(t *testing.T, f *fixture, kind, text string) string {
	t.Helper()
	s, err := store.Open(f.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, _, err := s.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	question := ""
	if kind == "answer" {
		question = fmt.Sprint(m.Object("question")["id"])
	}
	sub, err := human.Submit(context.Background(), s, m.String("run_id"), question, kind, "command", human.Input{Text: text, IdempotencyKey: kind + "-exact"}, nil, false, "")
	if err != nil {
		t.Fatal(err)
	}
	return sub.Entry.ReceiptHash
}

func TestALOOP02Reminder(t *testing.T) {
	for _, planner := range []string{"claude", "codex"} {
		for _, variant := range []string{"embedded", "quoted", "fenced", "lower", "comment", "crlf", "reminder-fails"} {
			t.Run(planner+"/"+variant, func(t *testing.T) {
				first := map[string]string{"embedded": "VERDICT: APPROVE\nMore text\n", "quoted": "> VERDICT: APPROVE\n", "fenced": "```\nVERDICT: APPROVE\n```\n", "lower": "VERDICT: approve\n", "comment": "VERDICT: APPROVE # comment\n", "crlf": "Review\r\nVERDICT: APPROVE\r\n\r\n", "reminder-fails": "First actual review without verdict\n"}[variant]
				plan := AgentPlan{Critiques: []string{first, "Reminder\nVERDICT: APPROVE\n"}}
				if variant == "reminder-fails" {
					plan.Critiques = []string{first, "Second actual review without verdict\n", "Last review\nVERDICT: APPROVE\n"}
				}
				f := newFixture(t, planner, false, true, plan)
				data, err := f.run(t)
				if err != nil {
					t.Fatal(err)
				}
				if data["status"] != "approved" {
					t.Fatal(data)
				}
				calls := f.calls()
				want := 2
				if variant == "crlf" {
					want = 1
				}
				if variant == "reminder-fails" {
					want = 4
				}
				if len(calls) != want {
					t.Fatal(calls)
				}
				if variant != "crlf" {
					b, e := os.ReadFile(filepath.Join(f.Workspace, "rounds/r01.critique.md"))
					if e != nil || string(b) != first {
						t.Fatal("first reply lost", e)
					}
					if _, e = os.Stat(filepath.Join(f.Workspace, "rounds/r01.critique-retry.md")); e != nil {
						t.Fatal(e)
					}
				}
				if variant == "reminder-fails" {
					b, e := os.ReadFile(filepath.Join(f.Workspace, "rounds/r01.verdict-missing.md"))
					if e != nil || !strings.Contains(string(b), "MISSING") {
						t.Fatal("missing diagnostic", e)
					}
					found := false
					entries, _ := os.ReadDir(f.Capture)
					for _, entry := range entries {
						if filepath.Ext(entry.Name()) != ".json" {
							continue
						}
						b, _ := os.ReadFile(filepath.Join(f.Capture, entry.Name()))
						var c map[string]any
						_ = json.Unmarshal(b, &c)
						turn := c["turn"].(map[string]any)
						if turn["purpose"] == "revision" {
							found = strings.Contains(c["prompt"].(string), "MISSING verdict diagnostic")
						}
					}
					if !found {
						t.Fatal("planner missed the diagnostic")
					}
				}
			})
		}
	}
}

func TestALOOP03DirectiveBeforeApproval(t *testing.T) {
	for _, planner := range []string{"claude", "codex"} {
		t.Run(planner, func(t *testing.T) {
			f := newFixture(t, planner, true, true, AgentPlan{})
			injected := false
			f.Options.Hook = func(name string, s *store.Store) error {
				if name == "engine.final.before" && !injected {
					injected = true
					m, _, err := s.LoadSnapshot()
					if err != nil {
						return err
					}
					_, err = human.Submit(context.Background(), s, m.String("run_id"), "", "steer", "command", human.Input{Text: "  Preserve exact\r\nwords.\n", IdempotencyKey: "before-final"}, nil, false, "")
					return err
				}
				return nil
			}
			data, err := f.run(t)
			if err != nil {
				t.Fatal(err)
			}
			if data["status"] != "approved" || number(data["round"]) != 2 {
				t.Fatal(data)
			}
			calls := f.calls()
			if len(calls) != 3 || !strings.Contains(calls[1], "planner directive") {
				t.Fatal(calls)
			}
			m := f.manifest(t)
			entries := m["applications"].([]any)
			if len(entries) != 1 {
				t.Fatal(entries)
			}
			a := entries[0].(map[string]any)
			if a["planner_delivered"] != true || a["critic_delivered"] != true {
				t.Fatal(a)
			}
			captures, _ := os.ReadDir(f.Capture)
			delivered := 0
			for _, entry := range captures {
				if filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				b, _ := os.ReadFile(filepath.Join(f.Capture, entry.Name()))
				var c map[string]any
				_ = json.Unmarshal(b, &c)
				if strings.Contains(c["prompt"].(string), "  Preserve exact\r\nwords.\n") {
					delivered++
				}
			}
			if delivered != 2 {
				t.Fatalf("exact text deliveries=%d", delivered)
			}
		})
	}
}

func TestALOOP04Cap(t *testing.T) {
	for _, question := range []bool{false, true} {
		t.Run(fmt.Sprint(question), func(t *testing.T) {
			plan := AgentPlan{Critiques: []string{"Review\nVERDICT: REVISE\n", "Review\nVERDICT: APPROVE\n"}}
			if question {
				plan.QuestionPurpose = "revision"
			}
			f := newFixture(t, "claude", false, true, plan)
			f.Request.Resolved.Settings.MaxRounds = 1
			data, err := f.run(t)
			requireCode(t, err, "REVIEW_IMPASSE")
			if data["status"] != "impasse" || number(data["round"]) != 2 || len(f.calls()) != 2 {
				t.Fatal(data, f.calls())
			}
			if _, err = os.Stat(filepath.Join(f.Workspace, "rounds/r01.spec.md")); err != nil {
				t.Fatal(err)
			}
			f.Request.Resolved = nil
			f.Request.Resume = true
			f.Request.Explicit = map[string]any{"max_rounds": 0}
			_, err = f.run(t)
			requireCode(t, err, "CONFIG_CONFLICT")
			f.Request.Explicit = map[string]any{"max_rounds": 2, "claude_model": "different"}
			_, err = f.run(t)
			requireCode(t, err, "CONFIG_CONFLICT")
			f.Request.Explicit = map[string]any{"max_rounds": 2}
			data, err = f.run(t)
			if question {
				requireCode(t, err, "ANSWER_REQUIRED")
				if len(f.calls()) != 2 {
					t.Fatal("impossible extra turn")
				}
				submit(t, f, "answer", "Use A exactly.\n")
				f.setPlan(t, AgentPlan{Critiques: plan.Critiques})
				data, err = f.run(t)
			}
			if err != nil || data["status"] != "approved" {
				t.Fatal(data, err)
			}
			m := f.manifest(t)
			if len(m["amendments"].([]any)) != 1 {
				t.Fatal("cap amendment missing")
			}
			s, err := store.Open(f.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			state, err := LoadState(s, m)
			if err != nil {
				t.Fatal(err)
			}
			if state.Settings.MaxRounds != 1 {
				t.Fatal("original cap changed")
			}
		})
	}
}

func TestAHUMAN05Answers(t *testing.T) {
	for _, planner := range []string{"claude", "codex"} {
		for _, channel := range []string{"command", "file", "withdraw-delete", "withdraw-empty", "terminal", "terminal-eof", "steering-only"} {
			t.Run(planner+"/"+channel, func(t *testing.T) {
				f := newFixture(t, planner, false, false, AgentPlan{QuestionPurpose: "draft"})
				data, err := f.run(t)
				requireCode(t, err, "ANSWER_REQUIRED")
				if data["status"] != "awaiting_answer" || len(f.calls()) != 1 {
					t.Fatal(data, f.calls())
				}
				question := f.manifest(t).Object("question")["id"]
				f.Request.Resolved = nil
				f.Request.Resume = true
				answer := "  exact answer\r\nnext line\n"
				switch channel {
				case "command":
					submit(t, f, "answer", answer)
				case "file":
					if err = os.WriteFile(filepath.Join(f.Workspace, "HUMAN.md"), []byte(answer), 0600); err != nil {
						t.Fatal(err)
					}
				case "withdraw-delete":
					_ = os.Remove(filepath.Join(f.Workspace, "QUESTIONS.md"))
				case "withdraw-empty":
					_ = os.WriteFile(filepath.Join(f.Workspace, "QUESTIONS.md"), []byte(" \n\t"), 0600)
				case "terminal", "terminal-eof":
					f.Request.Wait = true
					f.Request.Interactive = true
					f.Request.Terminal = true
					text := answer + "\n"
					if channel == "terminal-eof" {
						text = answer
					}
					f.Request.Input = strings.NewReader(text)
				case "steering-only":
					submit(t, f, "steer", "A directive does not answer the question.\n")
				}
				data, err = f.run(t)
				if channel == "terminal-eof" || channel == "steering-only" {
					requireCode(t, err, "ANSWER_REQUIRED")
					if len(f.calls()) != 1 || data["question_id"] != question {
						t.Fatal("gate released", data, f.calls())
					}
					return
				}
				if err != nil || data["status"] != "approved" {
					t.Fatal(data, err)
				}
				calls := f.calls()
				if len(calls) != 3 || !strings.Contains(calls[1], "directive") {
					t.Fatal(calls)
				}
				m := f.manifest(t)
				entries := m["applications"].([]any)
				if len(entries) != 1 {
					t.Fatal(entries)
				}
				a := entries[0].(map[string]any)
				if a["planner_delivered"] != true || a["critic_delivered"] != true {
					t.Fatal(a)
				}
				if channel == "command" || channel == "file" || channel == "terminal" {
					s, _ := store.Open(f.Workspace)
					defer s.Close()
					app, e := human.LoadApplication(s, fmt.Sprint(a["receipt_hash"]))
					if e != nil {
						t.Fatal(e)
					}
					b, e := s.ReadText(app.AnswerPath)
					if e != nil || string(b) != answer {
						t.Fatalf("exact answer lost: %q %v", b, e)
					}
				}
			})
		}
	}
}

func TestALOOP06ExternalAndAmendedInputs(t *testing.T) {
	for _, path := range []string{"SPEC.md", "BRIEF.md", "CONSTRAINTS.md"} {
		t.Run(path, func(t *testing.T) {
			f := newFixture(t, "codex", false, true, AgentPlan{})
			if _, err := f.run(t); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.Workspace, path), []byte("# Explicit new input\n"), 0600); err != nil {
				t.Fatal(err)
			}
			f.Request.Resolved = nil
			f.Request.Resume = true
			_, err := f.run(t)
			requireCode(t, err, "ARTIFACT_CHANGED")
			if len(f.calls()) != 1 {
				t.Fatal("conflict launched agent")
			}
			if path == "SPEC.md" {
				f.Request.AmendSpec = true
				data, err := f.run(t)
				if err != nil {
					t.Fatal(err)
				}
				if data["status"] != "approved" || len(f.calls()) != 2 {
					t.Fatal(data, f.calls())
				}
			}
		})
	}
}

func TestALOOP06ExternalAfterCheckedTurn(t *testing.T) {
	for _, planner := range []string{"claude", "codex"} {
		for _, role := range []string{"planner", "critic"} {
			t.Run(planner+"/"+role, func(t *testing.T) {
				f := newFixture(t, planner, false, role == "critic", AgentPlan{})
				f.Options.Hook = func(name string, s *store.Store) error {
					if name == "engine.turn.checked" {
						return os.WriteFile(filepath.Join(s.Path, "SPEC.md"), []byte("# External edit after checked completion\n"), 0600)
					}
					return nil
				}
				_, err := f.run(t)
				requireCode(t, err, "ARTIFACT_CHANGED")
				if len(f.calls()) != 1 || f.manifest(t).String("status") == "approved" {
					t.Fatal("external edit reached approval")
				}
			})
		}
	}
}

func TestALOOP06InputAfterFinalSnapshot(t *testing.T) {
	f := newFixture(t, "codex", false, true, AgentPlan{})
	var id string
	f.Options.Hook = func(name string, s *store.Store) error {
		if name == "engine.final.committed" {
			id = submit(t, f, "steer", "A new directive after final commit.\n")
		}
		return nil
	}
	data, err := f.run(t)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok := data["pending_inputs"].([]string)
	if !ok || len(pending) != 1 || pending[0] != id || len(f.calls()) != 1 {
		t.Fatal(data, f.calls())
	}
	f.Options.Hook = nil
	f.Request.Resolved = nil
	if _, err = f.run(t); err != nil || len(f.calls()) != 1 {
		t.Fatal("saved result changed", err)
	}
}
