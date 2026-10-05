//go:build darwin || linux

package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
)

const optionalReview = "The design meets the requirements. You can also provide an example for readers.\nVERDICT: APPROVE\n"
const advisoryReview = "Consider a different example.\nVERDICT: REVISE\n"

func enableAuxiliary(f *fixture, advisory, closing bool, cap int) {
	f.Request.Resolved.Settings.SecondOpinion = advisory
	f.Request.Resolved.Settings.ClosingPass = closing
	f.Request.Resolved.Settings.MaxRounds = cap
	f.Settings = f.Request.Resolved.Settings
}
func readOwned(t *testing.T, f *fixture, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.Workspace, path))
	if err != nil {
		t.Fatal(path, err)
	}
	return b
}
func checkApproved(t *testing.T, f *fixture, data map[string]any, err error, result string) {
	t.Helper()
	if err != nil || data["status"] != "approved" {
		t.Fatal(data, err)
	}
	final, ok := data["final_result"].(map[string]any)
	if !ok || final["closing_result"] != result {
		t.Fatal(final)
	}
	if err = contract.Validate("final-result", final); err != nil {
		t.Fatal(err)
	}
	if err = contract.Validate("data-run", data); err != nil {
		t.Fatal(err)
	}
	m := f.manifest(t)
	if contract.HashBytes(readOwned(t, f, "SPEC.md")) != m.Object("approval")["spec_hash"] {
		t.Fatal("final hash differs")
	}
	before := len(f.calls())
	f.Request.Resume = true
	f.Request.Resolved = nil
	data, err = f.run(t)
	if err != nil || data["status"] != "approved" || len(f.calls()) != before {
		t.Fatal("saved approval changed", data, err, f.calls())
	}
}
func TestAFINAL01Auxiliary(t *testing.T) {
	for _, planner := range []string{"claude", "codex"} {
		for _, persistent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/persistent-%v", planner, persistent), func(t *testing.T) {
				f := newFixture(t, planner, persistent, true, AgentPlan{Critiques: []string{optionalReview}, Advisory: advisoryReview, ClosingUnchanged: true})
				enableAuxiliary(f, true, true, 2)
				data, err := f.run(t)
				checkApproved(t, f, data, err, "unchanged")
				if len(f.calls()) != 3 || !strings.HasSuffix(f.calls()[1], " advisory") || !strings.HasSuffix(f.calls()[2], " closing") {
					t.Fatal(f.calls())
				}
				if string(readOwned(t, f, "rounds/second-opinion.md")) != advisoryReview || string(readOwned(t, f, "rounds/r01.critique.md")) != optionalReview {
					t.Fatal("complete reviews differ")
				}
				captures, _ := os.ReadDir(f.Capture)
				var ordinary, advisory map[string]any
				foundClosing := false
				for _, entry := range captures {
					if filepath.Ext(entry.Name()) != ".json" {
						continue
					}
					b, _ := os.ReadFile(filepath.Join(f.Capture, entry.Name()))
					var c map[string]any
					_ = json.Unmarshal(b, &c)
					turn := c["turn"].(map[string]any)
					switch turn["purpose"] {
					case "critique":
						ordinary = c
					case "advisory":
						advisory = c
					case "closing":
						foundClosing = strings.Contains(c["prompt"].(string), "second-opinion.md") && strings.Contains(c["prompt"].(string), "critique")
					}
				}
				if !foundClosing {
					t.Fatal("closing lacks both review paths")
				}
				// All auxiliary launches have the shared guard and receipts. Advisory cannot
				// reuse the persistent critic session or replace the main ordinary receipt.
				m := f.manifest(t)
				s, e := store.Open(f.Workspace)
				if e != nil {
					t.Fatal(e)
				}
				defer s.Close()
				state, e := LoadState(s, m)
				if e != nil {
					t.Fatal(e)
				}
				main, _, e := s.ReadRecord("receipt", state.OrdinaryReceipt.Path)
				if e != nil {
					t.Fatal(e)
				}
				fresh, _, e := s.ReadRecord("receipt", state.Auxiliary.AdvisoryReceipt.Path)
				if e != nil {
					t.Fatal(e)
				}
				mainCompletion := main["identity"].(map[string]any)
				freshCompletion := fresh["identity"].(map[string]any)
				if persistent && mainCompletion["session_id"] == freshCompletion["session_id"] {
					t.Fatal("advisory reused ordinary session", ordinary, advisory)
				}
				if m.Object("verdict")["value"] != "APPROVE" || state.LastCritique != "rounds/r01.critique.md" {
					t.Fatal("advisory changed verdict")
				}
			})
		}
	}
}
func TestAFINAL02AtCap(t *testing.T) {
	for _, planner := range []string{"claude"} {
		t.Run(planner, func(t *testing.T) {
			f := newFixture(t, planner, true, true, AgentPlan{Critiques: []string{optionalReview}, Advisory: advisoryReview})
			enableAuxiliary(f, true, true, 1)
			data, err := f.run(t)
			checkApproved(t, f, data, err, "skipped_no_round_left")
			if len(f.calls()) != 2 || f.manifest(t).Object("auxiliary")["skip_reason"] != "no_round_left" {
				t.Fatal(f.calls())
			}
			if _, err = os.Stat(filepath.Join(f.Workspace, "rounds/closing-approved.spec.md")); !os.IsNotExist(err) {
				t.Fatal("closing snapshot at cap", err)
			}
		})
	}
}
func TestAFINAL03SameAndChanged(t *testing.T) {
	for _, planner := range []string{"claude"} {
		for _, unchanged := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unchanged-%v", planner, unchanged), func(t *testing.T) {
				f := newFixture(t, planner, true, true, AgentPlan{Critiques: []string{optionalReview}, ClosingUnchanged: unchanged})
				enableAuxiliary(f, false, true, 2)
				data, err := f.run(t)
				result := "confirmed"
				calls := 3
				if unchanged {
					result = "unchanged"
					calls = 2
				}
				checkApproved(t, f, data, err, result)
				if len(f.calls()) != calls {
					t.Fatal(f.calls())
				}
				if string(readOwned(t, f, "rounds/closing-approved.spec.md")) != "# Owned input\nBuild the owned fixture.\n" {
					t.Fatal("snapshot differs")
				}
				if !unchanged && !strings.HasSuffix(f.calls()[2], " confirmation") {
					t.Fatal(f.calls())
				}
			})
		}
	}
}
func TestAFINAL04Confirmation(t *testing.T) {
	for _, planner := range []string{"claude"} {
		for _, variant := range []string{"revise-later", "missing-later", "restore-revise", "restore-missing"} {
			t.Run(planner+"/"+variant, func(t *testing.T) {
				critiques := []string{optionalReview, "Changes require correction.\nVERDICT: REVISE\n", "Updated design.\nVERDICT: APPROVE\n"}
				if strings.Contains(variant, "missing") {
					critiques = []string{optionalReview, "No verdict received.\n", "Again no final verdict.\n", "Updated design.\nVERDICT: APPROVE\n"}
				}
				f := newFixture(t, planner, true, true, AgentPlan{Critiques: critiques})
				cap := 3
				result := "confirmed"
				if strings.HasPrefix(variant, "restore") {
					cap = 2
					result = "rejected_at_cap"
				}
				enableAuxiliary(f, false, true, cap)
				data, err := f.run(t)
				checkApproved(t, f, data, err, result)
				count := 0
				for _, call := range f.calls() {
					if strings.HasSuffix(call, " closing") {
						count++
					}
				}
				if count != 1 {
					t.Fatal("closing repeated", f.calls())
				}
				if result == "rejected_at_cap" {
					if string(readOwned(t, f, "SPEC.md")) != string(readOwned(t, f, "rounds/closing-approved.spec.md")) {
						t.Fatal("wrong restoration")
					}
					if string(readOwned(t, f, "rounds/closing-rejected.spec.md")) == string(readOwned(t, f, "SPEC.md")) {
						t.Fatal("rejected bytes lost")
					}
					final := data["final_result"].(map[string]any)
					reviews := final["rejected_review_evidence"].([]RejectedReview)
					want := 1
					if strings.Contains(variant, "missing") {
						want = 2
					}
					if len(reviews) != want {
						t.Fatal(reviews)
					}
					for _, r := range reviews {
						if contract.HashBytes(readOwned(t, f, r.Path)) != r.Hash || r.SpecHash == f.manifest(t).String("spec_hash") {
							t.Fatal("review bound to restored bytes", r)
						}
					}
				}
			})
		}
	}
}
func TestAFINAL05QuestionsAndDisabled(t *testing.T) {
	for _, planner := range []string{"claude"} {
		for _, variant := range []string{"question-changed", "question-same", "both-disabled", "closing-disabled"} {
			t.Run(planner+"/"+variant, func(t *testing.T) {
				plan := AgentPlan{Advisory: advisoryReview}
				question := strings.HasPrefix(variant, "question")
				if question {
					plan.QuestionPurpose = "closing"
					plan.ClosingUnchanged = variant == "question-same"
				}
				f := newFixture(t, planner, true, true, plan)
				enableAuxiliary(f, variant == "closing-disabled", question, 3)
				data, err := f.run(t)
				result := "disabled"
				if question {
					requireCode(t, err, "ANSWER_REQUIRED")
					if len(f.calls()) != 2 {
						t.Fatal("confirmation before answer", f.calls())
					}
					submit(t, f, "answer", "Use A.\n")
					f.setPlan(t, AgentPlan{})
					f.Request.Resume = true
					f.Request.Resolved = nil
					data, err = f.run(t)
					result = "confirmed"
					if len(f.calls()) != 4 || !strings.HasSuffix(f.calls()[2], "directive") || !strings.HasSuffix(f.calls()[3], "confirmation") {
						t.Fatal(f.calls())
					}
				}
				checkApproved(t, f, data, err, result)
				if !question {
					want := 1
					if variant == "closing-disabled" {
						want = 2
					}
					if len(f.calls()) != want {
						t.Fatal(f.calls())
					}
				}
			})
		}
	}
}
func TestAFINAL06ActualCrashes(t *testing.T) {
	seams := []string{"closing.approved.before", "closing.approved.after", "engine.receipt.saved", "engine.result.committed", "restoration.rejected.before", "restoration.rejected.after", "restoration.intent.before", "restoration.intent.after", "restoration.replace.before", "restoration.replace.after", "restoration.receipt.before", "restoration.receipt.after", "engine.final.before", "engine.final.committed"}
	for _, planner := range []string{"claude"} {
		for _, seam := range seams {
			t.Run(planner+"/"+seam, func(t *testing.T) {
				f := newFixture(t, planner, true, true, AgentPlan{Critiques: []string{optionalReview, "Reject changes.\nVERDICT: REVISE\n"}})
				enableAuxiliary(f, false, true, 2)
				purpose := ""
				if seam == "engine.receipt.saved" || seam == "engine.result.committed" {
					purpose = "closing"
				}
				if seam == "engine.final.before" || seam == "engine.final.committed" {
					purpose = "revision"
				}
				spec := controllerSpec{f.Request, f.Options.Env, seam, purpose}
				path := filepath.Join(f.Root, "controller.json")
				b, _ := json.Marshal(spec)
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
				exe, _ := os.Executable()
				cmd := exec.Command(exe, "-test.run=^TestEngineControllerChild$", "--", "--engine-controller", path)
				cmd.Env = f.Options.Env
				cmd.Dir = f.Root
				output, err := cmd.CombinedOutput()
				status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
				if err == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
					t.Fatalf("not SIGKILL: %v %s", err, output)
				}
				if _, err = os.Stat(path + ".kill"); err != nil {
					t.Fatal("seam not reached", err)
				}
				proof, _ := json.Marshal(map[string]any{"signal": "SIGKILL", "wait_status": int(status), "seam": seam, "controller_pid": cmd.Process.Pid})
				_ = os.WriteFile(filepath.Join(f.Root, "crash-proof.json"), proof, 0600)
				before := len(f.calls())
				f.Request.Resume = true
				f.Request.Resolved = nil
				data, err := f.run(t)
				if seam == "closing.approved.after" {
					requireCode(t, err, "TURN_UNCERTAIN")
					if len(f.calls()) != before || data["status"] != "handover" {
						t.Fatal(data, f.calls())
					}
					return
				}
				checkApproved(t, f, data, err, "rejected_at_cap")
				if strings.HasPrefix(seam, "restoration.") || strings.HasPrefix(seam, "engine.final.") {
					if len(f.calls()) != before {
						t.Fatal("restoration launched agent", f.calls())
					}
				}
				count := 0
				for _, call := range f.calls() {
					if strings.HasSuffix(call, " closing") {
						count++
					}
				}
				if count != 1 {
					t.Fatal("closing repeated", f.calls())
				}
			})
		}
	}
}

