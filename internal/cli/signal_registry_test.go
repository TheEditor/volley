//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/engine"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/store"
	"github.com/TheEditor/volley/tests/ownedagent"
)

func TestCLIControllerChild(t *testing.T) {
	for i, a := range os.Args {
		if a == "owned-cli" {
			os.Exit(Execute(context.Background(), os.Args[i+1:], os.Stdout, os.Stderr, Options{RequestID: func() (string, error) { return os.Getenv("OWNED_REQUEST_ID"), nil }, RunOptions: &engine.Options{Env: os.Environ(), Runner: process.UnixRunner{}}}))
		}
		if a == "owned-lock" {
			s, e := store.Open(os.Args[i+1])
			if e != nil {
				os.Exit(91)
			}
			defer s.Close()
			if e = s.AcquireOwner(context.Background(), 0); e != nil {
				os.Exit(92)
			}
			if _, e = s.StagePrivateText("state/lock-ready", []byte("ready")); e != nil {
				os.Exit(93)
			}
			io.Copy(io.Discard, os.Stdin)
			return
		}
	}
}
func waitOwnedFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, e := os.ReadFile(path); e == nil && len(b) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned child did not reach ready boundary", path)
}
func TestContractRegistrySignals(t *testing.T) {
	for _, v := range []struct {
		id, code string
		signal   syscall.Signal
	}{{"A-CONTRACT-R39", "CONTROLLER_INTERRUPTED", syscall.SIGINT}, {"A-CONTRACT-R40", "CONTROLLER_STOPPED", syscall.SIGTERM}} {
		t.Run(v.id, func(t *testing.T) {
			f := newContractFixture(t, true)
			f.setPlan(t, ownedagent.Plan{Hold: true})
			exe, _ := os.Executable()
			args := []string{"--json", "run", f.ws, "--config", f.config}
			cmd := exec.Command(exe, append([]string{"-test.run=^TestCLIControllerChild$", "--", "owned-cli"}, args...)...)
			cmd.Env = append(append([]string{}, f.opts.RunOptions.Env...), "OWNED_REQUEST_ID="+v.id)
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			if e := cmd.Start(); e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { cmd.Process.Kill() })
			waitOwnedFile(t, filepath.Join(f.capture, "launches.txt"))
			if e := cmd.Process.Signal(v.signal); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("owned controller did not settle")
			}
			f.observe(t, v.id, v.code, args, cmd.ProcessState.ExitCode(), out.Bytes(), stderr.Bytes())
			if f.launches() != 1 {
				t.Fatal("signal started another turn")
			}
			if _, e := os.Stat(filepath.Join(f.ws, "state/manifest.json")); e != nil {
				t.Fatal("signal checkpoint absent", e)
			}
		})
	}
}
