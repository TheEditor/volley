//go:build darwin || linux

package ops

import (
	"context"
	"github.com/TheEditor/volley/internal/store"
	"time"
)

func (o Options) stop(ctx context.Context, r Request, s *store.Store, m store.Snapshot) (map[string]any, error) {
	if r.DryRun && r.Yes {
		return nil, failure("INVALID_INPUT", "Choose --yes or --dry-run")
	}
	result := data(s, m)
	if receipt := m.Object("stop_control"); len(receipt) > 0 {
		result["control_receipt"] = receipt
	}
	result["dry_run"] = r.DryRun
	result["effects"] = []string{"Request controller stop through the durable inbox", "Retain pane, process and uncertain-turn evidence; do not kill recorded targets"}
	if r.DryRun {
		return result, nil
	}
	if !r.Yes {
		return result, failure("ACK_REQUIRED", "Stop requires --yes")
	}
	if terminal(m.String("status")) {
		return result, nil
	}
	if s.ObserveOwner() == "unknown" {
		return result, failure("LOCKED", "Owner identity is unknown; no stop request was written")
	}
	key := "stop:" + m.String("run_id")
	entry, err := s.SubmitCurrentInput(ctx, store.InputReceipt{RunID: m.String("run_id"), Kind: "control", Key: key, Text: "stop", Channel: "command"}, time.Second)
	if err != nil {
		return result, err
	}
	result["control_receipt"] = map[string]any{"path": entry.ReceiptPath, "sha256": entry.ReceiptHash, "applied": false}
	if s.ObserveOwner() == "absent" {
		if err = s.AcquireOwner(ctx, time.Millisecond); err != nil {
			return result, err
		}
		// The owner can change while the receipt is submitted. Recheck under lock.
		m, err = read(s)
		if err != nil {
			return result, err
		}
		if !terminal(m.String("status")) {
			m["status"] = "stopped"
			m["stop_control"] = map[string]any{"path": entry.ReceiptPath, "sha256": entry.ReceiptHash, "applied": true}
			tx, err := s.NewTransaction("control", m, nil, nil)
			if err != nil {
				return result, err
			}
			if err = s.CommitTransaction(tx); err != nil {
				return result, err
			}
		}
		result = data(s, m)
		result["owner"] = "absent"
		result["dry_run"] = false
		result["effects"] = []string{"Saved stopped state; retained all workspace and target evidence"}
		result["control_receipt"] = m["stop_control"]
		return result, nil
	}
	end := time.Now().Add(5 * time.Second)
	for {
		m, err = read(s)
		if err != nil {
			return result, err
		}
		if receipt := m.Object("stop_control"); receipt["sha256"] == entry.ReceiptHash && receipt["applied"] == true {
			result = data(s, m)
			result["dry_run"] = false
			result["effects"] = []string{"Controller acknowledged stop; retained target evidence"}
			result["control_receipt"] = receipt
			return result, nil
		}
		if time.Now().After(end) {
			return result, failure("WAIT_TIMEOUT", "Stop is submitted but not yet acknowledged; inspect the same run")
		}
		if err = pause(ctx); err != nil {
			return result, err
		}
	}
}
