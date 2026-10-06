package gashkireal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/agent"
	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/gashki"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

type adapterFixture struct {
	*fixture
	store            *store.Store
	client           *gashki.Client
	manager          *gashki.PaneManager
	run              string
	binary           string
	gates, mutations int
}

func adapterCode(e error) string {
	var x *contract.Error
	if errors.As(e, &x) {
		return x.Code
	}
	return ""
}
func newAdapterFixture(t *testing.T, binary, placement string) *adapterFixture {
	f := newFixture(t)
	a := &adapterFixture{fixture: f, binary: binary}
	if placement == "default" {
		put(t, f.config, []byte(fmt.Sprintf("state_dir = %q\nready_timeout = \"15s\"\nwait_timeout = \"15s\"\n", filepath.Join(f.root, "state", "gashki"))))
		f.env = append(f.env, "TMUX="+f.socketPath+",1,0", "TMUX_PANE="+f.caller)
	}
	var e error
	a.run, e = store.NewID()
	if e != nil {
		t.Fatal(e)
	}
	a.store, e = store.Open(filepath.Join(f.root, "ws"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.store.Close() })
	if e = a.store.Prepare(); e != nil {
		t.Fatal(e)
	}
	if e = a.store.AcquireOwner(context.Background(), 0); e != nil {
		t.Fatal(e)
	}
	if _, e = a.store.StagePrivateText("gashki.config.toml", read(t, f.config)); e != nil {
		t.Fatal(e)
	}
	if placement == "here" {
		f.env = append(f.env, "TMUX="+f.socketPath+",1,0", "TMUX_PANE="+f.caller)
	}
	a.makeClient()
	return a
}
func (a *adapterFixture) makeClient() {
	t := a.t
	manifest := object(t, read(t, filepath.Join(a.dir, "fixture.json")))
	binary := filepath.Join(a.dir, a.binary)
	gate := func(ctx context.Context, kind string) error {
		a.gates++
		b, e := a.store.ReadText("gashki.config.toml")
		if e != nil || contract.HashBytes(b) != contract.HashBytes(read(t, a.config)) {
			return fmt.Errorf("frozen config changed")
		}
		return nil
	}
	var e error
	a.client, e = gashki.NewClient(gashki.ClientOptions{RunID: a.run, Store: a.store, Runner: process.UnixRunner{}, Binary: binary, Config: filepath.Join(a.store.Path, "gashki.config.toml"), Env: a.env, Gate: gate, Register: func(paths []string) error {
		for _, p := range paths {
			if filepath.IsAbs(p) || strings.HasPrefix(p, "../") {
				return fmt.Errorf("unowned output")
			}
		}
		return nil
	}, BeforeMutation: func(ctx context.Context, intent gashki.CallIntent) error {
		a.mutations++
		b, e := a.store.ReadText("state/control/gk-call-" + intent.ID + "-intent.json")
		if e != nil || len(b) == 0 {
			return fmt.Errorf("intent absent")
		}
		return nil
	}, SourceProof: &gashki.SourceProof{SourceCommit: pin, ArchiveHash: text(manifest, "source_archive_sha256"), BinaryPath: binary}})
	if e != nil {
		t.Fatal(e)
	}
	if e = a.client.Preflight(context.Background()); e != nil {
		t.Fatal(e)
	}
	server, e := gashki.CanonicalSocket(a.socketPath)
	if e != nil {
		t.Fatal(e)
	}
	a.manager = &gashki.PaneManager{Client: a.client, ExpectedServer: server, Observer: gashki.TmuxObserver{Path: a.tmux, Runner: process.UnixRunner{}, Gate: gate}}
}
func (a *adapterFixture) prepare(provider, role, placement string) gashki.SpawnIntent {
	a.t.Helper()
	a.proveProvider(provider)
	put(a.t, filepath.Join(a.root, "stub", provider+".mode"), []byte("idle"))
	settings := config.Settings{ClaudeModel: "fixture-model", ClaudeEffort: "high", CodexModel: "fixture-model", CodexEffort: "high", ClaudePlannerTools: []string{"Read", "Glob", "Grep", "Skill", "Edit", "Write"}, ClaudeCriticTools: []string{"Read", "Glob", "Grep", "Skill"}}
	args, _, e := agent.BuildGashkiArguments(agent.ArgumentOptions{Request: review.TurnRequest{Role: role, Provider: provider, Workspace: a.store.Path}, Settings: settings, InheritedChecked: true})
	if e != nil {
		a.t.Fatal(e)
	}
	plan, e := a.manager.PrepareSpawn(context.Background(), gashki.SpawnRequest{RunID: a.run, Role: role, Provider: provider, Cwd: a.store.Path, Placement: placement, AgentArgs: args}, gashki.Budget{})
	if e != nil {
		var native *contract.Error
		if errors.As(e, &native) {
			a.t.Log(native.Evidence)
		}
		a.t.Fatal(e)
	}
	if len(strings.Split(plan.Selector, "/")[0]) != 30 {
		a.t.Fatal("group size")
	}
	return plan
}
func (a *adapterFixture) pane(provider, role, placement string) gashki.PaneBinding {
	a.t.Helper()
	plan := a.prepare(provider, role, placement)
	p, e := a.manager.Spawn(context.Background(), plan, nil, gashki.Budget{})
	if e != nil {
		var native *contract.Error
		if errors.As(e, &native) {
			a.t.Log(native.Evidence)
		}
		a.t.Fatal(e)
	}
	return p
}
func (a *adapterFixture) sendPlan(pane gashki.PaneBinding) gashki.SendIntent {
	a.t.Helper()
	id, e := store.NewID()
	if e != nil {
		a.t.Fatal(e)
	}
	p, e := a.manager.PrepareSend(context.Background(), id, pane, []byte("Owned adapter prompt\n"), gashki.Budget{})
	if e != nil {
		a.t.Fatal(e)
	}
	return p
}
func (a *adapterFixture) independentCounts(provider string, processes, pastes, submits, stops int) {
	a.t.Helper()
	dir := filepath.Join(a.root, "stub")
	argv := string(read(a.t, filepath.Join(dir, provider+".argv")))
	keys, _ := os.ReadFile(filepath.Join(dir, provider+".keys"))
	hookBytes, e := os.ReadFile(filepath.Join(dir, provider+".hooks"))
	if e != nil && (!os.IsNotExist(e) || submits != 0 || stops != 0) {
		a.t.Fatal(e)
	}
	hooks := string(hookBytes)
	if strings.Count(argv, "--model\n") != processes || strings.Count(string(keys), "<Paste>\n") != pastes || strings.Count(hooks, "UserPromptSubmit\n") != submits || strings.Count(hooks, "Stop\n") != stops {
		a.t.Fatal("independent counts", argv, string(keys), hooks)
	}
	put(a.t, filepath.Join(a.root, "adapter-counts.json"), jsonBytes(map[string]any{"provider": provider, "processes": processes, "pastes": pastes, "submits": submits, "stops": stops, "argv_hash": contract.HashBytes([]byte(argv)), "keys_hash": contract.HashBytes(keys), "hooks_hash": contract.HashBytes([]byte(hooks)), "gates": a.gates, "mutations": a.mutations}))
}

