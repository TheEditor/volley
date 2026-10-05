//go:build darwin || linux

package gashki

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
)

type SpawnRequest struct {
	RunID, Role, Provider, Cwd, Placement string
	AgentArgs                             []string
	TrustFolder                           bool
}
type SpawnIntent struct {
	RunID             string        `json:"run_id"`
	Role              string        `json:"role"`
	Provider          string        `json:"provider"`
	Cwd               string        `json:"cwd"`
	Placement         string        `json:"placement"`
	AgentArgs         []string      `json:"agent_args"`
	TrustFolder       bool          `json:"trust_folder"`
	Selector          string        `json:"selector"`
	AgentArgsHash     string        `json:"agent_args_hash"`
	ConfigHash        string        `json:"config_hash"`
	Server            ServerBinding `json:"server"`
	OwnedAbsentBefore bool          `json:"owned_absent_before"`
	CallID            string        `json:"call_id"`
	ReconcileID       string        `json:"reconcile_id"`
	At                string        `json:"at"`
}

func spawnPath(run, role string) string { return "state/control/gk-spawn-" + run + "-" + role }
func (c *Client) saveExact(path string, v any) error {
	b, e := ownedJSON(v)
	if e != nil {
		return e
	}
	if old, e := c.options.Store.ReadText(path); e == nil {
		if !bytes.Equal(old, b) {
			return NativeError(decision("IDEMPOTENCY_CONFLICT", "immutable_operation_changed"), map[string]any{"path": path})
		}
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	if e := c.options.Register([]string{path}); e != nil {
		return e
	}
	return c.writeText(path, b)
}

func (m *PaneManager) PrepareSpawn(ctx context.Context, q SpawnRequest, budget Budget) (SpawnIntent, error) {
	var plan SpawnIntent
	q.AgentArgs = append([]string{}, q.AgentArgs...)
	if m.Client == nil || q.RunID != m.Client.options.RunID || q.Provider != "claude" && q.Provider != "codex" || q.Cwd != m.Client.options.Store.Path {
		return plan, fmt.Errorf("Invalid owned spawn context")
	}
	name, e := selector(q.RunID, q.Role)
	if e != nil {
		return plan, e
	}
	if q.Placement != "auto" && q.Placement != "normal" && q.Placement != "here" {
		return plan, fmt.Errorf("Invalid pane placement")
	}
	for _, arg := range q.AgentArgs {
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return plan, fmt.Errorf("Invalid provider argument")
		}
	}
	agentJSON, e := ownedJSON(q.AgentArgs)
	if e != nil {
		return plan, e
	}
	h, e := m.Client.ConfigHash()
	if e != nil {
		return plan, e
	}
	base := spawnPath(q.RunID, q.Role)
	if b, e := m.Client.options.Store.ReadText(base + "-intent.json"); e == nil {
		if e := decodeClosed(b, &plan); e != nil {
			return plan, e
		}
		if plan.RunID != q.RunID || plan.Role != q.Role || plan.Provider != q.Provider || plan.Cwd != q.Cwd || plan.AgentArgsHash != contract.HashBytes(agentJSON) || plan.TrustFolder != q.TrustFolder || plan.ConfigHash != h || plan.Selector != name {
			return plan, NativeError(decision("PANE_CONFLICT", "saved_spawn_intent_changed"), nil)
		}
		if q.Placement != "auto" && q.Placement != plan.Placement {
			return plan, NativeError(decision("PANE_CONFLICT", "saved_placement_changed"), nil)
		}
		return plan, nil
	} else if !os.IsNotExist(e) {
		return plan, e
	}
	view, e := m.selected(ctx, "", budget)
	if e != nil {
		return plan, e
	}
	placement := q.Placement
	if placement == "auto" {
		placement = "normal"
		if environmentValue(m.Client.options.Env, "TMUX") != "" && environmentValue(m.Client.options.Env, "TMUX_PANE") != "" {
			placement = "here"
		}
	}
	server := view.Server
	if placement == "here" {
		caller := environmentValue(m.Client.options.Env, "TMUX_PANE")
		if caller == "" || environmentValue(m.Client.options.Env, "TMUX") == "" {
			return plan, NativeError(decision("PANE_CONFLICT", "here_caller_unavailable"), nil)
		}
		view, e = m.selected(ctx, caller, budget)
		if e != nil {
			return plan, e
		}
		server.CallerWindow = view.Window
	}
	// A fresh intent first proves the random selector was not already live.
	status, e := m.Client.FreshCall(ctx, "status", nil, budget, "status", "--limit=500")
	if e != nil {
		return plan, e
	}
	if status.ValidationError != "" || !status.Response.OK {
		return plan, NativeError(decision("UPSTREAM_FAILURE", "spawn_ownership_discovery_unchecked"), map[string]any{"call_id": status.ID})
	}
	meta := Map(status.Response.Meta["pagination"])
	if meta["has_more"] == true {
		return plan, NativeError(decision("TURN_UNCERTAIN", "ownership_discovery_incomplete"), nil)
	}
	for _, raw := range Map(status.Response.Data)["items"].([]any) {
		if String(Map(raw)["name"]) == "" {
			return plan, NativeError(decision("TURN_UNCERTAIN", "ownership_discovery_missing_names"), nil)
		}
		if String(Map(raw)["name"]) == name {
			return plan, NativeError(decision("PANE_CONFLICT", "fresh_selector_already_exists"), nil)
		}
	}
	callID, e := store.NewID()
	if e != nil {
		return plan, e
	}
	reconcileID, e := store.NewID()
	if e != nil {
		return plan, e
	}
	plan = SpawnIntent{q.RunID, q.Role, q.Provider, q.Cwd, placement, append([]string{}, q.AgentArgs...), q.TrustFolder, name, contract.HashBytes(agentJSON), h, server, true, callID, reconcileID, time.Now().UTC().Format(time.RFC3339Nano)}
	if e := m.Client.saveExact(base+"-intent.json", plan); e != nil {
		return plan, e
	}
	return plan, nil
}

