//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/engine"
	"github.com/TheEditor/volley/internal/ops"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/store"
	"github.com/TheEditor/volley/tests/ownedagent"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCLIProviderChild(t *testing.T) {
	if exit := ownedagent.Run(); exit >= 0 {
		os.Exit(exit)
	}
}

type contractFixture struct {
	root, ws, config, plan, capture string
	opts                            Options
	results                         []map[string]any
}

func newContractFixture(t *testing.T, seed bool) *contractFixture {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	var space unix.Statfs_t
	if err := unix.Statfs(root, &space); err != nil {
		t.Fatal(err)
	}
	if uint64(space.Bavail)*uint64(space.Bsize) < 2<<30 {
		t.Fatal("owned fixture requires 2 GiB free space")
	}
	for _, n := range []string{"home", "tmp", "config", "state", "data", "cache", "runtime", "tools", "capture", "ws"} {
		os.Mkdir(filepath.Join(root, n), 0700)
	}
	f := &contractFixture{root: root, ws: filepath.Join(root, "ws"), plan: filepath.Join(root, "plan.json"), capture: filepath.Join(root, "capture"), config: filepath.Join(root, "settings.toml"), opts: fixed()}
	exe, _ := os.Executable()
	for _, p := range []string{"claude", "codex"} {
		path := filepath.Join(root, "tools", p)
		script := "#!/bin/sh\nexec " + ops.Command(exe) + " -test.run=^TestCLIProviderChild$ -- --engine-child " + p + " \"$@\"\n"
		os.WriteFile(path, []byte(script), 0700)
	}
	cfg := fmt.Sprintf("closing_pass = false\nclaude_bin = %s\ncodex_bin = %s\n", strconv.Quote(filepath.Join(root, "tools/claude")), strconv.Quote(filepath.Join(root, "tools/codex")))
	os.WriteFile(f.config, []byte(cfg), 0600)
	env := []string{"HOME=" + filepath.Join(root, "home"), "TMPDIR=" + filepath.Join(root, "tmp"), "PATH=" + filepath.Join(root, "tools"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_RUNTIME_DIR=" + filepath.Join(root, "runtime"), "F_ENGINE_CAPTURE=" + f.capture, "F_ENGINE_PLAN=" + f.plan, "TERM=dumb", "NO_COLOR=1"}
	f.opts.RunOptions = &engine.Options{Env: env, Runner: process.UnixRunner{}}
	file := "BRIEF.md"
	if seed {
		file = "SPEC.md"
	}
	os.WriteFile(filepath.Join(f.ws, file), []byte("# Owned contract input\n"), 0600)
	f.setPlan(t, ownedagent.Plan{})
	t.Cleanup(func() {
		hashes := map[string]string{}
		var totalBytes int64
		filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() && d.Type().IsRegular() {
				b, e := os.ReadFile(p)
				if e == nil {
					rel, _ := filepath.Rel(root, p)
					hashes[rel] = contract.HashBytes(b)
					totalBytes += int64(len(b))
				}
			}
			return nil
		})
		if totalBytes > 512<<20 {
			t.Error("owned fixture exceeded 512 MiB limit")
		}
		settings, _ := os.ReadFile(f.config)
		b, _ := json.Marshal(map[string]any{"target_os": runtime.GOOS, "test_binary_sha256": contractTestBinaryHash(), "settings": string(settings), "case": t.Name(), "tier": "A", "fixture": "F-DIRECT/F-FILES", "responses": f.results, "artifact_hashes": hashes})
		t.Log("REGISTRY_EVIDENCE", string(b))
	})
	return f
}
func (f *contractFixture) setPlan(t *testing.T, p ownedagent.Plan) {
	t.Helper()
	b, _ := json.Marshal(p)
	if err := os.WriteFile(f.plan, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *contractFixture) call(t *testing.T, id, code string, args ...string) contract.Result {
	t.Helper()
	o := f.opts
	o.RequestID = func() (string, error) { return id, nil }
	var out, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	exit := Execute(ctx, append([]string{"--json"}, args...), &out, &stderr, o)
	return f.observe(t, id, code, args, exit, out.Bytes(), stderr.Bytes())
}
func (f *contractFixture) observe(t *testing.T, id, code string, args []string, exit int, stdout, stderr []byte) contract.Result {
	t.Helper()
	var r contract.Result
	if err := json.Unmarshal(stdout, &r); err != nil {
		t.Fatal(id, err, string(stdout), string(stderr))
	}
	if err := contract.Validate("envelope.json", r); err != nil {
		t.Fatal(id, err, string(stdout))
	}
	want := 0
	if code != "" {
		registry, _ := contract.Load()
		want = registry.Error(code, "").Exit
	}
	got := ""
	if len(r.Errors) > 0 {
		got = r.Errors[0].Code
		if !strings.Contains(string(stderr), r.Errors[0].Message) {
			t.Fatal("stderr", id)
		}
	}
	if exit != want || got != code || r.OK != (code == "") {
		t.Fatal(id, "exit", exit, "code", got, "wanted", code, string(stdout))
	}
	f.results = append(f.results, map[string]any{"id": id, "argv": args, "exit": exit, "response": r, "stdout_sha256": contract.HashBytes(stdout), "stderr_sha256": contract.HashBytes(stderr)})
	return r
}
func (f *contractFixture) run(t *testing.T, id, code string, flags ...string) contract.Result {
	args := []string{"run", f.ws, "--config", f.config}
	return f.call(t, id, code, append(args, flags...)...)
}
func (f *contractFixture) launches() int {
	b, _ := os.ReadFile(filepath.Join(f.capture, "launches.txt"))
	return len(strings.Fields(string(b))) / 3
}
func TestContractRegistryCLI(t *testing.T) {
	f := newContractFixture(t, true)
	for _, p := range []struct {
		id, code string
		args     []string
	}{
		{"A-CONTRACT-R01", "INVALID_INPUT", []string{"--json=false"}}, {"A-CONTRACT-R02", "UNKNOWN_FLAG", []string{"plan", "--unknown"}}, {"A-CONTRACT-R03", "UNKNOWN_COMMAND", []string{"runs", "unknown"}}, {"A-CONTRACT-R04", "MISSING_REQUIRED", []string{"run"}}, {"A-CONTRACT-R05", "INVALID_CONFIG", []string{"config", "show", "--config", filepath.Join(f.root, "absent.toml")}}, {"A-CONTRACT-R06", "CONFIG_READ_FAILED", []string{"config", "show", "--config", f.root}}, {"A-CONTRACT-R10", "ACK_REQUIRED", []string{"runs", "stop", f.ws}}, {"A-CONTRACT-R13", "NOT_FOUND", []string{"runs", "get", filepath.Join(f.root, "absent")}}, {"A-CONTRACT-R37", "UNKNOWN_DELIVERY_SCHEME", []string{"plan", f.ws, "--deliver=webhook:bad"}},
	} {
		f.call(t, p.id, p.code, p.args...)
	}
	t.Run("missing-executable", func(t *testing.T) {
		bad := newContractFixture(t, true)
		bad.call(t, "A-CONTRACT-R07", "DEPENDENCY_MISSING", "run", bad.ws, "--config", bad.config, "--codex-bin", filepath.Join(bad.root, "absent-tool"))
	})
	if fullCI() {
		f.opts.RunOptions.Env = append(f.opts.RunOptions.Env, "VOLLEY_TEST_FAULT=platform")
		f.run(t, "A-CONTRACT-R08", "UNSUPPORTED_PLATFORM")
		f.opts.RunOptions.Env = f.opts.RunOptions.Env[:len(f.opts.RunOptions.Env)-1]
	}
	f.run(t, "A-CONTRACT-R38", "")
	f.call(t, "A-CONTRACT-R14", "CONFIG_CONFLICT", "run", f.ws, "--planner=codex")
	target := filepath.Join(f.root, "out")
	os.WriteFile(target, []byte("preserve\n"), 0600)
	f.call(t, "A-CONTRACT-R17", "OUTPUT_CONFLICT", "status", f.ws, "--deliver=file:"+target)
	os.WriteFile(filepath.Join(f.ws, "SPEC.md"), []byte("external change\n"), 0600)
	f.call(t, "A-CONTRACT-R18", "ARTIFACT_CHANGED", "runs", "resume", f.ws)
	if fullCI() {
		for _, site := range []string{"execution", "conformance"} {
			f.opts.RunOptions.Env = append(f.opts.RunOptions.Env, "VOLLEY_TEST_FAULT="+site)
			if site == "execution" {
				f.call(t, "A-CONTRACT-R35", "INTERNAL", "--help")
			} else {
				f.call(t, "A-CONTRACT-R36", "CONFORMANCE_FAILED", "conformance")
			}
			f.opts.RunOptions.Env = f.opts.RunOptions.Env[:len(f.opts.RunOptions.Env)-1]
		}
	}
}
func TestContractRegistryTurnResults(t *testing.T) {
	for _, v := range []struct {
		id, code string
		seed     bool
		plan     ownedagent.Plan
		flags    []string
	}{
		{"A-CONTRACT-R23", "REVIEW_IMPASSE", true, ownedagent.Plan{Critiques: []string{"VERDICT: REVISE\n"}}, []string{"--max-rounds=1"}},
		{"A-CONTRACT-R24", "ANSWER_REQUIRED", false, ownedagent.Plan{QuestionPurpose: "draft"}, nil},
		{"A-CONTRACT-R30", "UPSTREAM_FAILURE", true, ownedagent.Plan{MalformedCodex: true}, nil},
		{"A-CONTRACT-R31", "TURN_FAILED", true, ownedagent.Plan{Fail: true}, nil},
		{"A-CONTRACT-R32", "TURN_TIMEOUT", true, ownedagent.Plan{Hold: true}, []string{"--wait-timeout=200ms"}},
		{"A-CONTRACT-R33", "ARTIFACT_MISSING", false, ownedagent.Plan{MissingSpec: true}, nil},
		{"A-CONTRACT-R34", "CRITIC_MUTATION", true, ownedagent.Plan{MutationRole: "critic", MutationPath: "SPEC.md"}, nil},
		{"A-CONTRACT-R41", "PLANNER_MUTATION", false, ownedagent.Plan{MutationRole: "planner", MutationPath: "volley.config.toml"}, nil},
	} {
		t.Run(v.id, func(t *testing.T) {
			f := newContractFixture(t, v.seed)
			f.setPlan(t, v.plan)
			f.run(t, v.id, v.code, v.flags...)
			wantCalls := 1
			if v.id == "A-CONTRACT-R23" {
				wantCalls = 2
			}
			if f.launches() != wantCalls {
				t.Fatal("unexpected turn count", f.launches())
			}
			if v.id == "A-CONTRACT-R24" {
				f.opts.Input = strings.NewReader(`{"text":"exact answer\n"}`)
				f.call(t, "A-CONTRACT-R25", "ANSWER_CONFLICT", "human", "answer", f.ws, "--question-id="+strings.Repeat("a", 26), "--from-stdin")
			}
		})
	}
}
func TestContractRegistryState(t *testing.T) {
	for _, v := range []struct{ id, code, manifest string }{
		{"A-CONTRACT-R16", "STATE_VERSION_UNSUPPORTED", `{"record_version":99}`}, {"A-CONTRACT-R43", "STATE_INVALID", `{"record_version":1}`},
	} {
		t.Run(v.id, func(t *testing.T) {
			f := newContractFixture(t, true)
			os.Mkdir(filepath.Join(f.ws, "state"), 0700)
			os.WriteFile(filepath.Join(f.ws, "state/manifest.json"), []byte(v.manifest), 0600)
			f.run(t, v.id, v.code)
			if f.launches() != 0 {
				t.Fatal("invalid state started turn")
			}
		})
	}
	f := newContractFixture(t, true)
	os.Mkdir(filepath.Join(f.ws, "state"), 0700)
	os.WriteFile(filepath.Join(f.ws, "state/run"), []byte("old run"), 0600)
	f.run(t, "A-CONTRACT-R29", "LEGACY_BOUNDARY_UNVERIFIED")
}
func TestContractRegistryUnfinished(t *testing.T) {
	f := newContractFixture(t, true)
	f.opts.RunOptions.Hook = func(name string, s *store.Store) error {
		if name == "engine.intent.committed" {
			return fmt.Errorf("owned interruption")
		}
		return nil
	}
	f.run(t, "intent-setup", "INTERNAL", "--idempotency-key=bound")
	f.opts.RunOptions.Hook = nil
	f.call(t, "A-CONTRACT-R15", "IDEMPOTENCY_CONFLICT", "run", f.ws, "--idempotency-key=bound", "--planner=codex")
	f.call(t, "A-CONTRACT-R27", "TURN_UNCERTAIN", "runs", "resume", f.ws)
	f.opts.RunOptions.Env = append(f.opts.RunOptions.Env, "CLAUDE_CONFIG_DIR="+filepath.Join(f.root, "home"))
	f.call(t, "A-CONTRACT-R20", "IDENTITY_CONFLICT", "runs", "resume", f.ws)
	if f.launches() != 0 {
		t.Fatal("uncertain intent delivered")
	}
}
func TestContractRegistryBillingContextAndLocks(t *testing.T) {
	t.Run("billing", func(t *testing.T) {
		f := newContractFixture(t, true)
		f.opts.RunOptions.Env = append(f.opts.RunOptions.Env, "OPENAI_API_KEY=owned-marker")
		f.run(t, "A-CONTRACT-R09", "BILLING_REFUSED")
		if f.launches() != 0 {
			t.Fatal("billing refusal started turn")
		}
	})
	t.Run("context", func(t *testing.T) {
		f := newContractFixture(t, true)
		path := filepath.Join(f.root, "context")
		os.Mkdir(path, 0700)
		f.opts.RunOptions.Hook = func(name string, s *store.Store) error {
			if name == "engine.intent.committed" {
				return fmt.Errorf("owned interruption")
			}
			return nil
		}
		f.run(t, "context-setup", "INTERNAL", "--context-dir", path)
		f.opts.RunOptions.Hook = nil
		os.Rename(path, path+".old")
		os.Mkdir(path, 0700)
		f.call(t, "A-CONTRACT-R19", "CONTEXT_CHANGED", "runs", "resume", f.ws)
		if f.launches() != 0 {
			t.Fatal("changed context started turn")
		}
	})
	t.Run("lock-and-wait", func(t *testing.T) {
		f := newContractFixture(t, true)
		f.opts.RunOptions.Hook = func(name string, s *store.Store) error {
			if name == "engine.intent.committed" {
				return fmt.Errorf("owned interruption")
			}
			return nil
		}
		f.run(t, "lock-setup", "INTERNAL")
		f.opts.RunOptions.Hook = nil
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "-test.run=^TestCLIControllerChild$", "--", "owned-lock", f.ws)
		cmd.Env = f.opts.RunOptions.Env
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var childOut bytes.Buffer
		cmd.Stdout = &childOut
		cmd.Stderr = &childOut
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { input.Close(); cmd.Wait() })
		waitOwnedFile(t, filepath.Join(f.ws, "state/lock-ready"))
		f.run(t, "A-CONTRACT-R11", "LOCKED")
		f.call(t, "A-CONTRACT-R12", "WAIT_TIMEOUT", "runs", "get", f.ws, "--wait", "--wait-timeout=20ms")
		if f.launches() != 0 {
			t.Fatal("inspection started turn")
		}
	})
}
func TestContractWarnings(t *testing.T) {
	f := newContractFixture(t, false)
	f.opts.RunOptions.Env = append(f.opts.RunOptions.Env, "VOLLEY_PLANNER=codex")
	r := f.call(t, "warning-retired", "", "doctor")
	found := false
	for _, w := range r.Warnings {
		if w.Code == "ENV_SETTING_IGNORED" {
			found = true
		}
	}
	if !found {
		t.Fatal("ignored variable warning absent")
	}
	r = f.call(t, "warning-inactive", "", "config", "show", "--gashki-bin=/inactive")
	found = false
	for _, w := range r.Warnings {
		if w.Code == "INACTIVE_SETTING" {
			found = true
		}
	}
	if !found {
		t.Fatal("inactive setting warning absent")
	}
	f.setPlan(t, ownedagent.Plan{BlankPlannerReply: true})
	f.opts.RunOptions.Register = func(ctx context.Context, m store.Snapshot) error {
		return os.Mkdir(filepath.Join(f.config, "not-a-directory"), 0700)
	}
	r = f.run(t, "warning-turn-results", "")
	codes := map[string]bool{}
	for _, w := range r.Warnings {
		codes[w.Code] = true
		if len(w.Evidence) == 0 {
			t.Fatal("warning evidence absent", w)
		}
	}
	for _, code := range []string{"INDEX_UNAVAILABLE", "MODEL_UNRECORDED", "REPLY_UNAVAILABLE"} {
		if !codes[code] {
			t.Fatal("warning absent", code, r.Warnings)
		}
	}
	status := f.call(t, "warning-saved-status", "", "status", f.ws)
	saved := map[string]bool{}
	for _, w := range status.Warnings {
		saved[w.Code] = true
	}
	if !saved["MODEL_UNRECORDED"] || !saved["REPLY_UNAVAILABLE"] {
		t.Fatal("saved warnings absent", status.Warnings)
	}
	if f.launches() != 2 {
		t.Fatal("warning changed successful loop", f.launches())
	}
}

var binaryHashOnce sync.Once
var testBinaryHash string

func contractTestBinaryHash() string {
	binaryHashOnce.Do(func() { path, _ := os.Executable(); b, _ := os.ReadFile(path); testBinaryHash = contract.HashBytes(b) })
	return testBinaryHash
}
