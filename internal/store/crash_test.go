//go:build darwin || linux

package store

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These subprocesses use the test executable and owned fixture files. There is
// no model executable or vendor command in the process tree.
func TestASTATEHelper(t *testing.T) {
	start := -1
	for i, arg := range os.Args {
		if arg == "--" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return
	}
	args := os.Args[start:]
	if len(args) < 2 {
		t.Fatal("helper arguments")
	}
	mode, path := args[0], args[1]
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if mode == "probe" {
		err := s.AcquireOwner(context.Background(), 30*time.Millisecond)
		requireCode(t, err, "LOCKED")
		fmt.Println("LOCKED")
		return
	}
	if err := s.AcquireOwner(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if mode == "owner" {
		if err := os.WriteFile(filepath.Join(path, "ready.signal"), []byte("owned"), 0600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	if len(args) != 3 {
		t.Fatal("missing fault site")
	}
	site := args[2]
	var operation func() error
	switch mode {
	case "intent":
		tx := nextIntent(t, s)
		operation = func() error { return s.CommitTransaction(tx) }
	case "result":
		operation = resultOperation(t, s, false)
	case "final":
		operation = resultOperation(t, s, true)
	case "receipt":
		tx := nextIntent(t, s)
		if err := s.CommitTransaction(tx); err != nil {
			t.Fatal(err)
		}
		id, r := receiptFixture(t, s)
		operation = func() error { _, err := s.SaveTurnReceipt(id, r); return err }
	case "inbox":
		r := inputFixture(t, s, "crash-key")
		operation = func() error { _, err := s.SubmitInput(context.Background(), r, time.Second); return err }
	default:
		t.Fatal("unknown helper mode")
	}
	s.Fault = func(name string) error {
		if name == site {
			os.Exit(91)
		}
		return nil
	}
	if err := operation(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("fault site not reached")
}
func helperCommand(t *testing.T, mode, path, site string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestASTATEHelper$", "--", mode, path}
	if site != "" {
		args = append(args, site)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = []string{"HOME=" + path, "TMPDIR=" + path, "PATH=" + filepath.Join(path, "empty-tools")}
	return cmd
}
func dropOwner(t *testing.T, s *Store) {
	t.Helper()
	if err := s.owner.Close(); err != nil {
		t.Fatal(err)
	}
	s.owner = nil
}
func TestASTATE01And02ProcessDeath(t *testing.T) {
	for _, mode := range []string{"intent", "receipt", "result", "final", "inbox"} {
		prep := func(t *testing.T, s *Store) func() error {
			switch mode {
			case "intent":
				tx := nextIntent(t, s)
				return func() error { return s.CommitTransaction(tx) }
			case "receipt":
				tx := nextIntent(t, s)
				if err := s.CommitTransaction(tx); err != nil {
					t.Fatal(err)
				}
				id, r := receiptFixture(t, s)
				return func() error { _, err := s.SaveTurnReceipt(id, r); return err }
			case "inbox":
				r := inputFixture(t, s, "crash-key")
				return func() error { _, err := s.SubmitInput(context.Background(), r, time.Second); return err }
			default:
				return resultOperation(t, s, mode == "final")
			}
		}
		for _, site := range faultSites(t, prep) {
			t.Run(mode+"/"+site, func(t *testing.T) {
				s := fixture(t)
				dropOwner(t, s)
				output, err := helperCommand(t, mode, s.Path, site).CombinedOutput()
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 91 {
					t.Fatalf("helper did not die at %s: %v %s", site, err, output)
				}
				if err := s.AcquireOwner(context.Background(), time.Second); err != nil {
					t.Fatal(err)
				}
				r, err := s.Recover()
				if err != nil {
					t.Fatal(err)
				}
				if mode == "intent" && r.Unfinished {
					if err := s.ReadyIntent(activeIntentID(t, s)); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "receipt" && !r.Unfinished {
					t.Fatal("receipt death marked completion")
				}
				if mode == "inbox" {
					inbox, err := s.ReadInbox(context.Background(), r.Snapshot.String("run_id"), time.Second)
					if err != nil {
						t.Fatal(err)
					}
					if len(inbox.Entries) > 1 {
						t.Fatal("duplicate submission")
					}
				}
			})
		}
	}
}
func TestASTATE04Owners(t *testing.T) {
	t.Run("alias-and-owner-death", func(t *testing.T) {
		s := fixture(t)
		tx := nextIntent(t, s)
		if err := s.CommitTransaction(tx); err != nil {
			t.Fatal(err)
		}
		dropOwner(t, s)
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(s.Path, alias); err != nil {
			t.Fatal(err)
		}
		owner := helperCommand(t, "owner", s.Path, "")
		if err := owner.Start(); err != nil {
			t.Fatal(err)
		}
		done := false
		t.Cleanup(func() {
			if !done {
				_ = owner.Process.Kill()
				_ = owner.Wait()
			}
		})
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(s.Path, "ready.signal")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("owner readiness timeout")
			}
			time.Sleep(5 * time.Millisecond)
		}
		output, err := helperCommand(t, "probe", alias, "").CombinedOutput()
		if err != nil || !strings.Contains(string(output), "LOCKED") {
			t.Fatalf("alias owner: %v %s", err, output)
		}
		if err := owner.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = owner.Wait()
		done = true
		if err := s.AcquireOwner(context.Background(), time.Second); err != nil {
			t.Fatal(err)
		}
		r, err := s.Recover()
		if err != nil || !r.Unfinished || r.Receipt != nil {
			t.Fatalf("owner death settled active evidence: %+v %v", r, err)
		}
	})
	t.Run("symlink-state", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "state")); err != nil {
			t.Fatal(err)
		}
		s, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		requireCode(t, s.Prepare(), "STATE_INVALID")
		names, _ := os.ReadDir(outside)
		if len(names) != 0 {
			t.Fatal("wrote outside owned workspace")
		}
	})
	t.Run("replaced-lock", func(t *testing.T) {
		s := fixture(t)
		if err := os.Rename(filepath.Join(s.Path, "state/owner.lock"), filepath.Join(s.Path, "state/old.lock")); err != nil {
			t.Fatal(err)
		}
		put(t, s, "state/owner.lock", "")
		requireCode(t, s.StageErrorForTest(), "STATE_INVALID")
	})
}
func (s *Store) StageErrorForTest() error {
	_, err := s.StageText("state/logs/out", []byte("text"))
	return err
}
