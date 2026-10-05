//go:build darwin || linux

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
)

func TestALOOP05RequestKeys(t *testing.T) {
	for _, initialKey := range []string{"", "original"} {
		t.Run(fmt.Sprint(initialKey != ""), func(t *testing.T) {
			f := newFixture(t, "claude", false, true, AgentPlan{})
			f.Request.Key = initialKey
			if _, err := f.run(t); err != nil {
				t.Fatal(err)
			}
			runID := f.manifest(t).String("run_id")
			f.Request.Resolved = nil
			if _, err := f.run(t); err != nil {
				t.Fatal(err)
			}
			f.Request.Key = "new-matching"
			if _, err := f.run(t); err != nil {
				t.Fatal(err)
			}
			if f.manifest(t).String("run_id") != runID || len(f.calls()) != 1 {
				t.Fatal("second run or launch")
			}
			f.Request.Explicit = map[string]any{"claude_model": "different"}
			_, err := f.run(t)
			requireCode(t, err, "IDEMPOTENCY_CONFLICT")
			f.Request.Key = "unbound-conflicting"
			_, err = f.run(t)
			requireCode(t, err, "CONFIG_CONFLICT")
			for _, key := range stringsOf(f.manifest(t)["request_keys"]) {
				if key == "unbound-conflicting" {
					t.Fatal("failed key bound")
				}
			}
			f.Request.Key = ""
			_, err = f.run(t)
			requireCode(t, err, "CONFIG_CONFLICT")
		})
	}
}

func TestALOOP05FailedCreationDoesNotBind(t *testing.T) {
	for _, variant := range []string{"missing-basis", "empty-basis", "invalid-utf8", "missing-source", "different-target"} {
		t.Run(variant, func(t *testing.T) {
			f := newFixture(t, "claude", false, true, AgentPlan{})
			f.Request.Key = "unbound-on-failure"
			path := filepath.Join(f.Workspace, "SPEC.md")
			code := "INVALID_INPUT"
			switch variant {
			case "missing-basis":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "empty-basis":
				if err := os.WriteFile(path, []byte(" \n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid-utf8":
				if err := os.WriteFile(path, []byte{0xff}, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-source":
				f.Request.Seed = filepath.Join(f.Root, "absent.md")
				code = "NOT_FOUND"
			case "different-target":
				f.Request.Seed = filepath.Join(f.Root, "different.md")
				if err := os.WriteFile(f.Request.Seed, []byte("different seed\n"), 0600); err != nil {
					t.Fatal(err)
				}
				code = "CONFIG_CONFLICT"
			}
			_, err := f.run(t)
			requireCode(t, err, code)
			if _, err := os.Stat(filepath.Join(f.Workspace, "state/manifest.json")); !os.IsNotExist(err) {
				t.Fatal("failed creation saved a manifest", err)
			}
			if len(f.calls()) != 0 {
				t.Fatal("failed input launched an agent")
			}
			f.Request.Seed = ""
			if err := os.WriteFile(path, []byte("# Valid replacement\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := f.run(t); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestALOOP05StatusTable(t *testing.T) {
	for _, status := range []string{"ready", "running", "handover", "stopped", "live-owner"} {
		for _, key := range []string{"", "owned-key"} {
			t.Run(status+"/"+fmt.Sprint(key != ""), func(t *testing.T) {
				f := newFixture(t, "codex", false, true, AgentPlan{})
				f.Request.Key = key
				f.Options.Hook = func(name string, s *store.Store) error {
					if name == "engine.intent.committed" {
						return fmt.Errorf("owned preparation boundary")
					}
					return nil
				}
				_, err := f.run(t)
				if err == nil {
					t.Fatal("setup did not stop")
				}
				s, err := store.Open(f.Workspace)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.AcquireOwner(context.Background(), 0); err != nil {
					t.Fatal(err)
				}
				m, _, err := s.LoadSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				if status != "running" && status != "live-owner" {
					m["current_turn"] = nil
					m["status"] = status
					if status == "handover" {
						m["errors"] = []string{"CRITIC_MUTATION"}
					}
					tx, e := s.NewTransaction("control", m, nil, nil)
					if e != nil {
						t.Fatal(e)
					}
					if e = s.CommitTransaction(tx); e != nil {
						t.Fatal(e)
					}
				}
				if status != "live-owner" {
					s.Close()
				} else {
					defer s.Close()
				}
				f.Options.Hook = nil
				f.Request.Resolved = nil
				data, err := f.run(t)
				switch status {
				case "ready":
					if err != nil || data["status"] != "approved" {
						t.Fatal(data, err)
					}
				case "running":
					requireCode(t, err, "TURN_UNCERTAIN")
				case "handover":
					requireCode(t, err, "CRITIC_MUTATION")
				case "stopped":
					requireCode(t, err, "CONFIG_CONFLICT")
				case "live-owner":
					requireCode(t, err, "LOCKED")
					if data["run_id"] != m["run_id"] {
						t.Fatal("lost run handle")
					}
				}
				if status != "ready" && len(f.calls()) > 0 {
					t.Fatal("attachment launched agent")
				}
			})
		}
	}
}

func TestALOOP05SeedSources(t *testing.T) {
	f := newFixture(t, "claude", false, true, AgentPlan{})
	seed := filepath.Join(f.Root, "seed.md")
	text, _ := os.ReadFile(filepath.Join(f.Workspace, "SPEC.md"))
	if err := os.WriteFile(seed, text, 0600); err != nil {
		t.Fatal(err)
	}
	f.Request.Seed = seed
	if _, err := f.run(t); err != nil {
		t.Fatal(err)
	}
	f.Request.Resolved = nil
	if _, err := f.run(t); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(f.Root, "same-bytes-new-source.md")
	_ = os.WriteFile(other, text, 0600)
	f.Request.Seed = other
	_, err := f.run(t)
	requireCode(t, err, "CONFIG_CONFLICT")
	if len(f.calls()) != 1 {
		t.Fatal("source conflict launched agent")
	}
}

func TestALOOP06SavedEvidenceTamper(t *testing.T) {
	for _, kind := range []string{"critique", "raw-output", "receipt", "guard"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, "claude", false, true, AgentPlan{})
			if _, err := f.run(t); err != nil {
				t.Fatal(err)
			}
			m := f.manifest(t)
			s, err := store.Open(f.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			state, err := LoadState(s, m)
			s.Close()
			if err != nil {
				t.Fatal(err)
			}
			path := "rounds/r01.critique.md"
			switch kind {
			case "raw-output":
				path = filepath.Join(filepath.Dir(state.LastReceipt.Path), "stdout.pending")
			case "receipt":
				path = state.LastReceipt.Path
			case "guard":
				path = filepath.Join(filepath.Dir(state.LastReceipt.Path), "guard-result.json")
			}
			if err = os.WriteFile(filepath.Join(f.Workspace, path), []byte("tampered\n"), 0600); err != nil {
				t.Fatal(err)
			}
			f.Request.Resolved = nil
			_, err = f.run(t)
			if err == nil {
				t.Fatal("tampered saved approval returned")
			}
			var typed *contract.Error
			var stored *store.Error
			if !errors.As(err, &typed) && !errors.As(err, &stored) {
				t.Fatal(err)
			}
			if len(f.calls()) != 1 {
				t.Fatal("tamper launched agent")
			}
		})
	}
}