func TestAdapterRealDeliveryAndWait(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, placement := range []string{"normal", "here"} {
			t.Run(provider+"/"+placement, func(t *testing.T) {
				a := newAdapterFixture(t, "gashki-release", placement)
				callerBefore := a.tm("display-message", "-p", "-t", a.caller, "#{pane_pid}|#{pane_current_command}|#{window_id}|#{session_id}")
				pane := a.pane(provider, "planner", placement)
				plan := a.sendPlan(pane)
				delivery, e := a.manager.Deliver(context.Background(), plan, gashki.Budget{})
				if e != nil || delivery.Kind != review.WithCursor {
					t.Fatal(delivery, e)
				}
				// Resume retains the original confirmation and does not send or spawn.
				again, e := a.manager.Deliver(context.Background(), plan, gashki.Budget{})
				if e != nil || again.Cursor != delivery.Cursor {
					t.Fatal(again, e)
				}
				result, e := a.manager.AwaitCompletion(context.Background(), plan, again, gashki.Budget{Limit: 10 * time.Second, Started: time.Now()})
				if e != nil || result.Kind != review.Completed {
					t.Fatal(result, e)
				}
				a.independentCounts(provider, 1, 1, 1, 1)
				approved := []byte("# Synthetic approved snapshot\nKeep these bytes.\n")
				if _, e = a.store.StagePrivateText("SPEC.md", approved); e != nil {
					t.Fatal(e)
				}
				cleanup, e := a.manager.Cleanup(context.Background(), []gashki.PaneBinding{pane}, true, contract.HashBytes(approved), gashki.Budget{})
				if e != nil || cleanup.Pending || len(cleanup.Removed) != 1 {
					t.Fatal(cleanup, e)
				}
				saved, e := a.store.ReadText("SPEC.md")
				if e != nil || string(saved) != string(approved) {
					t.Fatal("approved snapshot changed")
				}
				if a.tm("display-message", "-p", "-t", a.caller, "#{pane_pid}|#{pane_current_command}|#{window_id}|#{session_id}") != callerBefore {
					t.Fatal("caller changed")
				}
				t.Log("A-GK-04/05/10/12 preliminary tier B adapter: saved confirmation, qualified wait, owned cleanup, independent one-paste count")
			})
		}
	}
}

