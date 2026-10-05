//go:build darwin || linux

package gashki

import (
	"bytes"
	"context"
	"errors"
	"os"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

type CleanupResult struct {
	Removed         []string  `json:"removed"`
	Retained        []string  `json:"retained"`
	Pending         bool      `json:"pending"`
	Warnings        []Warning `json:"warnings"`
	DiagnosticCodes []string  `json:"diagnostic_codes"`
}
type cleanupIntent struct {
	ID           string        `json:"id"`
	Panes        []PaneBinding `json:"panes"`
	ApprovedHash string        `json:"approved_hash"`
	Final        bool          `json:"final"`
}

// Cleanup preserves final records and artifacts. Only idle, verified owned
// panes may be stopped automatically after a terminal result.
func (m *PaneManager) Cleanup(ctx context.Context, panes []PaneBinding, final bool, approvedHash string, budget Budget) (CleanupResult, error) {
	result := CleanupResult{Removed: []string{}, Retained: []string{}, Warnings: []Warning{}, DiagnosticCodes: []string{}}
	warn := func(pane PaneBinding, code string) {
		result.Pending = true
		result.Retained = append(result.Retained, pane.UUID)
		result.DiagnosticCodes = append(result.DiagnosticCodes, code)
		result.Warnings = append(result.Warnings, Warning{Code: "CLEANUP_PENDING", Message: "Owned pane cleanup is not complete", Details: map[string]any{"pane_uuid": pane.UUID, "diagnostic_code": code, "saved_server": pane.Server, "follow_up": map[string]any{"binary": m.Client.options.Binary, "args": []string{"--config=" + m.Client.options.Config, "--json", "observe", pane.UUID}}}})
	}
	if !final {
		for _, p := range panes {
			result.Retained = append(result.Retained, p.UUID)
		}
		return result, nil
	}
	before, e := m.Client.options.Store.ReadText("SPEC.md")
	if e != nil && !os.IsNotExist(e) {
		return result, e
	}
	if approvedHash != "" && contract.HashBytes(before) != approvedHash {
		for _, p := range panes {
			warn(p, "ARTIFACT_CHANGED")
		}
		return result, nil
	}
	id, e := store.NewID()
	if e != nil {
		return result, e
	}
	intent := cleanupIntent{id, append([]PaneBinding{}, panes...), approvedHash, final}
	if e := m.Client.saveExact("state/control/gk-cleanup-"+id+"-intent.json", intent); e != nil {
		return result, e
	}
	for _, pane := range panes {
		if pane.RunID != m.Client.options.RunID || pane.UUID == "" || !sameServer(pane.Server, m.ExpectedServer) {
			warn(pane, "PANE_CONFLICT")
			continue
		}
		b, e := m.Client.options.Store.ReadText(spawnPath(pane.RunID, pane.Role) + "-pane.json")
		if e != nil || !bytes.Equal(b, mustOwnedJSON(pane)) {
			warn(pane, "PANE_CONFLICT")
			continue
		}
		if _, e := m.selected(ctx, "", budget); e != nil {
			warn(pane, "SERVER_CONFLICT")
			continue
		}
		// Public observation can repair a pane label from upstream records.
		// Check its current tmux identity first; a changed label cannot authorize
		// adopting or killing a pane after that repair.
		if e := m.CheckPane(ctx, pane, budget); e != nil {
			var native *contract.Error
			if errors.As(e, &native) && native.Code == "SESSION_LOST" {
				result.Removed = append(result.Removed, pane.UUID)
			} else {
				warn(pane, "PANE_CONFLICT")
			}
			continue
		}
		obs, e := m.Client.FreshCall(ctx, "observe", nil, budget, "observe", pane.UUID)
		if e != nil || obs.ValidationError != "" {
			warn(pane, "UPSTREAM_FAILURE")
			continue
		}
		if !obs.Response.OK {
			code := obs.Response.Errors[0].Code
			if code == "NOT_FOUND" || code == "PANE_DEAD" {
				result.Removed = append(result.Removed, pane.UUID)
				continue
			}
			warn(pane, MapFailure(code, ActionContext{Verb: "observe", Checked: true, Unfinished: true}).Code)
			continue
		}
		d := Map(obs.Response.Data)
		if d["id"] != pane.UUID || d["name"] != pane.Selector || d["agent"] != pane.Provider || d["target"] != pane.Target {
			warn(pane, "PANE_CONFLICT")
			continue
		}
		if d["state"] == "dead" {
			result.Removed = append(result.Removed, pane.UUID)
			continue
		}
		if d["state"] != "idle" {
			warn(pane, "TURN_UNCERTAIN")
			continue
		}
		if e := m.CheckPane(ctx, pane, budget); e != nil {
			warn(pane, "PANE_CONFLICT")
			continue
		}
		key := "vly:kill:" + intentHash(map[string]any{"run": pane.RunID, "pane": pane.UUID, "config": pane.ConfigHash, "approved_hash": approvedHash})
		call, e := m.Client.FreshCall(ctx, "kill", nil, budget, "kill", pane.UUID, "--yes", "--idempotency-key="+key)
		if e != nil || call.ValidationError != "" || call.Missing {
			warn(pane, "UPSTREAM_FAILURE")
			continue
		}
		if !call.Response.OK {
			warn(pane, MapFailure(call.Response.Errors[0].Code, ActionContext{Verb: "kill", Checked: true, Final: true}).Code)
			continue
		}
		d = Map(call.Response.Data)
		if d["id"] != pane.UUID || d["name"] != pane.Selector || d["agent"] != pane.Provider || d["target"] != pane.Target || d["action"] != "kill" || d["dry_run"] != false {
			warn(pane, "PANE_CONFLICT")
			continue
		}
		result.Removed = append(result.Removed, pane.UUID)
	}
	after, e := m.Client.options.Store.ReadText("SPEC.md")
	if e != nil && !os.IsNotExist(e) {
		return result, e
	}
	if !bytes.Equal(before, after) {
		result.Pending = true
		result.DiagnosticCodes = append(result.DiagnosticCodes, "ARTIFACT_CHANGED")
		result.Warnings = append(result.Warnings, Warning{Code: "CLEANUP_PENDING", Message: "Artifact changed during pane cleanup", Details: map[string]any{"expected_hash": contract.HashBytes(before), "observed_hash": contract.HashBytes(after)}})
	}
	if e := m.Client.saveExact("state/control/gk-cleanup-"+id+"-result.json", result); e != nil {
		return result, e
	}
	return result, nil
}

// Execute delegates protected-set comparison to the engine, including failed
// operations. Primitive operations themselves contain no round transition.
func (m *PaneManager) Execute(ctx context.Context, q review.TurnRequest, guard review.GuardedTurnExecutor, operation review.Operation) (review.TurnOutcome, error) {
	if guard == nil {
		return review.TurnOutcome{}, NativeError(decision("INTERNAL", "guarded_executor_is_required"), nil)
	}
	return guard.Execute(ctx, q, operation)
}
