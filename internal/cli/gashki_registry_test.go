//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/gashki"
	"github.com/TheEditor/volley/internal/ops"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/store"
	"github.com/TheEditor/volley/tests/ownedgashki"
)

func TestCLIGashkiChild(t *testing.T) {
	if exit := ownedgashki.Run(); exit >= 0 {
		os.Exit(exit)
	}
}

type contractObserver struct {
	view    gashki.ServerView
	missing bool
}

func (o *contractObserver) Snapshot(_ context.Context, _ *string, _ []string, target string, _ gashki.Budget) (gashki.ServerView, error) {
	if target != "" && o.missing {
		return o.view, errors.New("owned missing pane")
	}
	return o.view, nil
}

type gashkiFixture struct {
	f        *contractFixture
	client   *gashki.Client
	manager  *gashki.PaneManager
	observer *contractObserver
	pane     gashki.PaneBinding
	store    *store.Store
	protocol *gashki.Protocol
	run      string
}

func newGashkiFixture(t *testing.T) *gashkiFixture {
	t.Helper()
	f := newContractFixture(t, true)
	g := &gashkiFixture{f: f}
	var err error
	g.store, err = store.Open(f.ws)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.store.Close() })
	if err = g.store.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err = g.store.AcquireOwner(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	g.run, _ = store.NewID()
	_, err = g.store.StagePrivateText("gashki.config.toml", []byte("state_dir = \"/fixture/state\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	short, err := os.MkdirTemp("/tmp", "vgr-")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(short, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close(); os.RemoveAll(short) })
	server, err := gashki.CanonicalSocket(filepath.Join(short, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	g.pane = gashki.PaneBinding{RunID: g.run, Role: "planner", Provider: "claude", UUID: "01234567-1234-7123-8123-0123456789ab", Target: "%1", Selector: "vly-" + g.run + "/planner", Server: server, SessionReason: "public_gashki_contract_has_no_vendor_session_id"}
	g.observer = &contractObserver{view: gashki.ServerView{Server: server, UUID: g.pane.UUID, Provider: g.pane.Provider, Selector: g.pane.Selector, Target: g.pane.Target, Window: "@1"}}
	exe, _ := os.Executable()
	binary := filepath.Join(f.root, "tools/gashki")
	b := []byte("#!/bin/sh\nexec " + ops.Command(exe) + " -test.run=^TestCLIGashkiChild$ -- volley-owned-gk \"$@\"\n")
	if err = os.WriteFile(binary, b, 0700); err != nil {
		t.Fatal(err)
	}
	g.protocol, err = gashki.LoadProtocol()
	if err != nil {
		t.Fatal(err)
	}
	g.frame(t, "version", map[string]any{"contract_version": "2", "build": map[string]any{"commit": nil, "date": nil}}, "")
	g.frame(t, "capabilities", g.protocol.Capabilities, "")
	g.frame(t, "schema", g.protocol.SchemaData, "")
	config := map[string]any{"state_dir": "/fixture/state", "tmux_socket": nil, "ready_timeout": "15s", "wait_timeout": "15s"}
	prov := map[string]any{"_config_file": "fixture"}
	for k := range config {
		prov[k] = map[string]any{"source": "default"}
	}
	g.frame(t, "config-show", map[string]any{"config": config, "_provenance": prov}, "")
	g.frame(t, "config-get-state_dir", map[string]any{"key": "state_dir", "value": "/fixture/state"}, "")
	g.frame(t, "config-get-tmux_socket", map[string]any{"key": "tmux_socket", "value": nil}, "")
	g.frame(t, "status", map[string]any{"items": []any{}, "recommended_action": nil}, "")
	env := append(append([]string{}, f.opts.RunOptions.Env...), "VOLLEY_GK_CANNED_ROOT="+f.root)
	g.client, err = gashki.NewClient(gashki.ClientOptions{RunID: g.run, Store: g.store, Runner: process.UnixRunner{}, Binary: binary, Config: filepath.Join(f.ws, "gashki.config.toml"), Env: env, Gate: func(context.Context, string) error { return nil }, Register: func([]string) error { return nil }, BeforeMutation: func(_ context.Context, c gashki.CallIntent) error {
		saved, e := g.store.ReadText("state/control/gk-call-" + c.ID + "-intent.json")
		if e != nil {
			return e
		}
		want, _ := contract.Canonical(c)
		if string(saved) != string(want) {
			return fmt.Errorf("durable intent differs")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = g.client.Preflight(context.Background()); err != nil {
		var native *contract.Error
		if errors.As(err, &native) {
			if id, ok := native.Evidence["call_id"].(string); ok {
				call, _, _ := g.client.LoadCall(id)
				t.Log("checked preflight failure", call.ValidationError, call.Process, call.RunnerError)
			}
		}
		t.Fatal(err)
	}
	g.manager = &gashki.PaneManager{Client: g.client, Observer: g.observer, ExpectedServer: server}
	return g
}
func (g *gashkiFixture) raw(t *testing.T, name string, b []byte, exit int) {
	t.Helper()
	stderr := ""
	if strings.Contains(string(b), "TMUX_SPLIT_FAILED") {
		stderr = "Owned window has one row; split refused\n"
	}
	frame, _ := json.Marshal(map[string]any{"raw": b, "exit": exit, "stderr": stderr})
	if err := os.WriteFile(filepath.Join(g.f.root, name+".json"), frame, 0600); err != nil {
		t.Fatal(err)
	}
}
func (g *gashkiFixture) frame(t *testing.T, name string, data any, code string) {
	t.Helper()
	b, _ := json.Marshal(data)
	var typed any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	decoder.Decode(&typed)
	canonical, err := gashki.PayloadCanonical(typed)
	if err != nil {
		t.Fatal(err)
	}
	errs := []any{}
	exit := 0
	if code != "" {
		decl := gashki.Map(gashki.Map(g.protocol.Capabilities["error_codes"])[code])
		exit = int(gashki.Integer(decl["exit_code"]))
		errs = append(errs, map[string]any{"code": code, "message": "owned upstream refusal", "path": nil, "did_you_mean": nil, "remediation": nil, "exit_code": decl["exit_code"], "retryable": decl["retryable"], "evidence": map[string]any{}})
	}
	b, _ = json.Marshal(map[string]any{"ok": code == "", "tool_version": "fixture", "data": typed, "meta": map[string]any{"request_id": "01234567-1234-7123-8123-0123456789ab", "ts_iso": "2026-10-04T00:00:00.000Z", "elapsed_ms": 0, "contract_version": "2", "data_hash": "sha256:" + contract.HashBytes(canonical)}, "errors": errs, "warnings": []any{}, "commands": []any{}})
	g.raw(t, name, b, exit)
}
func TestContractRegistryGashki(t *testing.T) {
	for _, id := range []string{"R21", "R22", "R26", "R28", "R42", "cleanup"} {
		t.Run(id, func(t *testing.T) {
			g := newGashkiFixture(t)
			f := g.f
			code := ""
			var action func(context.Context) (any, error)
			switch id {
			case "R21":
				code = "SERVER_CONFLICT"
				short, e := os.MkdirTemp("/tmp", "vgr-other-")
				if e != nil {
					t.Fatal(e)
				}
				listener, e := net.Listen("unix", filepath.Join(short, "socket"))
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { listener.Close(); os.RemoveAll(short) })
				other, e := gashki.CanonicalSocket(filepath.Join(short, "socket"))
				if e != nil {
					t.Fatal(e)
				}
				g.observer.view.Server = other
				action = func(ctx context.Context) (any, error) { return nil, g.manager.CheckPane(ctx, g.pane, gashki.Budget{}) }
			case "R22":
				code = "PANE_CONFLICT"
				g.observer.view.UUID = "01234567-1234-7123-8123-0123456789ff"
				action = func(ctx context.Context) (any, error) { return nil, g.manager.CheckPane(ctx, g.pane, gashki.Budget{}) }
			case "R28":
				code = "SESSION_LOST"
				g.observer.missing = true
				g.frame(t, "observe", nil, "NOT_FOUND")
				action = func(ctx context.Context) (any, error) { return nil, g.manager.CheckPane(ctx, g.pane, gashki.Budget{}) }
			case "R42":
				code = "EXECUTION_ENVIRONMENT_FAILED"
				g.frame(t, "spawn", nil, "TMUX_SPLIT_FAILED")
				action = func(ctx context.Context) (any, error) {
					plan, e := g.manager.PrepareSpawn(ctx, gashki.SpawnRequest{RunID: g.run, Role: "planner", Provider: "claude", Cwd: f.ws, Placement: "normal", AgentArgs: []string{}}, gashki.Budget{})
					if e != nil {
						return nil, e
					}
					_, e = g.manager.Spawn(ctx, plan, nil, gashki.Budget{})
					return nil, e
				}
			case "R26":
				code = "SEND_UNCERTAIN"
				turn, _ := store.NewID()
				plan, e := g.manager.PrepareSend(context.Background(), turn, g.pane, []byte("Owned prompt\n"), gashki.Budget{})
				if e != nil {
					t.Fatal(e)
				}
				g.raw(t, "send", []byte("{malformed"), 0)
				action = func(ctx context.Context) (any, error) {
					_, e := g.manager.Deliver(ctx, plan, gashki.Budget{})
					return nil, e
				}
			case "cleanup":
				action = func(ctx context.Context) (any, error) {
					result, e := g.manager.Cleanup(ctx, []gashki.PaneBinding{g.pane}, true, contract.HashBytes([]byte("different approved content")), gashki.Budget{})
					if e != nil {
						return nil, e
					}
					if !result.Pending || len(result.Warnings) != 1 {
						t.Fatal(result)
					}
					w := result.Warnings[0]
					return rawOutput{Data: map[string]any{"run_id": g.run, "workspace": f.ws, "status": "approved", "warnings": []string{"CLEANUP_PENDING"}}, Warnings: []contract.Warning{{Code: w.Code, Message: w.Message, Evidence: map[string]any{"reason": result.DiagnosticCodes[0], "path": "SPEC.md"}}}}, nil
				}
			}
			// These Tier A calls exercise the checked adapter and the public renderer.
			// The real backend handler integration is a separate Tier B requirement.
			f.opts.Command = func(ctx context.Context, _ []string, _ *contract.Registry) (any, error) { return action(ctx) }
			r := f.call(t, "A-CONTRACT-"+id, code, "--help")
			if id == "cleanup" && (len(r.Warnings) != 1 || r.Warnings[0].Code != "CLEANUP_PENDING") {
				t.Fatal(r)
			}
			if id == "R26" {
				f.call(t, "R26-saved-replay", code, "--help")
				b, _ := os.ReadFile(filepath.Join(f.root, "calls.jsonl"))
				if strings.Count(string(b), `"verb":"send"`) != 1 {
					t.Fatal("uncertain send replayed")
				}
			}
		})
	}
}

func TestContractGashkiResidualBudget(t *testing.T) {
	f := newContractFixture(t, true)
	f.opts.Command = func(_ context.Context, _ []string, _ *contract.Registry) (any, error) {
		_, err := (gashki.Budget{Limit: 500 * time.Millisecond, Started: time.Now()}).WaitChunk()
		return nil, err
	}
	f.call(t, "R32-subsecond-residual", "TURN_TIMEOUT", "--help")
	if f.launches() != 0 {
		t.Fatal("subsecond budget launched agent")
	}
}