type adapterCrashInput struct {
	Dir, Root, Socket, SocketPath, Caller, Config, Tmux, Run, Binary, Stage string
	Env                                                                     []string
	Spawn                                                                   gashki.SpawnIntent
	Send                                                                    gashki.SendIntent
	CallID                                                                  string
}

// This child is an owned controller. It receives only explicit fixture paths
// and roots, performs one committed primitive, and dies by actual SIGKILL.
func TestAdapterCrashChild(t *testing.T) {
	path := option("--adapter-crash")
	if path == "" {
		return
	}
	var in adapterCrashInput
	if e := json.Unmarshal(read(t, path), &in); e != nil {
		t.Fatal(e)
	}
	a := &adapterFixture{fixture: &fixture{t: t, dir: in.Dir, root: in.Root, socket: in.Socket, socketPath: in.SocketPath, caller: in.Caller, config: in.Config, tmux: in.Tmux, env: in.Env}, run: in.Run, binary: in.Binary}
	var e error
	a.store, e = store.Open(filepath.Join(in.Root, "ws"))
	if e != nil {
		t.Fatal(e)
	}
	if e = a.store.AcquireOwner(context.Background(), 0); e != nil {
		t.Fatal(e)
	}
	a.makeClient()
	switch in.Stage {
	case "before-send":
	case "after-send", "fault-send":
		call, e := a.client.Call(context.Background(), gashki.CallRequest{ID: in.CallID, Verb: "send", Args: []string{"send", in.Send.Pane.UUID, "--from-stdin", "--idempotency-key=" + in.Send.Key}, Input: []byte(in.Send.Payload), Budget: gashki.Budget{Limit: 20 * time.Second, Started: time.Now()}})
		if e != nil {
			t.Fatal(e)
		}
		if in.Stage == "after-send" && (call.ValidationError != "" || !call.Response.OK) {
			t.Fatal(call)
		}
		if in.Stage == "fault-send" && (call.Process.Signal != "killed" || !call.Process.Settled || !call.Missing) {
			t.Fatal(call)
		}
	case "after-spawn":
		plan := in.Spawn
		args := []string{"spawn", plan.Selector, "--agent=" + plan.Provider, "--cwd=" + plan.Cwd, "--agent-args=" + string(jsonBytes(plan.AgentArgs))}
		if plan.Placement == "here" {
			args = append(args, "--here")
		}
		call, e := a.client.Call(context.Background(), gashki.CallRequest{ID: plan.CallID, Verb: "spawn", Args: args, Budget: gashki.Budget{Limit: 20 * time.Second, Started: time.Now()}})
		if e != nil || call.ValidationError != "" || !call.Response.OK {
			t.Fatal(call, e)
		}
	case "after-confirmation", "after-confirmation-stopped":
		delivery, e := a.manager.Deliver(context.Background(), in.Send, gashki.Budget{})
		if e != nil || delivery.Kind != review.WithCursor {
			t.Fatal(delivery, e)
		}
	default:
		t.Fatal("unknown owned crash stage")
	}
	if e := syscall.Kill(os.Getpid(), syscall.SIGKILL); e != nil {
		t.Fatal(e)
	}
	select {}
}

