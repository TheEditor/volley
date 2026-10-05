//go:build darwin || linux

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/store"
	"github.com/TheEditor/volley/tests/ownedagent"
)

type AgentPlan = ownedagent.Plan

func TestEngineAgentChild(t *testing.T) {
	if exit := ownedagent.Run(); exit >= 0 {
		os.Exit(exit)
	}
}

type quickClock struct{}

func (quickClock) Wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type fixture struct {
	Root, Workspace, Capture, Plan string
	Request                        Request
	Options                        Options
	Settings                       config.Settings
}

func newFixture(t *testing.T, planner string, persistent, seed bool, plan AgentPlan) *fixture {
	t.Helper()
	root := ""
	for i, arg := range os.Args {
		if arg == "--engine-evidence" && i+1 < len(os.Args) {
			base := os.Args[i+1]
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			id, _ := store.NewID()
			root = filepath.Join(base, strings.ReplaceAll(t.Name(), "/", "_")+"-"+id)
			break
		}
	}
	retained := root != ""
	if !retained {
		root = t.TempDir()
	}
	for _, name := range []string{"ws", "home", "tmp", "config", "state", "data", "cache", "runtime", "tools", "capture"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	root, _ = filepath.EvalSymlinks(root)
	exe, _ := os.Executable()
	flags := map[string]config.Override{}
	for _, provider := range []string{"claude", "codex"} {
		path := filepath.Join(root, "tools", provider)
		wrapper := "#!/bin/sh\nexec " + strconv.Quote(exe) + " -test.run=^TestEngineAgentChild$ -- --engine-child " + provider + " \"$@\"\n"
		if err := os.WriteFile(path, []byte(wrapper), 0700); err != nil {
			t.Fatal(err)
		}
		flags[provider+"_bin"] = config.Override{Value: path, Flag: "--" + provider + "-bin"}
	}
	flags["planner"] = config.Override{Value: planner, Flag: "--planner"}
	flags["persistent"] = config.Override{Value: persistent, Flag: "--persistent"}
	flags["closing_pass"] = config.Override{Value: false, Flag: "--closing-pass"}
	resolved, _, err := config.Resolve(config.ResolveOptions{Cwd: root, Home: filepath.Join(root, "home"), XDGRoot: filepath.Join(root, "config"), Workspace: filepath.Join(root, "ws"), Mutating: true, Flags: flags})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{Root: root, Workspace: filepath.Join(root, "ws"), Capture: filepath.Join(root, "capture"), Plan: filepath.Join(root, "plan.json")}
	f.Request = Request{Workspace: f.Workspace, Resolved: &resolved, Explicit: map[string]any{}}
	f.Settings = resolved.Settings
	file := "BRIEF.md"
	if seed {
		file = "SPEC.md"
	}
	if err = os.WriteFile(filepath.Join(f.Workspace, file), []byte("# Owned input\nBuild the owned fixture.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.Options = Options{Runner: process.UnixRunner{}, Clock: quickClock{}, Env: []string{"HOME=" + filepath.Join(root, "home"), "TMPDIR=" + filepath.Join(root, "tmp"), "PATH=" + filepath.Join(root, "tools"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_RUNTIME_DIR=" + filepath.Join(root, "runtime"), "F_ENGINE_CAPTURE=" + f.Capture, "F_ENGINE_PLAN=" + f.Plan, "GORACE=atexit_sleep_ms=0"}}
	f.setPlan(t, plan)
	if retained {
		t.Cleanup(func() { f.evidence(t) })
	}
	t.Log("owned engine evidence", root)
	return f
}

func (f *fixture) setPlan(t *testing.T, p AgentPlan) {
	t.Helper()
	b, _ := json.Marshal(p)
	if err := os.WriteFile(f.Plan, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) calls() []string {
	b, _ := os.ReadFile(filepath.Join(f.Capture, "launches.txt"))
	if strings.TrimSpace(string(b)) == "" {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}
func (f *fixture) run(t *testing.T) (map[string]any, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return Run(ctx, f.Request, f.Options)
}
func (f *fixture) manifest(t *testing.T) store.Snapshot {
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
	return m
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *contract.Error
	var stored *store.Error
	if errors.As(err, &typed) {
		if typed.Code == code {
			return
		}
	}
	if errors.As(err, &stored) {
		if stored.Code == code {
			return
		}
	}
	t.Fatalf("wanted %s, got %v", code, err)
}

var evidenceBinaryHash = sync.OnceValues(func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(exe)
	return contract.HashBytes(b), err
})

func (f *fixture) evidence(t *testing.T) {
	hashes := make(map[string]any)
	_ = filepath.WalkDir(f.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Error(err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			t.Error(e)
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			t.Error(e)
			return nil
		}
		rel, _ := filepath.Rel(f.Root, path)
		hashes[rel] = map[string]any{"sha256": contract.HashBytes(b), "bytes": len(b)}
		return nil
	})
	binaryHash, err := evidenceBinaryHash()
	if err != nil {
		t.Error(err)
		return
	}
	e := map[string]any{"case": t.Name(), "tier": "A", "fixture": "F-DIRECT/F-FILES", "target_os": runtime.GOOS, "settings": f.Settings, "owned_agent_launches": len(f.calls()), "calls": f.calls(), "artifact_hashes": hashes, "test_binary_sha256": binaryHash, "passed": !t.Failed()}
	b, err := contract.Canonical(e)
	if err != nil {
		t.Error(err)
		return
	}
	if err = os.WriteFile(filepath.Join(f.Root, "evidence.json"), b, 0600); err != nil {
		t.Error(err)
	}
}

func TestALOOP01Ordinary(t *testing.T) {
	for _, planner := range []string{"claude", "codex"} {
		for _, persistent := range []bool{false, true} {
			for _, seed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/persistent-%v/seed-%v", planner, persistent, seed), func(t *testing.T) {
					f := newFixture(t, planner, persistent, seed, AgentPlan{Critiques: []string{"Review\nVERDICT: REVISE\n", "Review\nVERDICT: APPROVE\n"}})
					data, err := f.run(t)
					if err != nil {
						t.Fatal(err)
					}
					if data["status"] != "approved" {
						t.Fatal(data)
					}
					calls := f.calls()
					want := 4
					if seed {
						want = 3
					}
					if len(calls) != want {
						t.Fatal(calls)
					}
					if seed && strings.Contains(calls[0], "draft") {
						t.Fatal("seed drafted")
					}
					for _, name := range []string{"rounds/r01.spec.md", "rounds/r02.critique.md"} {
						if _, err = os.Stat(filepath.Join(f.Workspace, name)); err != nil {
							t.Fatal(err)
						}
					}
					m := f.manifest(t)
					if m.String("spec_hash") != m.String("reviewed_spec_hash") {
						t.Fatal("unreviewed approval")
					}
					f.Request.Resolved = nil
					f.Request.Resume = true
					before := len(calls)
					if _, err = f.run(t); err != nil {
						t.Fatal(err)
					}
					if len(f.calls()) != before {
						t.Fatal("approved attachment called agent")
					}
				})
			}
		}
	}
}
