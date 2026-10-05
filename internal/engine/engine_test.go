//go:build darwin || linux

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/store"
)

type AgentPlan struct {
	Critiques       []string `json:"critiques"`
	QuestionPurpose string   `json:"question_purpose"`
	MutationPath    string   `json:"mutation_path"`
	MutationRole    string   `json:"mutation_role"`
	MutationPurpose string   `json:"mutation_purpose"`
	Fail            bool     `json:"fail"`
	Hold            bool     `json:"hold"`
}

func TestEngineAgentChild(t *testing.T) {
	start := -1
	for i, arg := range os.Args {
		if arg == "--engine-child" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return
	}
	provider := os.Args[start]
	args := os.Args[start+1:]
	if len(args) == 1 && args[0] == "--version" {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintln(os.Stdout, "owned engine stub 1.0")
		os.Exit(0)
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil || len(stdin) > 0 {
		os.Exit(91)
	}
	cwd, _ := os.Getwd()
	b, err := os.ReadFile(filepath.Join(cwd, "state/manifest.json"))
	if err != nil {
		os.Exit(92)
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		os.Exit(93)
	}
	turn := m["current_turn"].(map[string]any)
	id := turn["id"].(string)
	role := turn["role"].(string)
	purpose := turn["purpose"].(string)
	promptBytes, err := os.ReadFile(filepath.Join(cwd, turn["prompt_path"].(string)))
	if err != nil {
		os.Exit(94)
	}
	capture := map[string]any{"provider": provider, "argv": args, "stdin_bytes": len(stdin), "turn": turn, "prompt": string(promptBytes)}
	captureBytes, _ := json.Marshal(capture)
	if os.WriteFile(filepath.Join(os.Getenv("F_ENGINE_CAPTURE"), id+".json"), captureBytes, 0600) != nil {
		os.Exit(95)
	}
	counter := filepath.Join(os.Getenv("F_ENGINE_CAPTURE"), "launches.txt")
	prior, _ := os.ReadFile(counter)
	f, err := os.OpenFile(counter, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(96)
	}
	fmt.Fprintf(f, "%s %s %s\n", provider, role, purpose)
	f.Close()
	var plan AgentPlan
	b, err = os.ReadFile(os.Getenv("F_ENGINE_PLAN"))
	if err != nil || json.Unmarshal(b, &plan) != nil {
		os.Exit(97)
	}
	time.Sleep(100 * time.Millisecond)
	if plan.Hold {
		time.Sleep(15 * time.Second)
	}
	if plan.MutationRole == role && (plan.MutationPurpose == "" || plan.MutationPurpose == purpose) {
		path := plan.MutationPath
		if path == "@stdout" {
			path = "state/turns/" + id + "/stdout.pending"
		}
		forged := "forged by owned stub\n"
		if plan.MutationPath == "@stdout" {
			forged = strings.Repeat(forged, 100)
		}
		if os.WriteFile(filepath.Join(cwd, path), []byte(forged), 0600) != nil {
			os.Exit(98)
		}
	}
	if plan.Fail && (plan.MutationRole == "" || plan.MutationRole == role) && (plan.MutationPurpose == "" || plan.MutationPurpose == purpose) {
		os.Exit(23)
	}
	reply := "Owned planner reply.\n"
	if role == "planner" {
		if os.WriteFile(filepath.Join(cwd, "SPEC.md"), []byte("# SPEC\nChecked "+purpose+" result.\n"), 0600) != nil {
			os.Exit(99)
		}
		if plan.QuestionPurpose == purpose {
			_ = os.WriteFile(filepath.Join(cwd, "QUESTIONS.md"), []byte("1. Choose A or B. Recommendation: A.\n"), 0600)
		} else {
			_ = os.Remove(filepath.Join(cwd, "QUESTIONS.md"))
		}
	} else {
		index := 0
		for _, line := range strings.Split(string(prior), "\n") {
			if strings.Contains(line, " critic ") {
				index++
			}
		}
		reply = "Review\nVERDICT: APPROVE\n"
		if index < len(plan.Critiques) {
			reply = plan.Critiques[index]
		}
	}
	if provider == "codex" {
		session := "11111111-2222-4333-8444-555555555555"
		if role == "critic" {
			session = "99999999-2222-4333-8444-555555555555"
		}
		if len(args) > 2 && args[1] == "resume" {
			session = args[2]
		}
		fmt.Fprintf(os.Stdout, "{\"type\":\"thread.started\",\"thread_id\":%s}\n", strconv.Quote(session))
		for i, arg := range args {
			if arg == "--output-last-message" && i+1 < len(args) {
				_ = os.WriteFile(args[i+1], []byte(reply), 0600)
			}
		}
		fmt.Fprintln(os.Stdout, "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
	} else {
		fmt.Fprint(os.Stdout, reply)
	}
	os.Exit(0)
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
	if root == "" {
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
	t.Cleanup(func() { f.evidence(t) })
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
func (f *fixture) evidence(t *testing.T) {
	hashes := make(map[string]any)
	_ = filepath.WalkDir(f.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil || !info.Mode().IsRegular() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return nil
		}
		rel, _ := filepath.Rel(f.Root, path)
		hashes[rel] = map[string]any{"sha256": contract.HashBytes(b), "bytes": len(b)}
		return nil
	})
	exe, _ := os.Executable()
	binary, _ := os.ReadFile(exe)
	e := map[string]any{"case": t.Name(), "tier": "A", "fixture": "F-DIRECT/F-FILES", "target_os": runtime.GOOS, "settings": f.Settings, "owned_agent_launches": len(f.calls()), "calls": f.calls(), "artifact_hashes": hashes, "test_binary_sha256": contract.HashBytes(binary), "passed": !t.Failed()}
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