func (a *adapterFixture) crash(stage string, spawn gashki.SpawnIntent, send gashki.SendIntent, callID, fault string) {
	a.t.Helper()
	env := append([]string{}, a.env...)
	if fault != "" {
		env = append(env, "GASHKI_TEST_FAULT="+fault)
	}
	in := adapterCrashInput{Dir: a.dir, Root: a.root, Socket: a.socket, SocketPath: a.socketPath, Caller: a.caller, Config: a.config, Tmux: a.tmux, Run: a.run, Binary: a.binary, Stage: stage, Env: env, Spawn: spawn, Send: send, CallID: callID}
	path := filepath.Join(a.root, "controller-crash.json")
	put(a.t, path, jsonBytes(in))
	if e := a.store.Close(); e != nil {
		a.t.Fatal(e)
	}
	exe, e := os.Executable()
	if e != nil {
		a.t.Fatal(e)
	}
	cmd := exec.Command(exe, "-test.run=^TestAdapterCrashChild$", "-test.timeout=45s", "--", "--adapter-crash", path)
	cmd.Env = []string{"HOME=" + filepath.Join(a.root, "home"), "PATH=" + filepath.Join(a.root, "tools"), "TMPDIR=" + filepath.Join(a.root, "tmp"), "GORACE=atexit_sleep_ms=0"}
	cmd.Dir = filepath.Join(a.root, "ws")
	b, e := cmd.CombinedOutput()
	put(a.t, filepath.Join(a.root, "controller-crash.stdout"), b)
	var exited *exec.ExitError
	if !errors.As(e, &exited) {
		a.t.Fatal("controller did not die", e, string(b))
	}
	status, ok := exited.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		a.t.Fatal("controller crash was not SIGKILL", e, string(b))
	}
	put(a.t, filepath.Join(a.root, "controller-crash-result.json"), jsonBytes(map[string]any{"stage": stage, "signal": "SIGKILL", "pid": cmd.Process.Pid, "stdout_hash": contract.HashBytes(b)}))
	a.store, e = store.Open(filepath.Join(a.root, "ws"))
	if e != nil {
		a.t.Fatal(e)
	}
	if e = a.store.AcquireOwner(context.Background(), 0); e != nil {
		a.t.Fatal(e)
	}
	// The trigger is absent from the parent's saved environment before recovery.
	a.makeClient()
}
func (a *adapterFixture) seedSendAttempt(plan gashki.SendIntent) string {
	a.t.Helper()
	id, e := store.NewID()
	if e != nil {
		a.t.Fatal(e)
	}
	b, e := contract.Canonical(plan)
	if e != nil {
		a.t.Fatal(e)
	}
	attempt := map[string]any{"call_id": id, "plan_hash": contract.HashBytes(b), "previous_hash": "", "reason": "initial"}
	b, e = contract.Canonical(attempt)
	if e != nil {
		a.t.Fatal(e)
	}
	if _, e = a.store.StagePrivateText("state/turns/"+plan.TurnID+"/gk-send-attempt-0001.json", b); e != nil {
		a.t.Fatal(e)
	}
	return id
}

