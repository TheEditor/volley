//go:build darwin || linux

package engine

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/gashki"
	"github.com/TheEditor/volley/internal/store"
)

func (o *Owner) cleanupGashki(ctx context.Context, m store.Snapshot) error {
	panes := []gashki.PaneBinding{}
	for _, role := range []string{"planner", "critic", "second"} {
		b, err := o.Store.ReadText("state/control/gk-spawn-" + o.State.RunID + "-" + role + "-pane.json")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var pane gashki.PaneBinding
		if err = decode(b, &pane); err != nil {
			return err
		}
		panes = append(panes, pane)
	}
	// The outer cleanup intent is durable before the first primitive kill.
	id, err := store.NewID()
	if err != nil {
		return err
	}
	path := "state/control/engine-cleanup-" + id + ".json"
	b, err := contract.Canonical(panes)
	if err != nil {
		return err
	}
	hash, err := o.Store.StagePrivateText(path, b)
	if err != nil {
		return err
	}
	m.Object("cleanup")["intent"] = map[string]any{"path": path, "sha256": hash}
	m.Object("cleanup")["pending"] = true
	if err = saveState(o.Store, m, o.State, "control", nil, nil); err != nil {
		return err
	}
	o.Gashki.cleanup = true
	defer func() { o.Gashki.cleanup = false }()
	approvedHash := ""
	final := m.String("status") == "approved"
	if final {
		approvedHash = m.String("spec_hash")
	}
	result, err := o.Gashki.manager.Cleanup(ctx, panes, final, approvedHash, gashki.Budget{Limit: 30 * time.Second, Started: time.Now()})
	if err != nil {
		return err
	}
	resultPath := "state/control/engine-cleanup-" + id + "-result.json"
	b, err = contract.Canonical(result)
	if err != nil {
		return err
	}
	resultHash, err := o.Store.StagePrivateText(resultPath, b)
	if err != nil {
		return err
	}
	m, err = o.snapshot()
	if err != nil {
		return err
	}
	m["retention"] = map[string]any{"kind": "durable-evidence", "reason": "Keep review records; owned idle pane cleanup checked", "panes": result.Retained, "processes": []any{}}
	if !final {
		m.Object("retention")["kind"] = "retain"
		m.Object("retention")["reason"] = "Keep resumable review panes and durable evidence"
	}
	m.Object("cleanup")["pending"] = result.Pending
	m.Object("cleanup")["actions"] = result.Removed
	m.Object("cleanup")["result"] = map[string]any{"path": resultPath, "sha256": resultHash}
	for _, w := range result.Warnings {
		o.State.Warnings = append(o.State.Warnings, contract.Warning{Code: w.Code, Message: w.Message, Evidence: map[string]any{"path": resultPath, "reason": strings.Join(result.DiagnosticCodes, ", ")}})
	}
	return saveState(o.Store, m, o.State, "control", nil, nil)
}
