//go:build darwin || linux

package gashki

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

type stubFrame struct {
	Raw  []byte `json:"raw"`
	Exit int    `json:"exit"`
}
type stubInvocation struct {
	Verb  string   `json:"verb"`
	Args  []string `json:"args"`
	Input string   `json:"input"`
	PID   int      `json:"pid"`
}

func TestOwnedGashkiStub(t *testing.T) {
	at := -1
	for i, a := range os.Args {
		if a == "volley-owned-gk" {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	root := os.Getenv("VOLLEY_GK_CANNED_ROOT")
	if root == "" {
		os.Exit(90)
	}
	args := os.Args[at+1:]
	start := 0
	for start < len(args) && strings.HasPrefix(args[start], "--") {
		start++
	}
	verb := "version"
	if start < len(args) {
		verb = args[start]
		if verb == "config" && start+1 < len(args) {
			verb += " " + args[start+1]
			if args[start+1] == "get" && start+2 < len(args) {
				verb += " " + args[start+2]
			}
		}
	}
	input, e := io.ReadAll(os.Stdin)
	if e != nil {
		os.Exit(91)
	}
	path := filepath.Join(root, "calls.jsonl")
	prior, _ := os.ReadFile(path)
	count := 1
	for _, line := range strings.Split(string(prior), "\n") {
		var r stubInvocation
		if json.Unmarshal([]byte(line), &r) == nil && r.Verb == verb {
			count++
		}
	}
	f, e := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		os.Exit(92)
	}
	_ = json.NewEncoder(f).Encode(stubInvocation{verb, args, string(input), os.Getpid()})
	_ = f.Close()
	name := strings.ReplaceAll(verb, " ", "-")
	b, e := os.ReadFile(filepath.Join(root, fmt.Sprintf("%s-%04d.json", name, count)))
	if e != nil {
		b, e = os.ReadFile(filepath.Join(root, name+".json"))
	}
	if e != nil {
		os.Exit(93)
	}
	var frame stubFrame
	if json.Unmarshal(b, &frame) != nil {
		os.Exit(94)
	}
	_, _ = os.Stdout.Write(frame.Raw)
	os.Exit(frame.Exit)
}

type fakeObserver struct {
	Server           ServerBinding
	Pane             PaneBinding
	Foreign, Missing bool
	Window           string
}

func (o *fakeObserver) Snapshot(ctx context.Context, socket *string, env []string, target string, budget Budget) (ServerView, error) {
	v := ServerView{Server: o.Server, Target: "%0", Window: o.Window}
	if target == o.Pane.Target {
		if o.Missing {
			return v, errors.New("simulated missing target")
		}
		v.UUID = o.Pane.UUID
		v.Provider = o.Pane.Provider
		v.Selector = o.Pane.Selector
		v.Target = o.Pane.Target
		if o.Foreign {
			v.UUID = "01234567-1234-7123-8123-0123456789ff"
		}
	}
	return v, nil
}

type canned struct {
	t                *testing.T
	root             string
	store            *store.Store
	client           *Client
	manager          *PaneManager
	observer         *fakeObserver
	pane             PaneBinding
	run              string
	mutations, gates int
}

func writeTest(t *testing.T, path string, b []byte) {
	t.Helper()
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func newCanned(t *testing.T) *canned {
	t.Helper()
	f := &canned{t: t, root: t.TempDir()}
	var e error
	workspace := t.TempDir()
	for i, arg := range os.Args {
		if arg != "--evidence" || i+1 >= len(os.Args) {
			continue
		}
		parent := os.Args[i+1]
		if !filepath.IsAbs(parent) {
			t.Fatal("explicit evidence root must be absolute")
		}
		f.root, e = os.MkdirTemp(parent, "canned-")
		if e != nil {
			t.Fatal(e)
		}
		workspace = filepath.Join(f.root, "ws")
		if e := os.Mkdir(workspace, 0700); e != nil {
			t.Fatal(e)
		}
		writeTest(t, filepath.Join(f.root, "case.json"), []byte(fmt.Sprintf("{\"test\":%q}\n", t.Name())))
		t.Log("owned canned evidence", f.root)
		break
	}
	f.run, e = store.NewID()
	if e != nil {
		t.Fatal(e)
	}
	f.store, e = store.Open(workspace)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.store.Close() })
	if e := f.store.Prepare(); e != nil {
		t.Fatal(e)
	}
	if e := f.store.AcquireOwner(context.Background(), 0); e != nil {
		t.Fatal(e)
	}
	if _, e := f.store.StagePrivateText("gashki.config.toml", []byte("state_dir = \"/fixture/state\"\n")); e != nil {
		t.Fatal(e)
	}
	short, e := os.MkdirTemp("/tmp", "vcs-")
	if e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("unix", filepath.Join(short, "socket"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { listener.Close(); os.RemoveAll(short) })
	server, e := CanonicalSocket(filepath.Join(short, "socket"))
	if e != nil {
		t.Fatal(e)
	}
	name, e := selector(f.run, "planner")
	if e != nil {
		t.Fatal(e)
	}
	f.pane = PaneBinding{RunID: f.run, Role: "planner", UUID: "01234567-1234-7123-8123-0123456789ab", Provider: "claude", Selector: name, Target: "%1", Server: server, SessionReason: "public_gashki_contract_has_no_vendor_session_id"}
	f.observer = &fakeObserver{Server: server, Pane: f.pane, Window: "@1"}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	wrapper := filepath.Join(f.root, "gashki")
	// A shell is used only by the test executable wrapper, with one quoted
	// executable path and forwarded argv. The product client launches argv.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	writeTest(t, wrapper, []byte("#!/bin/sh\nexec "+quote(exe)+" -test.run=^TestOwnedGashkiStub$ -- volley-owned-gk \"$@\"\n"))
	if e := os.Chmod(wrapper, 0700); e != nil {
		t.Fatal(e)
	}
	hash := contract.HashBytes(readTest(t, wrapper))
	p, e := LoadProtocol()
	if e != nil {
		t.Fatal(e)
	}
	f.frame("version", envelope(t, map[string]any{"contract_version": "2", "build": map[string]any{"commit": nil, "date": nil}}, ""), 0)
	f.frame("capabilities", envelope(t, p.Capabilities, ""), 0)
	f.frame("schema", envelope(t, p.SchemaData, ""), 0)
	config := map[string]any{"state_dir": "/fixture/state", "tmux_socket": nil, "ready_timeout": "15s", "wait_timeout": "15s"}
	prov := map[string]any{"_config_file": "fixture"}
	for k := range config {
		prov[k] = map[string]any{"source": "default"}
	}
	f.frame("config-show", envelope(t, map[string]any{"config": config, "_provenance": prov}, ""), 0)
	f.frame("config-get-state_dir", envelope(t, map[string]any{"key": "state_dir", "value": "/fixture/state"}, ""), 0)
	f.frame("config-get-tmux_socket", envelope(t, map[string]any{"key": "tmux_socket", "value": nil}, ""), 0)
	f.frame("status", envelope(t, map[string]any{"items": []any{}, "recommended_action": nil}, ""), 0)
	f.frame("spawn", envelope(t, map[string]any{"id": f.pane.UUID, "name": name, "agent": "claude", "target": "%1", "existing": false}, ""), 0)
	f.ready()
	f.frame("observe", envelope(t, map[string]any{"id": f.pane.UUID, "name": name, "agent": "claude", "target": "%1", "state": "working", "age_ms": 0, "confidence": 1, "source": "hook", "safe_to_send": false, "cursor": "e1.4", "evidence": map[string]any{}}, ""), 0)
	f.client, e = NewClient(ClientOptions{RunID: f.run, Store: f.store, Runner: process.UnixRunner{}, Binary: wrapper, BinaryHash: hash, Config: filepath.Join(f.store.Path, "gashki.config.toml"), Env: []string{"PATH=" + f.root, "HOME=" + f.root, "TMPDIR=" + f.root, "VOLLEY_GK_CANNED_ROOT=" + f.root, "GORACE=atexit_sleep_ms=0"}, Gate: func(context.Context, string) error { f.gates++; return nil }, Register: func([]string) error { return nil }, BeforeMutation: func(ctx context.Context, intent CallIntent) error {
		f.mutations++
		b, e := f.store.ReadText(callPaths(intent.ID)[0])
		if e != nil || !bytes.Equal(b, mustOwnedJSON(intent)) {
			return fmt.Errorf("primitive intent not durable")
		}
		return nil
	}, SourceProof: &SourceProof{SourcePin, "7c67c1c039bf9fff4db5d6be1f2ee03f897b9e88a0262111f75b7c0fcea267b8", hash}})
	if e != nil {
		t.Fatal(e)
	}
	if e := f.client.Preflight(context.Background()); e != nil {
		t.Fatal(e)
	}
	f.manager = &PaneManager{Client: f.client, Observer: f.observer, ExpectedServer: server}
	return f
}
func readTest(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func (f *canned) frame(name string, raw []byte, exit int) {
	b, e := json.Marshal(stubFrame{raw, exit})
	if e != nil {
		f.t.Fatal(e)
	}
	writeTest(f.t, filepath.Join(f.root, name+".json"), b)
}
func (f *canned) calls(verb string) []stubInvocation {
	f.t.Helper()
	b, _ := os.ReadFile(filepath.Join(f.root, "calls.jsonl"))
	a := []stubInvocation{}
	for _, line := range strings.Split(string(b), "\n") {
		var r stubInvocation
		if json.Unmarshal([]byte(line), &r) == nil && r.Verb == verb {
			a = append(a, r)
		}
	}
	return a
}
func (f *canned) ready() {
	f.frame("wait", envelope(f.t, map[string]any{"id": f.pane.UUID, "name": f.pane.Selector, "until": "idle", "state": "idle", "cursor": "e1.2", "event": map[string]any{"seq": 2, "kind": "pane_ready", "source": "screen", "at": "2026-10-04T00:00:00Z", "detail": map[string]any{"name": f.pane.Selector, "socket": f.pane.Server.SocketPath}}}, ""), 0)
}
func (f *canned) spawn() PaneBinding {
	f.t.Helper()
	plan, e := f.manager.PrepareSpawn(context.Background(), SpawnRequest{RunID: f.run, Role: "planner", Provider: "claude", Cwd: f.store.Path, Placement: "normal", AgentArgs: []string{}}, Budget{})
	if e != nil {
		f.t.Fatal(e)
	}
	p, e := f.manager.Spawn(context.Background(), plan, nil, Budget{})
	if e != nil {
		f.t.Fatal(e)
	}
	f.pane = p
	f.observer.Pane = p
	return p
}
func (f *canned) sendPlan() SendIntent {
	f.t.Helper()
	pane := f.spawn()
	turn, e := store.NewID()
	if e != nil {
		f.t.Fatal(e)
	}
	plan, e := f.manager.PrepareSend(context.Background(), turn, pane, []byte("Owned prompt\n"), Budget{})
	if e != nil {
		f.t.Fatal(e)
	}
	f.frame("send", envelope(f.t, map[string]any{"id": pane.UUID, "name": pane.Selector, "target": pane.Target, "send_id": "01234567-1234-7123-8123-0123456789bc", "attempt": 1, "submitted": true, "replayed": false, "barrier_cursor": "e1.3", "turn_cursor": "e1.4", "payload_sha256": plan.PayloadHash, "match_sha256": plan.PayloadHash}, ""), 0)
	return plan
}
func errorCode(err error) string {
	var e *contract.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestAGK03OwnedSpawnReconciliation(t *testing.T) {
	for _, variant := range []string{"before-call", "lost-reply", "existing-foreign", "foreign-uuid", "foreign-provider", "foreign-window"} {
		t.Run(variant, func(t *testing.T) {
			f := newCanned(t)
			q := SpawnRequest{RunID: f.run, Role: "planner", Provider: "claude", Cwd: f.store.Path, Placement: "normal", AgentArgs: []string{}}
			if variant == "foreign-window" {
				q.Placement = "here"
				f.client.options.Env = append(f.client.options.Env, "TMUX=fixture,1,0", "TMUX_PANE=%0")
			}
			plan, e := f.manager.PrepareSpawn(context.Background(), q, Budget{})
			if e != nil {
				t.Fatal(e)
			}
			switch variant {
			case "lost-reply":
				f.frame("spawn-0001", nil, 0)
				raw := readTest(t, filepath.Join(f.root, "spawn.json"))
				f.frame("spawn-0002", rehash(t, rewrite(t, decodeFrame(t, raw), func(m map[string]any) { Map(m["data"])["existing"] = true })), 0)
			case "existing-foreign":
				raw := decodeFrame(t, readTest(t, filepath.Join(f.root, "spawn.json")))
				f.frame("spawn", rehash(t, rewrite(t, raw, func(m map[string]any) { Map(m["data"])["existing"] = true })), 0)
			case "foreign-uuid":
				f.observer.Foreign = true
			case "foreign-provider":
				f.observer.Pane.Provider = "codex"
			case "foreign-window":
				f.observer.Window = "@2"
			}
			p, e := f.manager.Spawn(context.Background(), plan, nil, Budget{})
			if strings.HasPrefix(variant, "foreign") || variant == "existing-foreign" {
				if errorCode(e) != "PANE_CONFLICT" {
					t.Fatal(p, e)
				}
				if len(f.calls("kill")) != 0 {
					t.Fatal("uncertain pane killed")
				}
			} else {
				if e != nil || p.UUID != f.pane.UUID || p.ReadyCursor == "" {
					t.Fatal(p, e)
				}
				want := 1
				if variant == "lost-reply" {
					want = 2
				}
				if len(f.calls("spawn")) != want {
					t.Fatal("spawn count")
				}
			}
		})
	}
}
func decodeFrame(t *testing.T, b []byte) []byte {
	t.Helper()
	var f stubFrame
	if e := json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	return f.Raw
}

func TestAGK07RecoveryBindingRefusals(t *testing.T) {
	for _, variant := range []string{"key", "payload", "pane", "config", "state_dir", "queried-state", "server", "unsettled"} {
		t.Run(variant, func(t *testing.T) {
			f := newCanned(t)
			plan := f.sendPlan()
			before := len(f.calls("send"))
			switch variant {
			case "key":
				plan.Key = "other"
			case "payload":
				plan.Payload += " changed"
			case "pane":
				plan.Pane.UUID = "01234567-1234-7123-8123-0123456789ff"
			case "config":
				writeTest(t, filepath.Join(f.store.Path, "gashki.config.toml"), []byte("changed"))
			case "state_dir":
				plan.StateDir = "/changed"
			case "queried-state":
				f.frame("config-get-state_dir", envelope(t, map[string]any{"key": "state_dir", "value": "/changed"}, ""), 0)
			case "server":
				f.observer.Server.Inode++
			case "unsettled":
				attempt, e := f.manager.nextAttempt(plan, nil, "initial")
				if e != nil {
					t.Fatal(e)
				}
				h, _ := f.client.ConfigHash()
				intent := CallIntent{attempt.CallID, "send", []string{"send", plan.Pane.UUID, "--from-stdin", "--idempotency-key=" + plan.Key}, contract.HashBytes([]byte(plan.Payload)), h, f.client.options.BinaryHash, f.client.Protocol.Hash}
				if e := f.client.saveExact(callPaths(attempt.CallID)[0], intent); e != nil {
					t.Fatal(e)
				}
			}
			_, e := f.manager.Deliver(context.Background(), plan, Budget{})
			if e == nil {
				t.Fatal("changed or unsettled binding accepted")
			}
			if len(f.calls("send")) != before {
				t.Fatal("recovery send released")
			}
		})
	}
}

func TestCannedLostAndMalformedSend(t *testing.T) {
	for _, variant := range []string{"missing", "malformed", "cursorless", "hash", "unverified-warning", "saved-confirmation"} {
		t.Run(variant, func(t *testing.T) {
			f := newCanned(t)
			plan := f.sendPlan()
			raw := decodeFrame(t, readTest(t, filepath.Join(f.root, "send.json")))
			switch variant {
			case "missing":
				f.frame("send-0001", nil, 0)
				replayed := rewrite(t, raw, func(m map[string]any) { Map(m["data"])["replayed"] = true })
				f.frame("send-0002", rehash(t, replayed), 0)
			case "malformed":
				f.frame("send", []byte("broken"), 0)
			case "cursorless":
				f.frame("send", rehash(t, rewrite(t, raw, func(m map[string]any) { Map(m["data"])["turn_cursor"] = "" })), 0)
			case "hash":
				f.frame("send", rehash(t, rewrite(t, raw, func(m map[string]any) { Map(m["data"])["payload_sha256"] = strings.Repeat("0", 64) })), 0)
			case "unverified-warning":
				f.frame("send", rewrite(t, raw, func(m map[string]any) {
					m["warnings"] = []any{map[string]any{"code": "SUBMIT_UNVERIFIED", "message": "fixture", "details": nil}}
				}), 0)
			}
			r, e := f.manager.Deliver(context.Background(), plan, Budget{})
			if variant == "missing" || variant == "saved-confirmation" {
				if e != nil || r.Kind != review.WithCursor {
					t.Fatal(r, e)
				}
				want := 1
				if variant == "missing" {
					want = 2
					if !r.Replayed {
						t.Fatal("replay absent")
					}
				}
				if len(f.calls("send")) != want {
					t.Fatal("send count")
				}
				again, e := f.manager.Deliver(context.Background(), plan, Budget{})
				if e != nil || again.Cursor != r.Cursor || len(f.calls("send")) != want {
					t.Fatal("saved cursor caused send", again, e)
				}
			} else {
				if errorCode(e) != "SEND_UNCERTAIN" || len(f.calls("send")) != 1 {
					t.Fatal(r, e, "malformed send replayed")
				}
			}
			for _, call := range f.calls("send") {
				if call.Input != plan.Payload || !containsArg(call.Args, "--idempotency-key="+plan.Key) || containsArg(call.Args, "--no-verify") {
					t.Fatal("payload/key changed", call)
				}
			}
		})
	}
}
func rehash(t *testing.T, b []byte) []byte {
	t.Helper()
	return rewrite(t, b, func(m map[string]any) {
		h, e := PayloadCanonical(m["data"])
		if e != nil {
			t.Fatal(e)
		}
		Map(m["meta"])["data_hash"] = "sha256:" + contract.HashBytes(h)
	})
}

func TestQualifiedCompletionAndSavedCursor(t *testing.T) {
	for _, variant := range []string{"complete", "idle-only", "wrong-id", "old-sequence", "500ms"} {
		t.Run(variant, func(t *testing.T) {
			f := newCanned(t)
			plan := f.sendPlan()
			delivery, e := f.manager.Deliver(context.Background(), plan, Budget{})
			if e != nil {
				t.Fatal(e)
			}
			data := map[string]any{"id": plan.Pane.UUID, "name": plan.Pane.Selector, "until": "idle", "state": "idle", "cursor": "e1.5", "event": map[string]any{"seq": 5, "kind": "turn_ended", "source": "hook", "at": "2026-10-04T00:00:00Z", "detail": map[string]any{}}}
			switch variant {
			case "idle-only":
				data["event"] = nil
			case "wrong-id":
				data["id"] = "01234567-1234-7123-8123-0123456789ff"
			case "old-sequence":
				data["cursor"] = "e1.3"
				Map(data["event"])["seq"] = 3
			}
			f.frame("wait", envelope(t, data, ""), 0)
			waits := len(f.calls("wait"))
			budget := Budget{}
			if variant == "500ms" {
				budget = Budget{Limit: 500 * time.Millisecond, Started: time.Now()}
			}
			r, e := f.manager.AwaitCompletion(context.Background(), plan, delivery, budget)
			if variant == "complete" {
				if e != nil || r.Kind != review.Completed {
					t.Fatal(r, e)
				}
				again, e := f.manager.AwaitCompletion(context.Background(), plan, delivery, Budget{})
				if e != nil || again.Cursor != r.Cursor || len(f.calls("wait")) != waits+1 {
					t.Fatal("completed result waited again", again, e)
				}
			} else if variant == "500ms" {
				if errorCode(e) != "TURN_TIMEOUT" || len(f.calls("wait")) != waits {
					t.Fatal("sub-second wait ran", r, e)
				}
			} else if errorCode(e) != "TURN_UNCERTAIN" {
				t.Fatal(r, e)
			}
			if len(f.calls("send")) != 1 || len(f.calls("spawn")) != 1 {
				t.Fatal("completion recovery changed agent actions")
			}
		})
	}
}

func TestCheckedClientRefusesChangedBindings(t *testing.T) {
	f := newCanned(t)
	id, e := store.NewID()
	if e != nil {
		t.Fatal(e)
	}
	if _, e := f.client.Call(context.Background(), CallRequest{ID: id, Verb: "config get", Args: []string{"spawn", "bad/name", "--agent=claude"}}); e == nil {
		t.Fatal("misdeclared mutation released")
	}
	if len(f.calls("spawn")) != 0 {
		t.Fatal("uncommitted mutation ran")
	}
	f.client.options.Gate = func(context.Context, string) error { return errors.New("frozen gate refusal") }
	if _, e := f.client.FreshCall(context.Background(), "status", nil, Budget{}, "status"); e == nil {
		t.Fatal("frozen gate bypassed")
	}
	if len(f.calls("status")) != 0 {
		t.Fatal("gated subprocess ran")
	}
}

func TestCannedTerminalCleanup(t *testing.T) {
	for _, variant := range []string{"success", "already-dead", "kill-failed", "unknown-owner", "active", "unfinished"} {
		t.Run(variant, func(t *testing.T) {
			f := newCanned(t)
			pane := f.spawn()
			approved := []byte("# Approved\nSaved content.\n")
			if _, e := f.store.StagePrivateText("SPEC.md", approved); e != nil {
				t.Fatal(e)
			}
			raw := decodeFrame(t, readTest(t, filepath.Join(f.root, "observe.json")))
			state := "idle"
			if variant == "already-dead" {
				state = "dead"
			}
			if variant == "active" {
				state = "working"
			}
			f.frame("observe", rehash(t, rewrite(t, raw, func(m map[string]any) {
				Map(m["data"])["state"] = state
				Map(m["data"])["safe_to_send"] = state == "idle"
			})), 0)
			f.frame("kill", envelope(t, map[string]any{"id": pane.UUID, "name": pane.Selector, "agent": pane.Provider, "target": pane.Target, "action": "kill", "dry_run": false, "already_killed": false, "irreversible": true}, ""), 0)
			if variant == "kill-failed" {
				f.frame("kill", envelope(t, nil, "INTERNAL"), 6)
			}
			if variant == "unknown-owner" {
				f.observer.Foreign = true
			}
			result, e := f.manager.Cleanup(context.Background(), []PaneBinding{pane}, variant != "unfinished", contract.HashBytes(approved), Budget{})
			if e != nil {
				t.Fatal(e)
			}
			saved, e := f.store.ReadText("SPEC.md")
			if e != nil || !bytes.Equal(saved, approved) {
				t.Fatal("cleanup changed approved artifact", e)
			}
			switch variant {
			case "success":
				if result.Pending || len(result.Removed) != 1 || len(f.calls("kill")) != 1 {
					t.Fatal(result)
				}
			case "already-dead":
				if result.Pending || len(result.Removed) != 1 || len(f.calls("kill")) != 0 {
					t.Fatal(result)
				}
			case "unfinished":
				if result.Pending || len(result.Retained) != 1 || len(f.calls("kill")) != 0 {
					t.Fatal(result)
				}
			default:
				if !result.Pending || len(result.Retained) != 1 || len(result.Warnings) != 1 {
					t.Fatal(result)
				}
				if variant != "kill-failed" && len(f.calls("kill")) != 0 {
					t.Fatal("unsafe kill")
				}
			}
		})
	}
}

func TestCannedNoPasteAndUnlimitedWait(t *testing.T) {
	for _, variant := range []string{"busy", "composer", "dead", "contradictory", "unlimited-working", "unlimited-compacting", "unlimited-idle", "unlimited-dead"} {
		t.Run(variant, func(t *testing.T) {
			f := newCanned(t)
			plan := f.sendPlan()
			if strings.HasPrefix(variant, "unlimited-") {
				delivery, e := f.manager.Deliver(context.Background(), plan, Budget{})
				if e != nil {
					t.Fatal(e)
				}
				timeout := rewrite(t, envelope(t, nil, "WAIT_TIMEOUT"), func(m map[string]any) {
					Map(m["errors"].([]any)[0])["evidence"] = map[string]any{"id": plan.Pane.UUID, "since": delivery.Cursor, "cursor": delivery.Cursor}
				})
				f.frame("wait-0002", timeout, 4)
				complete := map[string]any{"id": plan.Pane.UUID, "name": plan.Pane.Selector, "until": "idle", "state": "idle", "cursor": "e1.5", "event": map[string]any{"seq": 5, "kind": "turn_ended", "source": "hook", "at": "2026-10-04T00:00:00Z", "detail": map[string]any{}}}
				f.frame("wait-0003", envelope(t, complete, ""), 0)
				state := strings.TrimPrefix(variant, "unlimited-")
				raw := decodeFrame(t, readTest(t, filepath.Join(f.root, "observe.json")))
				f.frame("observe", rehash(t, rewrite(t, raw, func(m map[string]any) { Map(m["data"])["state"] = state })), 0)
				r, e := f.manager.AwaitCompletion(context.Background(), plan, delivery, Budget{})
				if state == "working" || state == "compacting" {
					if e != nil || r.Kind != review.Completed || len(f.calls("wait")) != 3 {
						t.Fatal(r, e)
					}
				} else if errorCode(e) != "TURN_UNCERTAIN" || len(f.calls("wait")) != 2 {
					t.Fatal(r, e)
				}
				if len(f.calls("send")) != 1 {
					t.Fatal("wait timeout caused send")
				}
				return
			}
			code := "NOT_SAFE_TO_SEND"
			if variant == "composer" {
				code = "COMPOSER_NOT_EMPTY"
			}
			if variant == "dead" {
				code = "PANE_DEAD"
			}
			raw := rewrite(t, envelope(t, nil, code), func(m map[string]any) {
				ev := map[string]any{"id": plan.Pane.UUID, "state": "working", "source": "hook", "confidence": 1}
				if variant == "contradictory" {
					ev["barrier_cursor"] = "e1.3"
				}
				Map(m["errors"].([]any)[0])["evidence"] = ev
			})
			decl := Map(Map(f.client.Protocol.Capabilities["error_codes"])[code])
			f.frame("send-0001", raw, int(Integer(decl["exit_code"])))
			result, e := f.manager.Deliver(context.Background(), plan, Budget{})
			if variant == "busy" {
				if e != nil || result.Kind != review.WithCursor || len(f.calls("send")) != 2 {
					t.Fatal(result, e)
				}
				for _, call := range f.calls("send") {
					if call.Input != plan.Payload || !containsArg(call.Args, "--idempotency-key="+plan.Key) {
						t.Fatal("no-paste retry binding changed")
					}
				}
			} else {
				want := "SEND_UNCERTAIN"
				if variant == "dead" {
					want = "SESSION_LOST"
				}
				if errorCode(e) != want || len(f.calls("send")) != 1 {
					t.Fatal(result, e)
				}
			}
		})
	}
}

func TestAGK08CannedPublicErrorEvidence(t *testing.T) {
	f := newCanned(t)
	cases := []struct{ verb, codes string }{
		{"config get", "INVALID_CONFIG INVALID_INPUT MISSING_REQUIRED UNKNOWN_FLAG UNKNOWN_COMMAND INTERNAL"},
		{"spawn", "HERE_UNAVAILABLE TMUX_SPLIT_FAILED AGENT_CLI_MISSING CODEX_HOOKS_UNTRUSTED LAUNCH_PROMPT_UNHANDLED ENV_NOT_SET CONFLICT TMUX_NO_SERVER STATE_DIR_UNWRITABLE"},
		{"send", "NOT_FOUND PANE_DEAD AGENT_UNVERIFIABLE NOT_SAFE_TO_SEND COMPOSER_NOT_EMPTY LOCKED IDEMPOTENCY_CONFLICT SEND_STUCK_IN_COMPOSER SEND_UNCONFIRMED SEND_INTERRUPTED SEND_INPUT_MIXED INTERNAL"},
		{"wait", "APPROVAL_REQUIRED TURN_FAILED AGENT_NOT_LOGGED_IN WAIT_TIMEOUT PANE_DEAD NOT_FOUND INVALID_CONFIG STATE_DIR_UNWRITABLE"},
		{"observe", "NOT_FOUND TMUX_NO_SERVER STATE_DIR_UNWRITABLE INTERNAL"},
		{"status", "TMUX_NO_SERVER STATE_DIR_UNWRITABLE INVALID_CONFIG INTERNAL"},
		{"kill", "LOCKED IDEMPOTENCY_CONFLICT NOT_FOUND INTERNAL STATE_DIR_UNWRITABLE"},
	}
	for _, group := range cases {
		for _, code := range strings.Fields(group.codes) {
			t.Run(group.verb+"/"+code, func(t *testing.T) {
				raw := envelope(t, nil, code)
				decl := Map(Map(f.client.Protocol.Capabilities["error_codes"])[code])
				exit := int(Integer(decl["exit_code"]))
				name := strings.ReplaceAll(group.verb, " ", "-")
				args := strings.Split(group.verb, " ")
				if group.verb == "config get" {
					name += "-state_dir"
					args = append(args, "state_dir")
				}
				f.frame(name, raw, exit)
				call, e := f.client.FreshCall(context.Background(), group.verb, nil, Budget{}, args...)
				if e != nil || call.ValidationError != "" || call.Response.OK || len(call.Response.Errors) != 1 {
					t.Fatal(call, e)
				}
				upstream := call.Response.Errors[0]
				if upstream.Code != code || upstream.Exit != exit || upstream.Message != "fixture" {
					t.Fatal(upstream)
				}
				b, _, e := f.store.ReadRaw(call.RawPath)
				if e != nil || !bytes.Equal(b, raw) || call.RawHash != contract.HashBytes(raw) {
					t.Fatal("raw response not retained", e)
				}
				recovered, exists, e := f.client.LoadCall(call.ID)
				if e != nil || !exists || recovered.Response.Errors[0].Code != code || recovered.RawHash != call.RawHash {
					t.Fatal("record recovery lost error evidence", recovered, e)
				}
				d := MapFailure(code, ActionContext{Verb: group.verb, Checked: true, Unfinished: group.verb == "wait"})
				if !d.Retain {
					t.Fatal("error discarded ownership evidence", d)
				}
			})
		}
	}
	for _, q := range []struct{ verb, code string }{{"send", "CONFLICT"}, {"spawn", "APPROVAL_REQUIRED"}, {"config get", "ENV_NOT_SET"}, {"kill", "WAIT_TIMEOUT"}} {
		t.Run("wrong-verb/"+q.verb+"/"+q.code, func(t *testing.T) {
			name := strings.ReplaceAll(q.verb, " ", "-")
			args := strings.Split(q.verb, " ")
			if q.verb == "config get" {
				name += "-state_dir"
				args = append(args, "state_dir")
			}
			exit := int(Integer(Map(Map(f.client.Protocol.Capabilities["error_codes"])[q.code])["exit_code"]))
			f.frame(name, envelope(t, nil, q.code), exit)
			call, e := f.client.FreshCall(context.Background(), q.verb, nil, Budget{}, args...)
			if e != nil || call.ValidationError == "" {
				t.Fatal("wrong verb accepted", call, e)
			}
		})
	}
}

func TestSavedDeliveryAndCompletionRawEvidence(t *testing.T) {
	for _, variant := range []string{"delivery", "completion"} {
		t.Run(variant, func(t *testing.T) {
			f := newCanned(t)
			plan := f.sendPlan()
			delivery, e := f.manager.Deliver(context.Background(), plan, Budget{})
			if e != nil {
				t.Fatal(e)
			}
			var path string
			if variant == "delivery" {
				attempts, e := f.manager.attempts(plan)
				if e != nil {
					t.Fatal(e)
				}
				path = callPaths(attempts[len(attempts)-1].CallID)[2]
			} else {
				data := map[string]any{"id": plan.Pane.UUID, "name": plan.Pane.Selector, "until": "idle", "state": "idle", "cursor": "e1.5", "event": map[string]any{"seq": 5, "kind": "turn_ended", "source": "hook", "at": "2026-10-04T00:00:00Z", "detail": map[string]any{}}}
				f.frame("wait", envelope(t, data, ""), 0)
				completed, e := f.manager.AwaitCompletion(context.Background(), plan, delivery, Budget{})
				if e != nil {
					t.Fatal(e)
				}
				path = completed.EvidencePaths[len(completed.EvidencePaths)-2]
			}
			writeTest(t, filepath.Join(f.store.Path, path), []byte("changed raw evidence"))
			sends, waits := len(f.calls("send")), len(f.calls("wait"))
			if variant == "delivery" {
				_, e = f.manager.Deliver(context.Background(), plan, Budget{})
			} else {
				_, e = f.manager.AwaitCompletion(context.Background(), plan, delivery, Budget{})
			}
			if errorCode(e) != "ARTIFACT_CHANGED" || len(f.calls("send")) != sends || len(f.calls("wait")) != waits {
				t.Fatal("changed evidence accepted or replayed", e)
			}
		})
	}
}

func TestSpawnRequiresUsableReadyState(t *testing.T) {
	f := newCanned(t)
	raw := decodeFrame(t, readTest(t, filepath.Join(f.root, "wait.json")))
	f.frame("wait", rehash(t, rewrite(t, raw, func(m map[string]any) { Map(m["data"])["state"] = "working" })), 0)
	plan, e := f.manager.PrepareSpawn(context.Background(), SpawnRequest{RunID: f.run, Role: "planner", Provider: "claude", Cwd: f.store.Path, Placement: "normal"}, Budget{})
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.manager.Spawn(context.Background(), plan, nil, Budget{})
	if errorCode(e) != "PANE_CONFLICT" || len(f.calls("kill")) != 0 {
		t.Fatal("unusable ready state accepted", e)
	}
}