func TestAdapterActualControllerCrashes(t *testing.T) {
	for _, stage := range []string{"before-send", "after-send", "after-spawn", "after-confirmation", "after-confirmation-stopped"} {
		for _, provider := range []string{"claude", "codex"} {
			t.Run(stage+"/"+provider, func(t *testing.T) {
				a := newAdapterFixture(t, "gashki-release", "normal")
				if stage == "after-spawn" {
					plan := a.prepare(provider, "planner", "normal")
					a.crash(stage, plan, gashki.SendIntent{}, "", "")
					pane, e := a.manager.Spawn(context.Background(), plan, nil, gashki.Budget{})
					if e != nil || pane.ReadyCursor == "" {
						t.Fatal(pane, e)
					}
					a.independentCounts(provider, 1, 0, 0, 0)
					return
				}
				pane := a.pane(provider, "planner", "normal")
				plan := a.sendPlan(pane)
				id := ""
				if !strings.HasPrefix(stage, "after-confirmation") {
					id = a.seedSendAttempt(plan)
				}
				if stage == "after-confirmation" {
					put(t, filepath.Join(a.root, "stub", provider+".latency"), []byte("10"))
				}
				a.crash(stage, gashki.SpawnIntent{}, plan, id, "")
				delivery, e := a.manager.Deliver(context.Background(), plan, gashki.Budget{})
				if e != nil || delivery.Kind != review.WithCursor {
					t.Fatal(delivery, e)
				}
				if stage == "after-send" && !delivery.Replayed {
					t.Fatal("lost receipt did not replay")
				}
				if strings.HasPrefix(stage, "after-confirmation") {
					observation, e := a.client.FreshCall(context.Background(), "observe", nil, gashki.Budget{}, "observe", pane.UUID)
					want := "working"
					if stage == "after-confirmation-stopped" {
						want = "idle"
					}
					if e != nil || observation.ValidationError != "" || !observation.Response.OK || gashki.Map(observation.Response.Data)["state"] != want {
						t.Fatal("resume stimulus state was not proved", want, observation, e)
					}
				}
				result, e := a.manager.AwaitCompletion(context.Background(), plan, delivery, gashki.Budget{Limit: 15 * time.Second, Started: time.Now()})
				if e != nil || result.Kind != review.Completed {
					t.Fatal(result, e)
				}
				a.independentCounts(provider, 1, 1, 1, 1)
				t.Log("A-GK-05/10 tier B actual controller SIGKILL; saved bindings recovered; one paste")
			})
		}
	}
}

func TestAdapterNamedSendFaults(t *testing.T) {
	for _, site := range []string{"send-barrier", "send-paste", "send-enter"} {
		for _, provider := range []string{"claude", "codex"} {
			t.Run(site+"/"+provider, func(t *testing.T) {
				a := newAdapterFixture(t, "gashki-fault", "normal")
				pane := a.pane(provider, "planner", "normal")
				plan := a.sendPlan(pane)
				id := a.seedSendAttempt(plan)
				a.crash("fault-send", gashki.SpawnIntent{}, plan, id, site+":kill")
				keys, _ := os.ReadFile(filepath.Join(a.root, "stub", provider+".keys"))
				initial := strings.Count(string(keys), "<Paste>\n")
				want := 1
				if site == "send-barrier" {
					want = 0
				}
				if initial != want {
					t.Fatal("initial paste", initial, want)
				}
				delivery, e := a.manager.Deliver(context.Background(), plan, gashki.Budget{})
				if adapterCode(e) != "SEND_UNCERTAIN" || delivery.Kind == review.WithCursor {
					t.Fatal(delivery, e)
				}
				keys, _ = os.ReadFile(filepath.Join(a.root, "stub", provider+".keys"))
				if strings.Count(string(keys), "<Paste>\n") != initial {
					t.Fatal("recovery pasted again")
				}
				var native *contract.Error
				if !errors.As(e, &native) {
					t.Fatal(e)
				}
				put(t, filepath.Join(a.root, "native-fault-result.json"), jsonBytes(native))
				files, e := filepath.Glob(filepath.Join(a.store.Path, "state", "control", "*-stdout.raw"))
				if e != nil {
					t.Fatal(e)
				}
				found := false
				for _, file := range files {
					b := read(t, file)
					if bytes.Contains(b, []byte(`"code":"SEND_INTERRUPTED"`)) && bytes.Contains(b, []byte(`"barrier_cursor"`)) {
						found = true
					}
				}
				if !found {
					t.Fatal("public interrupted/barrier evidence absent")
				}
				t.Log("A-GK-06 tier B: named tagged seam, actual SIGKILL, cleared trigger, interrupted same-key recovery, no extra paste")
			})
		}
	}
}

