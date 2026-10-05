//go:build darwin || linux

package process

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func fixtureArgs(mode string, more ...string) []string {
	return append([]string{"-test.run=^TestProcessFixture$", "--", "volley-owned-fixture", mode}, more...)
}
func TestProcessFixture(t *testing.T) {
	at := -1
	for i, v := range os.Args {
		if v == "volley-owned-fixture" {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	args := os.Args[at+1:]
	mode := args[0]
	switch mode {
	case "argv":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(11)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"args": args[1:], "stdin": string(b)})
	case "hold":
		signal.Ignore(syscall.SIGTERM)
		fmt.Fprintln(os.Stdout, "owned child ready")
		for {
			time.Sleep(time.Hour)
		}
	case "tree":
		cmd := exec.Command(os.Args[0], fixtureArgs("hold")...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = nil
		if err := cmd.Start(); err != nil {
			os.Exit(12)
		}
		fmt.Fprintf(os.Stdout, "descendant=%d\n", cmd.Process.Pid)
		_ = cmd.Wait()
	case "controller":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		result, err := (UnixRunner{}).Run(ctx, Request{Path: os.Args[0], Args: fixtureArgs("hold"), Env: []string{}, OnStart: func(id Identity) error { b, _ := json.Marshal(id); return os.WriteFile(args[1], b, 0600) }})
		if err != nil {
			os.Exit(13)
		}
		b, _ := json.Marshal(result)
		if err := os.WriteFile(args[2], b, 0600); err != nil {
			os.Exit(14)
		}
	default:
		os.Exit(15)
	}
	os.Exit(0)
}
func TestAPROC01DeadlineAndDescendants(t *testing.T) {
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	began := time.Now()
	result, err := (UnixRunner{}).Run(context.Background(), Request{Path: exe, Args: fixtureArgs("tree"), Cwd: root, Env: []string{"PATH=" + root}, Timeout: 350 * time.Millisecond, Stdout: &output})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != DeadlineExpired || !result.TimedOut || !result.Settled || result.RemainingMembers != 0 || len(result.ObservedMembers) < 2 || !reflect.DeepEqual(result.Signals, []string{"TERM", "KILL"}) {
		t.Fatalf("Deadline result: %+v", result)
	}
	if elapsed := time.Since(began); elapsed < 5*time.Second || elapsed > 7*time.Second {
		t.Fatalf("Grace or bounded wait: %s", elapsed)
	}
	if !strings.Contains(output.String(), "owned child ready") {
		t.Fatalf("Descendant fixture did not run: %s", output.String())
	}
	live, err := groupAlive(result.Identity.PGID)
	if err != nil || live != 0 {
		t.Fatalf("Group members remain: %d %v", live, err)
	}
	t.Logf("Result: %+v; group live=0; timeout executable absent", result)
	// A saved budget does not grant a new full turn budget.
	result, err = (UnixRunner{}).Run(context.Background(), Request{Path: exe, Args: fixtureArgs("hold"), Timeout: time.Second, ElapsedBefore: time.Second})
	if err != nil || result.Started || result.Outcome != DeadlineExpired {
		t.Fatalf("Saved budget: %+v %v", result, err)
	}
}
func TestAPROC02BoundariesAndStdin(t *testing.T) {
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A path with whitespace and a newline is still one executable path.
	path := filepath.Join(root, "owned fixture\nwith spaces")
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0700); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(root, "stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	stderr, err := os.Create(filepath.Join(root, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	args := []string{"model with spaces", "high effort", "reference path\ncontrol text", "$(not a command)", "", "日本"}
	result, err := (UnixRunner{}).Run(context.Background(), Request{Path: path, Args: fixtureArgs("argv", args...), Cwd: root, Env: []string{"PATH=" + root}, Timeout: 2 * time.Second, Stdout: out, Stderr: stderr})
	if err != nil || result.Exit != 0 || !result.Settled {
		t.Fatalf("Boundary result: %+v %v", result, err)
	}
	b, err = os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args  []string
		Stdin string
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Args, args) || got.Stdin != "" {
		t.Fatalf("Argv/stdin: %+v", got)
	}
	for _, bad := range []string{"a\nb", "a\rb", "a\tb", "--model", "a\x00b", string([]byte{0xff})} {
		if ValidateIdentifier(bad) == nil {
			t.Fatalf("Invalid model accepted: %q", bad)
		}
	}
	if err := ValidateIdentifier("model with spaces"); err != nil {
		t.Fatal(err)
	}
	result, err = (UnixRunner{}).Run(context.Background(), Request{Path: path, Args: []string{"a\x00b"}})
	if err == nil || result.Started || result.Outcome != NotStarted {
		t.Fatalf("NUL argument: %+v %v", result, err)
	}
	t.Logf("Exact argv=%q; stdin empty; stdout and stderr use separate files", got.Args)
}