func TestAFINAL04RestorationBlocked(t *testing.T) {
	for _, planner := range []string{"claude"} {
		for _, variant := range []string{"pending-steer", "human-file", "question-file", "changed-basis", "external-spec", "unsettled-closing"} {
			t.Run(planner+"/"+variant, func(t *testing.T) {
				f := newFixture(t, planner, true, true, AgentPlan{Critiques: []string{optionalReview, "Reject changes.\nVERDICT: REVISE\n"}})
				enableAuxiliary(f, false, true, 2)
				fired := false
				f.Options.Hook = func(name string, s *store.Store) error {
					if fired {
						return nil
					}
					if variant == "unsettled-closing" && name == "closing.approved.after" {
						fired = true
						return failure("TURN_UNCERTAIN", "Owned test interrupts before launch", nil)
					}
					if variant == "changed-basis" && name == "engine.result.committed" {
						m, _, e := s.LoadSnapshot()
						if e != nil {
							return e
						}
						state, e := LoadState(s, m)
						if e != nil {
							return e
						}
						if state.Auxiliary.ClosingDone && m.Object("verdict")["value"] == "APPROVE" {
							fired = true
							submit(t, f, "steer", "Add a binding requirement.\n")
						}
						return nil
					}
					if name != "restoration.intent.after" {
						return nil
					}
					fired = true
					switch variant {
					case "pending-steer":
						submit(t, f, "steer", "Preserve the new decision.\n")
					case "human-file":
						return os.WriteFile(filepath.Join(f.Workspace, "HUMAN.md"), []byte("New decision.\n"), 0600)
					case "question-file":
						return os.WriteFile(filepath.Join(f.Workspace, "QUESTIONS.md"), []byte("Choose A or B.\n"), 0600)
					case "external-spec":
						return os.WriteFile(filepath.Join(f.Workspace, "SPEC.md"), []byte("Unrelated edit.\n"), 0600)
					}
					return nil
				}
				data, err := f.run(t)
				code := "REVIEW_IMPASSE"
				if variant == "external-spec" {
					code = "ARTIFACT_CHANGED"
				}
				if variant == "unsettled-closing" {
					code = "TURN_UNCERTAIN"
				}
				requireCode(t, err, code)
				if !fired || data["status"] == "approved" {
					t.Fatal(data, err)
				}
				approved := readOwned(t, f, "rounds/closing-approved.spec.md")
				if variant != "unsettled-closing" && string(readOwned(t, f, "SPEC.md")) == string(approved) {
					t.Fatal("blocked restoration replaced spec")
				}
				before := len(f.calls())
				f.Request.Resolved = nil
				f.Request.Resume = true
				f.Options.Hook = nil
				_, err = f.run(t)
				requireCode(t, err, code)
				if len(f.calls()) != before {
					t.Fatal("blocked resume called agent", f.calls())
				}
			})
		}
	}
}
func TestAFINALAuxiliaryGuard(t *testing.T) {
	for _, planner := range []string{"claude"} {
		for _, purpose := range []string{"advisory", "closing"} {
			for _, path := range []string{"HUMAN.md", "state/manifest.json", "@stdout"} {
				for _, failed := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/failed-%v", planner, purpose, path, failed), func(t *testing.T) {
						role := "critic"
						if purpose == "closing" {
							role = "planner"
						}
						f := newFixture(t, planner, true, true, AgentPlan{MutationRole: role, MutationPurpose: purpose, MutationPath: path, Fail: failed})
						enableAuxiliary(f, purpose == "advisory", purpose == "closing", 2)
						data, err := f.run(t)
						code := "PLANNER_MUTATION"
						if role == "critic" {
							code = "CRITIC_MUTATION"
						}
						requireCode(t, err, code)
						if calls := f.calls(); len(calls) == 0 || !strings.HasSuffix(calls[len(calls)-1], " "+purpose) {
							t.Fatal("guard did not reach auxiliary turn", calls)
						}
						if data["status"] == "approved" {
							t.Fatal(data)
						}
						calls := len(f.calls())
						f.Request.Resolved = nil
						f.Request.Resume = true
						_, _ = f.run(t)
						if len(f.calls()) != calls {
							t.Fatal("guard failure repeated auxiliary")
						}
					})
				}
			}
		}
	}
}
func TestAFINALRestoredEvidenceTamper(t *testing.T) {
	for _, planner := range []string{"claude"} {
		t.Run(planner, func(t *testing.T) {
			f := newFixture(t, planner, true, true, AgentPlan{Critiques: []string{optionalReview, "Reject changes.\nVERDICT: REVISE\n"}})
			enableAuxiliary(f, false, true, 2)
			data, err := f.run(t)
			checkApproved(t, f, data, err, "rejected_at_cap")
			m := f.manifest(t)
			s, e := store.Open(f.Workspace)
			if e != nil {
				t.Fatal(e)
			}
			state, e := LoadState(s, m)
			s.Close()
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(f.Workspace, state.Auxiliary.RestorationReceipt.Path), []byte("{}"), 0600); e != nil {
				t.Fatal(e)
			}
			before := len(f.calls())
			_, err = f.run(t)
			requireCode(t, err, "ARTIFACT_CHANGED")
			if len(f.calls()) != before {
				t.Fatal("tamper launched agent")
			}
		})
	}
}