func spawnArgs(plan SpawnIntent) []string {
	args := []string{"spawn", plan.Selector, "--agent=" + plan.Provider, "--cwd=" + plan.Cwd, "--agent-args=" + string(mustOwnedJSON(plan.AgentArgs))}
	if plan.Placement == "here" {
		args = append(args, "--here")
	}
	if plan.TrustFolder {
		args = append(args, "--trust-folder")
	}
	return args
}
func mustOwnedJSON(v any) []byte {
	b, e := ownedJSON(v)
	if e != nil {
		panic(e)
	}
	return b
}

func (m *PaneManager) Spawn(ctx context.Context, plan SpawnIntent, expected *PaneBinding, budget Budget) (PaneBinding, error) {
	var pane PaneBinding
	if plan.RunID != m.Client.options.RunID || !plan.OwnedAbsentBefore {
		return pane, NativeError(decision("PANE_CONFLICT", "spawn_owner_is_unverified"), nil)
	}
	base := spawnPath(plan.RunID, plan.Role)
	b, e := m.Client.options.Store.ReadText(base + "-intent.json")
	if e != nil {
		return pane, e
	}
	if !bytes.Equal(b, mustOwnedJSON(plan)) {
		return pane, NativeError(decision("PANE_CONFLICT", "saved_spawn_binding_changed"), nil)
	}
	if expected != nil {
		if !paneMatchesSpawn(*expected, plan) {
			return pane, NativeError(decision("PANE_CONFLICT", "expected_pane_spawn_binding_changed"), nil)
		}
		if e := m.CheckPane(ctx, *expected, budget); e != nil {
			return pane, e
		}
		return *expected, nil
	}
	if b, e := m.Client.options.Store.ReadText(base + "-pane.json"); e == nil {
		if e := decodeClosed(b, &pane); e != nil {
			return pane, e
		}
		if !paneMatchesSpawn(pane, plan) {
			return pane, NativeError(decision("PANE_CONFLICT", "saved_pane_spawn_binding_changed"), nil)
		}
		if e := m.CheckPane(ctx, pane, budget); e != nil {
			return pane, e
		}
		return pane, nil
	} else if !os.IsNotExist(e) {
		return pane, e
	}
	if e := m.checkSpawnServer(ctx, plan, budget); e != nil {
		return pane, e
	}
	call, exists, e := m.Client.LoadCall(plan.CallID)
	if e != nil {
		return pane, e
	}
	if !exists {
		call, e = m.Client.Call(ctx, CallRequest{ID: plan.CallID, Verb: "spawn", Args: spawnArgs(plan), Budget: budget})
		if e != nil {
			return pane, e
		}
	}
	reconciled := false
	priorUUID := ""
	if exists && call.ValidationError == "" && call.Response.OK {
		priorUUID = String(Map(call.Response.Data)["id"])
	}
	if call.Missing || priorUUID != "" {
		if !call.Process.Settled || !m.Client.sourceChecked {
			return pane, NativeError(decision("TURN_UNCERTAIN", "spawn_reconciliation_not_proved"), nil)
		}
		call, exists, e = m.Client.LoadCall(plan.ReconcileID)
		if e != nil {
			return pane, e
		}
		if !exists {
			call, e = m.Client.Call(ctx, CallRequest{ID: plan.ReconcileID, Verb: "spawn", Args: spawnArgs(plan), Budget: budget})
			if e != nil {
				return pane, e
			}
		}
		reconciled = true
	}
	if call.ValidationError != "" || call.Missing {
		return pane, NativeError(decision("TURN_UNCERTAIN", "spawn_response_unchecked"), map[string]any{"call_id": call.ID})
	}
	if !call.Response.OK {
		return pane, NativeError(MapFailure(call.Response.Errors[0].Code, ActionContext{Verb: "spawn", Checked: true, Unfinished: exists}), map[string]any{"call_id": call.ID, "upstream": call.Response.Errors[0]})
	}
	d := Map(call.Response.Data)
	if d["name"] != plan.Selector || d["agent"] != plan.Provider || d["existing"] == true && !reconciled || priorUUID != "" && d["id"] != priorUUID {
		return pane, NativeError(decision("PANE_CONFLICT", "spawn_returned_unowned_existing_pane"), nil)
	}
	pane = PaneBinding{RunID: plan.RunID, Role: plan.Role, UUID: String(d["id"]), Provider: plan.Provider, Selector: plan.Selector, Target: String(d["target"]), Server: plan.Server, ConfigHash: plan.ConfigHash, AgentArgsHash: plan.AgentArgsHash, SessionReason: "public_gashki_contract_has_no_vendor_session_id"}
	if e := m.CheckPane(ctx, pane, budget); e != nil {
		return pane, e
	}
	ready, e := m.Client.FreshCall(ctx, "wait", nil, budget, "wait", pane.UUID, "--until=idle", "--wait-timeout=1s")
	if e != nil {
		return pane, e
	}
	if ready.ValidationError != "" || !ready.Response.OK {
		return pane, NativeError(decision("TURN_UNCERTAIN", "pane_ready_evidence_unchecked"), map[string]any{"call_id": ready.ID})
	}
	d = Map(ready.Response.Data)
	event := Map(d["event"])
	detail := Map(event["detail"])
	socket, e := CanonicalSocket(String(detail["socket"]))
	seq, cursorErr := cursorSequence(String(d["cursor"]))
	if e != nil || !sameServer(socket, plan.Server) || d["id"] != pane.UUID || d["name"] != pane.Selector || d["state"] != "idle" || d["until"] != "idle" || event["kind"] != "pane_ready" || detail["name"] != pane.Selector || cursorErr != nil || seq != Integer(event["seq"]) || seq <= 0 {
		return pane, NativeError(decision("PANE_CONFLICT", "pane_ready_binding_changed"), nil)
	}
	pane.ReadyCursor = String(d["cursor"])
	if e := m.Client.saveExact(base+"-pane.json", pane); e != nil {
		return pane, e
	}
	return pane, nil
}