func TestOwnedToolInputDoesNotReadControllerStdin(t *testing.T) {
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	payload := "  owned pointer\n日本\t"
	var output strings.Builder
	r, e := (UnixRunner{}).Run(context.Background(), Request{Path: exe, Args: fixtureArgs("argv"), Env: []string{}, Input: []byte(payload), Stdout: &output, Timeout: 2 * time.Second})
	if e != nil || r.Exit != 0 || !r.Settled {
		t.Fatal(r, e)
	}
	var got struct{ Stdin string }
	if e := json.Unmarshal([]byte(output.String()), &got); e != nil || got.Stdin != payload {
		t.Fatal(got, e)
	}
	if r, e := (UnixRunner{}).Run(context.Background(), Request{Path: exe, UserTTY: os.Stdin, Input: []byte(payload)}); e == nil || r.Started {
		t.Fatal("mixed input descriptors accepted", r, e)
	}
}
func TestAPROC03IdentityAndSignals(t *testing.T) {
	result, err := (UnixRunner{}).Run(context.Background(), Request{Path: filepath.Join(t.TempDir(), "absent")})
	if err == nil || result.Started || result.Outcome != NotStarted {
		t.Fatalf("Start failure: %+v %v", result, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(sig.String(), func(t *testing.T) {
			root := t.TempDir()
			identityFile := filepath.Join(root, "identity")
			resultFile := filepath.Join(root, "result")
			cmd := exec.Command(exe, fixtureArgs("controller", identityFile, resultFile)...)
			cmd.Env = []string{"PATH=" + root}
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var child Identity
			waitFile(t, identityFile, &child)
			defer func() {
				valid, _ := Inspect(child)
				if valid {
					_, _ = SignalVerified(child, syscall.SIGKILL)
				}
			}()
			// A reused PID or unverified record cannot authorize a signal.
			forged := child
			forged.Start += "/different"
			if ok, err := SignalVerified(forged, syscall.SIGKILL); ok || err != nil {
				t.Fatalf("Forged identity signalled: %v %v", ok, err)
			}
			unverified := child
			unverified.Verified = false
			if ok, _ := SignalVerified(unverified, syscall.SIGKILL); ok {
				t.Fatal("Unverified PID signalled")
			}
			if valid, err := Inspect(child); err != nil || !valid {
				t.Fatalf("Child changed: %v %v", valid, err)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if sig != syscall.SIGKILL && err != nil {
					t.Fatal(err)
				}
			case <-time.After(7 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("Controller did not settle")
			}
			if sig == syscall.SIGKILL {
				valid, err := Inspect(child)
				if err != nil || !valid {
					t.Fatalf("Retained child: %v %v", valid, err)
				}
				// Partial saved identity has uncertain ownership and leaves the child alone.
				partial := Identity{PID: child.PID, PGID: child.PGID}
				recovered, err := RecoverIdentity(partial)
				if err != nil || recovered.Outcome != Uncertain || recovered.Settled {
					t.Fatalf("Partial recovery: %+v %v", recovered, err)
				}
				if ok, _ := SignalVerified(partial, syscall.SIGKILL); ok {
					t.Fatal("Partial identity allowed kill")
				}
				if ok, err := SignalVerified(child, syscall.SIGKILL); err != nil || !ok {
					t.Fatalf("Owned fixture cleanup: %v %v", ok, err)
				}
				until := time.Now().Add(time.Second)
				for {
					live, _ := groupAlive(child.PGID)
					if live == 0 {
						break
					}
					if time.Now().After(until) {
						t.Fatal("Child remained alive")
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Logf("Controller SIGKILL left one live child; unverifiable saved identity is uncertain; verified fixture cleanup left 0")
			} else {
				var got Result
				waitFile(t, resultFile, &got)
				if got.Outcome != Interrupted || !got.Interrupted || !got.Settled || got.RemainingMembers != 0 {
					t.Fatalf("Interrupted: %+v", got)
				}
				t.Logf("Signal %s result: %+v", sig, got)
			}
		})
	}
}
func waitFile(t *testing.T, path string, target any) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(b, target) == nil {
			return
		}
		if time.Now().After(until) {
			t.Fatalf("Fixture receipt missing: %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func groupAlive(pgid int) (int, error) {
	all, err := platformProcesses()
	if err != nil {
		return 0, err
	}
	live := 0
	for _, p := range all {
		if p.PGID == pgid && !p.Zombie {
			live++
		}
	}
	return live, nil
}
func TestAPROC03UnknownSavedPID(t *testing.T) {
	saved := Identity{PID: os.Getpid(), PGID: unix.Getpgrp(), Start: "not the current start", Verified: true}
	if valid, err := Inspect(saved); err != nil || valid {
		t.Fatalf("Unknown start accepted: %v %v", valid, err)
	}
}

func TestAPROC03StartRecordFailure(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, panics := range []bool{false, true} {
		t.Run(fmt.Sprint(panics), func(t *testing.T) {
			var child Identity
			call := func() {
				defer func() {
					if p := recover(); p != nil && !panics {
						t.Fatalf("Unexpected panic: %v", p)
					}
				}()
				result, err := (UnixRunner{}).Run(context.Background(), Request{Path: exe, Args: fixtureArgs("hold"), OnStart: func(id Identity) error {
					child = id
					if panics {
						panic("owned test panic")
					}
					return fmt.Errorf("Owned start-record failure")
				}})
				if panics {
					t.Fatal("Panic not propagated")
				}
				if err == nil || result.Outcome != Uncertain || !result.Settled {
					t.Fatalf("Start-record failure: %+v %v", result, err)
				}
			}
			call()
			live, err := groupAlive(child.PGID)
			if err != nil || live != 0 {
				t.Fatalf("Start callback left a live group: %d %v", live, err)
			}
		})
	}
}

func TestEditorInputRefusesNonTerminal(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "not-terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result, err := (UnixRunner{}).Run(context.Background(), Request{Path: exe, Args: fixtureArgs("argv"), UserTTY: file})
	if err == nil || result.Started || result.Outcome != NotStarted {
		t.Fatalf("Non-terminal editor input accepted: %+v %v", result, err)
	}
}