func TestAFINAL06InterruptedRestorationNewInput(t *testing.T) {
	for _, planner := range []string{"claude"} {
		for _, seam := range []string{"restoration.intent.after", "restoration.replace.after", "restoration.receipt.after"} {
			for _, change := range []string{"steer", "external-spec"} {
				t.Run(planner+"/"+seam+"/"+change, func(t *testing.T) {
					f := newFixture(t, planner, true, true, AgentPlan{Critiques: []string{optionalReview, "Reject changes.\nVERDICT: REVISE\n"}})
					enableAuxiliary(f, false, true, 2)
					path := filepath.Join(f.Root, "controller.json")
					b, _ := json.Marshal(controllerSpec{f.Request, f.Options.Env, seam, ""})
					if err := os.WriteFile(path, b, 0600); err != nil {
						t.Fatal(err)
					}
					exe, _ := os.Executable()
					cmd := exec.Command(exe, "-test.run=^TestEngineControllerChild$", "--", "--engine-controller", path)
					cmd.Env = f.Options.Env
					cmd.Dir = f.Root
					output, err := cmd.CombinedOutput()
					status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
					if err == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
						t.Fatalf("not SIGKILL: %v %s", err, output)
					}
					proof, _ := json.Marshal(map[string]any{"signal": "SIGKILL", "wait_status": int(status), "seam": seam, "controller_pid": cmd.Process.Pid})
					_ = os.WriteFile(filepath.Join(f.Root, "crash-proof.json"), proof, 0600)
					before := len(f.calls())
					prior := readOwned(t, f, "SPEC.md")
					code := "REVIEW_IMPASSE"
					if change == "steer" {
						submit(t, f, "steer", "A new binding decision.\n")
					} else {
						code = "ARTIFACT_CHANGED"
						prior = []byte("Unrelated replacement.\n")
						if err = os.WriteFile(filepath.Join(f.Workspace, "SPEC.md"), prior, 0600); err != nil {
							t.Fatal(err)
						}
					}
					f.Request.Resume = true
					f.Request.Resolved = nil
					data, err := f.run(t)
					requireCode(t, err, code)
					if data["status"] == "approved" || len(f.calls()) != before || string(readOwned(t, f, "SPEC.md")) != string(prior) {
						t.Fatal("input lost or falsely approved", data, err, f.calls())
					}
					if change == "steer" && seam != "restoration.intent.after" {
						m := f.manifest(t)
						s, e := store.Open(f.Workspace)
						if e != nil {
							t.Fatal(e)
						}
						state, e := LoadState(s, m)
						s.Close()
						if e != nil || state.Auxiliary.RestorationReceipt == nil {
							t.Fatal("completed replacement lost receipt", e)
						}
					}
				})
			}
		}
	}
}