func paneMatchesSpawn(pane PaneBinding, plan SpawnIntent) bool {
	return pane.RunID == plan.RunID && pane.Role == plan.Role && pane.Selector == plan.Selector && pane.Provider == plan.Provider && pane.ConfigHash == plan.ConfigHash && pane.AgentArgsHash == plan.AgentArgsHash && sameServer(pane.Server, plan.Server) && pane.Server.CallerWindow == plan.Server.CallerWindow
}
func (m *PaneManager) checkSpawnServer(ctx context.Context, plan SpawnIntent, budget Budget) error {
	view, e := m.selected(ctx, "", budget)
	if e != nil {
		return e
	}
	if !sameServer(view.Server, plan.Server) {
		return NativeError(decision("SERVER_CONFLICT", "spawn_server_changed"), nil)
	}
	if plan.Placement == "here" {
		caller := environmentValue(m.Client.options.Env, "TMUX_PANE")
		if caller == "" {
			return NativeError(decision("PANE_CONFLICT", "here_caller_missing"), nil)
		}
		view, e = m.selected(ctx, caller, budget)
		if e != nil {
			return e
		}
		if view.Window != plan.Server.CallerWindow {
			return NativeError(decision("PANE_CONFLICT", "here_window_changed"), nil)
		}
	}
	return nil
}

func pointerPayload(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("Invalid immutable prompt path")
	}
	return []byte("Read the file " + strconv.Quote(path) + " and do what it asks."), nil
}