func TestAdapterRealNoPasteRefusals(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, variant := range []string{"busy", "composer", "dead"} {
			t.Run(provider+"/"+variant, func(t *testing.T) {
				a := newAdapterFixture(t, "gashki-release", "normal")
				pane := a.pane(provider, "planner", "normal")
				plan := a.sendPlan(pane)
				if variant == "busy" {
					put(t, filepath.Join(a.root, "stub", provider+".busy"), []byte("3"))
					a.tm("send-keys", "-t", pane.Target, "C-g")
					end := time.Now().Add(2 * time.Second)
					seen := false
					for time.Now().Before(end) {
						d := a.ok("observe", "", "observe", pane.UUID)
						if d["safe_to_send"] == false {
							seen = true
							break
						}
						time.Sleep(20 * time.Millisecond)
					}
					if !seen {
						t.Fatal("busy screen was not observed")
					}
				} else if variant == "composer" {
					a.tm("send-keys", "-t", pane.Target, "-l", "foreign composer text")
					end := time.Now().Add(time.Second)
					seen := false
					for time.Now().Before(end) {
						screen := a.tm("capture-pane", "-p", "-t", pane.Target)
						if strings.Contains(screen, "foreign composer text") {
							seen = true
							break
						}
						time.Sleep(20 * time.Millisecond)
					}
					if !seen {
						t.Fatal("composer text absent")
					}
				} else {
					a.tm("kill-pane", "-t", pane.Target)
					code, response := a.call("gashki-release", "send", plan.Payload, nil, "send", pane.UUID, "--from-stdin", "--idempotency-key="+plan.Key)
					checked, e := a.client.Protocol.Check("send", jsonBytes(response), code)
					if e != nil || !gashki.NoPasteRefusal(checked, "send", pane.UUID, a.client.SourceChecked()) {
						t.Fatal("dead refusal not checked", response, e)
					}
				}
				delivery, e := a.manager.Deliver(context.Background(), plan, gashki.Budget{Limit: 15 * time.Second, Started: time.Now()})
				if variant == "busy" {
					if e != nil || delivery.Kind != review.WithCursor {
						t.Fatal(delivery, e)
					}
					result, e := a.manager.AwaitCompletion(context.Background(), plan, delivery, gashki.Budget{Limit: 10 * time.Second, Started: time.Now()})
					if e != nil || result.Kind != review.Completed {
						t.Fatal(result, e)
					}
					a.independentCounts(provider, 1, 1, 1, 1)
				} else {
					want := "SEND_UNCERTAIN"
					if variant == "dead" {
						want = "SESSION_LOST"
					}
					if adapterCode(e) != want {
						t.Fatal(delivery, e)
					}
					a.independentCounts(provider, 1, 0, 0, 0)
					if variant == "composer" && !strings.Contains(a.tm("capture-pane", "-p", "-t", pane.Target), "foreign composer text") {
						t.Fatal("composer changed")
					}
				}
				files, e := filepath.Glob(filepath.Join(a.store.Path, "state", "control", "*-stdout.raw"))
				if e != nil {
					t.Fatal(e)
				}
				if variant != "dead" {
					wanted := "NOT_SAFE_TO_SEND"
					if variant == "composer" {
						wanted = "COMPOSER_NOT_EMPTY"
					}
					found := false
					for _, file := range files {
						b := read(t, file)
						if bytes.Contains(b, []byte(`"code":"`+wanted+`"`)) {
							exit := int(gashki.Integer(gashki.Map(gashki.Map(a.client.Protocol.Capabilities["error_codes"])[wanted])["exit_code"]))
							response, e := a.client.Protocol.Check("send", b, exit)
							if e != nil || !gashki.NoPasteRefusal(response, "send", pane.UUID, a.client.SourceChecked()) {
								t.Fatal("refusal no-paste proof", e)
							}
							found = true
						}
					}
					if !found {
						t.Fatal("public refusal absent", wanted)
					}
				}
				t.Log("A-GK-09 tier B: source-bound refusal; independent process, paste and hook counts")
			})
		}
	}
}

