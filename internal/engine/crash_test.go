//go:build darwin || linux

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/TheEditor/volley/internal/human"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/store"
)

type controllerSpec struct {
	Request               Request
	Env                   []string
	KillHook, KillPurpose string
}

func TestEngineControllerChild(t *testing.T) {
	path := ""
	for i, arg := range os.Args {
		if arg == "--engine-controller" && i+1 < len(os.Args) {
			path = os.Args[i+1]
		}
	}
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		os.Exit(90)
	}
	var spec controllerSpec
	if json.Unmarshal(b, &spec) != nil {
		os.Exit(91)
	}
	kill := func(name string, s *store.Store) error {
		if name != spec.KillHook {
			return nil
		}
		if spec.KillPurpose != "" {
			m, _, err := s.LoadSnapshot()
			if err != nil {
				return err
			}
			purpose := m.Object("current_turn")["purpose"]
			if purpose == nil {
				state, e := LoadState(s, m)
				if e != nil {
					return e
				}
				if state.LastReceipt != nil {
					receipt, _, e := s.ReadRecord("receipt", state.LastReceipt.Path)
					if e != nil {
						return e
					}
					purpose = receipt["purpose"]
				}
			}
			if purpose != spec.KillPurpose {
				return nil
			}
		}
		f, err := os.OpenFile(path+".kill", os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if err != nil {
			os.Exit(92)
		}
		_, _ = f.WriteString(name)
		_ = f.Sync()
		_ = f.Close()
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
	options := Options{Env: spec.Env, Runner: process.UnixRunner{}, Clock: quickClock{}, Hook: kill}
	options.Fault = func(name string) error {
		if name == spec.KillHook {
			return kill(name, nil)
		}
		return nil
	}
	_, err = Run(context.Background(), spec.Request, options)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(93) // every listed case must die by the owned SIGKILL seam
}

func TestALOOP03ActualControllerCrashes(t *testing.T) {
	for _, planner := range []string{"claude", "codex"} {
		for _, seam := range []string{"human.application.commit.after", "engine.intent.committed", "engine.turn.checked", "engine.receipt.saved", "engine.result.committed", "engine.final.committed"} {
			t.Run(planner+"/"+seam, func(t *testing.T) {
				f := newFixture(t, planner, true, true, AgentPlan{})
				if err := os.WriteFile(filepath.Join(f.Workspace, "HUMAN.md"), []byte("  exact directive\r\nkept once\n"), 0600); err != nil {
					t.Fatal(err)
				}
				purpose := "directive"
				if seam == "human.application.commit.after" || seam == "engine.final.committed" {
					purpose = ""
				}
				spec := controllerSpec{f.Request, f.Options.Env, seam, purpose}
				path := filepath.Join(f.Root, "controller.json")
				b, err := json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
				exe, _ := os.Executable()
				cmd := exec.Command(exe, "-test.run=^TestEngineControllerChild$", "--", "--engine-controller", path)
				cmd.Env = f.Options.Env
				cmd.Dir = f.Root
				output, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatal("controller did not crash")
				}
				status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
				if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
					t.Fatalf("not SIGKILL: %v %s", err, output)
				}
				if _, err = os.Stat(path + ".kill"); err != nil {
					t.Fatal("fault seam not reached", err)
				}
				proof, _ := json.Marshal(map[string]any{"signal": "SIGKILL", "wait_status": int(status), "seam": seam, "controller_pid": cmd.Process.Pid})
				if err = os.WriteFile(filepath.Join(f.Root, "crash-proof.json"), proof, 0600); err != nil {
					t.Fatal(err)
				}
				before := len(f.calls())
				f.Request.Resolved = nil
				f.Request.Resume = true
				data, err := f.run(t)
				uncertain := seam == "engine.intent.committed" || seam == "engine.turn.checked"
				if uncertain {
					requireCode(t, err, "TURN_UNCERTAIN")
					if data["status"] != "handover" || len(f.calls()) != before {
						t.Fatal("uncertain delivery repeated", data, f.calls())
					}
				} else {
					if err != nil || data["status"] != "approved" {
						t.Fatal(data, err)
					}
				}
				m := f.manifest(t)
				entries := m["applications"].([]any)
				if len(entries) != 1 {
					t.Fatal("second logical selection", entries)
				}
				a := entries[0].(map[string]any)
				s, err := store.Open(f.Workspace)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				app, err := human.LoadApplication(s, fmt.Sprint(a["receipt_hash"]))
				if err != nil {
					t.Fatal(err)
				}
				text, err := s.ReadText(app.AnswerPath)
				if err != nil || string(text) != "  exact directive\r\nkept once\n" {
					t.Fatal("directive lost", err)
				}
				directiveCalls := 0
				for _, call := range f.calls() {
					if strings.Contains(call, " directive") {
						directiveCalls++
					}
				}
				if directiveCalls > 1 {
					t.Fatal("duplicate application process")
				}
			})
		}
	}
}