func TestAFINAL03ApprovalAfterReminder(t *testing.T) {
	for _, planner := range []string{"claude"} {
		t.Run(planner, func(t *testing.T) {
			f := newFixture(t, planner, true, true, AgentPlan{Critiques: []string{"No final verdict.\n", optionalReview}, ClosingUnchanged: true})
			enableAuxiliary(f, true, true, 2)
			f.Request.Resolved.Settings.Rubric = "security"
			f.Settings = f.Request.Resolved.Settings
			data, err := f.run(t)
			checkApproved(t, f, data, err, "unchanged")
			entries, _ := os.ReadDir(f.Capture)
			found := false
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				b, _ := os.ReadFile(filepath.Join(f.Capture, entry.Name()))
				var c map[string]any
				_ = json.Unmarshal(b, &c)
				if c["turn"].(map[string]any)["purpose"] == "closing" {
					found = strings.Contains(c["prompt"].(string), "r01.critique-retry.md") && strings.Contains(c["prompt"].(string), "decline suggestions")
				}
			}
			if !found {
				t.Fatal("closing lacks accepted reminder review or decline option")
			}
			if len(f.calls()) != 4 {
				t.Fatal(f.calls())
			}
		})
	}
}

func TestAFINAL06QuestionBeforeFinal(t *testing.T) {
	for _, planner := range []string{"claude"} {
		t.Run(planner, func(t *testing.T) {
			f := newFixture(t, planner, true, true, AgentPlan{Critiques: []string{optionalReview, "Reject changes.\nVERDICT: REVISE\n"}})
			enableAuxiliary(f, false, true, 2)
			fired := false
			f.Options.Hook = func(name string, s *store.Store) error {
				if name == "engine.final.before" && !fired {
					m, _, e := s.LoadSnapshot()
					if e != nil {
						return e
					}
					state, e := LoadState(s, m)
					if e != nil {
						return e
					}
					if state.Auxiliary.RestorationReceipt != nil {
						fired = true
						return os.WriteFile(filepath.Join(f.Workspace, "QUESTIONS.md"), []byte("A new decision remains open.\n"), 0600)
					}
				}
				return nil
			}
			data, err := f.run(t)
			requireCode(t, err, "REVIEW_IMPASSE")
			if !fired || data["status"] != "impasse" {
				t.Fatal(data, err)
			}
			if len(data["pending_inputs"].([]string)) != 1 {
				t.Fatal("question not reported", data)
			}
			before := len(f.calls())
			f.Request.Resolved = nil
			f.Request.Resume = true
			f.Options.Hook = nil
			_, err = f.run(t)
			requireCode(t, err, "REVIEW_IMPASSE")
			if len(f.calls()) != before {
				t.Fatal("question resumed an agent")
			}
		})
	}
}