func TestAdapterRealCleanupPolicy(t *testing.T) {
	for _, variant := range []string{"already-dead", "kill-failed", "unknown-owner", "active", "unfinished"} {
		t.Run(variant, func(t *testing.T) {
			binary := "gashki-release"
			if variant == "kill-failed" {
				binary = "gashki-fault"
			}

			a := newAdapterFixture(t, binary, "normal")
			pane := a.pane("claude", "planner", "normal")
			approved := []byte("# Approved synthetic artifact\nDo not change these bytes.\n")
			if _, e := a.store.StagePrivateText("SPEC.md", approved); e != nil {
				t.Fatal(e)
			}
			switch variant {
			case "already-dead":
				a.tm("kill-pane", "-t", pane.Target)
			case "kill-failed":
				a.env = append(a.env, "GASHKI_TEST_FAULT=kill-ledger")
				a.makeClient()
			case "unknown-owner":
				a.tm("set-option", "-p", "-t", pane.Target, "@gashki_id", "01234567-1234-7123-8123-0123456789ff")
			case "active":
				put(t, filepath.Join(a.root, "stub", "claude.busy"), []byte("3"))
				a.tm("send-keys", "-t", pane.Target, "C-g")
				end := time.Now().Add(time.Second)
				for time.Now().Before(end) {
					obs := a.ok("observe", "", "observe", pane.UUID)
					if obs["state"] == "working" {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			result, e := a.manager.Cleanup(context.Background(), []gashki.PaneBinding{pane}, variant != "unfinished", contract.HashBytes(approved), gashki.Budget{})
			if e != nil {
				t.Fatal(e)
			}
			saved, e := a.store.ReadText("SPEC.md")
			if e != nil || !bytes.Equal(saved, approved) {
				t.Fatal("cleanup changed approved artifact", e)
			}
			if variant == "already-dead" {
				if result.Pending || len(result.Removed) != 1 {
					t.Fatal(result)
				}
			} else {
				if len(result.Retained) != 1 || len(result.Removed) != 0 || result.Pending != (variant != "unfinished") {
					t.Fatal(result)
				}
				if a.tm("display-message", "-p", "-t", pane.Target, "#{pane_id}") != pane.Target {
					t.Fatal("retained pane removed")
				}
			}
			a.independentCounts("claude", 1, 0, 0, 0)
			put(t, filepath.Join(a.root, "cleanup-policy-result.json"), jsonBytes(result))
			t.Log("A-GK-12 tier B: checked cleanup policy, retained uncertain/active panes, immutable approved bytes")
		})
	}
}

func TestAdapterRealHereWindowConflict(t *testing.T) {
	a := newAdapterFixture(t, "gashki-release", "here")
	plan := a.prepare("claude", "planner", "here")
	pane, e := a.manager.Spawn(context.Background(), plan, nil, gashki.Budget{})
	if e != nil {
		t.Fatal(e)
	}
	target := a.tm("new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "other-caller", "/bin/cat")
	env := []string{}
	for _, v := range a.env {
		if !strings.HasPrefix(v, "TMUX_PANE=") {
			env = append(env, v)
		}
	}
	a.env = append(env, "TMUX_PANE="+target)
	a.makeClient()
	_, e = a.manager.Spawn(context.Background(), plan, &pane, gashki.Budget{})
	if adapterCode(e) != "PANE_CONFLICT" {
		t.Fatal(e)
	}
	if a.tm("display-message", "-p", "-t", pane.Target, "#{@gashki_id}") != pane.UUID {
		t.Fatal("existing pane replaced")
	}
	a.independentCounts("claude", 1, 0, 0, 0)
	t.Log("A-GK-04 tier B: another owned caller window is refused; no adoption or replacement")
}

func TestAdapterRealUnsetSocketServerChange(t *testing.T) {
	a := newAdapterFixture(t, "gashki-release", "default")
	if a.client.SocketName() != nil {
		t.Fatal("socket should be unset")
	}
	pane := a.pane("claude", "planner", "normal")
	plan := a.sendPlan(pane)
	other := newFixture(t)
	env := []string{}
	for _, v := range a.env {
		if !strings.HasPrefix(v, "TMUX=") && !strings.HasPrefix(v, "TMUX_PANE=") {
			env = append(env, v)
		}
	}
	a.env = append(env, "TMUX="+other.socketPath+",1,0", "TMUX_PANE="+other.caller)
	a.makeClient()
	_, e := a.manager.Deliver(context.Background(), plan, gashki.Budget{})
	if adapterCode(e) != "SERVER_CONFLICT" {
		t.Fatal(e)
	}
	if a.tm("display-message", "-p", "-t", pane.Target, "#{@gashki_id}") != pane.UUID {
		t.Fatal("saved server pane changed")
	}
	a.independentCounts("claude", 1, 0, 0, 0)
	t.Log("A-GK-04 tier B: unset socket selects another actual owned server; saved server conflict forbids send")
}
