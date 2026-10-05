//go:build darwin || linux

package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/gashki"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

func (o *Owner) recoverGashkiTurn(ctx context.Context, m store.Snapshot) error {
	turn := m.Object("current_turn")
	id := fmt.Sprint(turn["id"])
	ref, _ := turn["recovery_guard"].(map[string]any)
	path, _ := ref["path"].(string)
	if !recoveryGuardPath(id, path) {
		return o.handover(m, failure("TURN_UNCERTAIN", "Pane turn has no checkpoint-bound delivery guard; inspect retained evidence", nil))
	}
	b, err := o.Store.ReadText(path)
	if err != nil || contract.HashBytes(b) != ref["sha256"] {
		return o.handover(m, failure("STATE_INVALID", "Pane delivery guard changed", nil))
	}
	var proof GuardProof
	if err = decode(b, &proof); err != nil {
		return o.handover(m, err)
	}
	b, err = o.Store.ReadText("state/turns/" + id + "/engine-request.json")
	if err != nil || contract.HashBytes(b) != turn["intent_hash"] {
		return o.handover(m, failure("STATE_INVALID", "Pane request binding differs", nil))
	}
	var q review.TurnRequest
	if err = decode(b, &q); err != nil {
		return o.handover(m, err)
	}
	if q.TurnID != id || q.RunID != m.String("run_id") || q.Purpose != turn["purpose"] || q.Role != turn["role"] || q.PromptHash != turn["prompt_hash"] || q.SpecBeforeHash != m.String("spec_hash") || proof.Before.RunID != q.RunID || proof.Before.Actor != q.Role {
		return o.handover(m, failure("STATE_INVALID", "Pane recovery identity differs", nil))
	}
	b, err = o.Store.ReadText("state/turns/" + id + "/gk-send-intent.json")
	if err != nil {
		return o.handover(m, err)
	}
	var send gashki.SendIntent
	if err = decode(b, &send); err != nil {
		return o.handover(m, err)
	}
	role := q.Role
	if q.Purpose == "advisory" {
		role = "second"
	}
	if send.TurnID != id || send.RunID != q.RunID || send.PromptHash != q.PromptHash || send.Pane.Provider != q.Provider || send.Pane.Role != role || send.Pane.UUID != turn["pane_uuid"] {
		return o.handover(m, failure("STATE_INVALID", "Pane send identity differs", nil))
	}
	missing := turn["cursor"] == ""
	if missing && (!o.Gashki.manager.Client.SourceChecked() || turn["operation"] != "send_prepared" || m.Object("recovery")["code"] != "SEND_UNCERTAIN" || m.Object("recovery")["missing_receipt"] != true || m.Object("recovery")["bindings_verified"] != true) {
		return o.handover(m, failure("SEND_UNCERTAIN", "Missing pane receipt has no checked same-key recovery facts", nil))
	}
	changes := immutableCheckpointChanges(proof.Changes)
	extra, err := checkpointChanges(o.Store, proof.Before)
	if err != nil {
		return o.handover(m, err)
	}
	changes = append(changes, extra...)
	// Only this turn's new output may have changed while its pane was working.
	if q.Role == "critic" {
		for _, path := range q.ExpectedArtifacts {
			obs, err := o.Store.Observe(path)
			if err != nil {
				continue
			}
			obs.Path = filepath.Join(o.Store.Path, path)
			changes = append(changes, store.AuthorizedChange{Path: path, Before: proof.Before.Files[path], After: obs, Kind: "agent:checked Gashki artifact"})
		}
	}
	mutation, err := o.Store.Compare(context.WithoutCancel(ctx), proof.Before, changes)
	if mutation != nil {
		code := "PLANNER_MUTATION"
		if q.Role == "critic" {
			code = "CRITIC_MUTATION"
		}
		return o.handover(m, failure(code, "Protected files changed during pane handover", map[string]any{"actor": q.Role, "paths": mutation.Paths}))
	}
	if err != nil {
		return o.handover(m, err)
	}
	b, err = o.Store.ReadText("state/turns/" + id + "/gk-prepared.json")
	if err != nil {
		return o.handover(m, err)
	}
	var prepared review.PreparedTurn
	if err = decode(b, &prepared); err != nil {
		return o.handover(m, err)
	}
	qb, _ := contract.Canonical(q)
	pb, _ := contract.Canonical(prepared.Request)
	if string(qb) != string(pb) {
		return o.handover(m, failure("STATE_INVALID", "Prepared pane request differs", nil))
	}
	remaining, err := time.ParseDuration(fmt.Sprint(turn["remaining"]))
	if err != nil || remaining < 0 || q.Timeout > 0 && remaining > q.Timeout {
		return o.handover(m, failure("STATE_INVALID", "Saved pane budget differs", nil))
	}
	o.Gashki.resume = &send
	o.Gashki.resumeMissing = missing
	o.Gashki.resumeElapsed = q.Timeout - remaining
	defer func() { o.Gashki.resume = nil; o.Gashki.resumeElapsed = 0; o.Gashki.resumeMissing = false }()
	out, err := o.Guard.Execute(ctx, q, func(ctx context.Context) (review.TurnOutcome, error) { return o.Gashki.Perform(ctx, prepared) })
	if err != nil {
		return o.handover(m, err)
	}
	if out.Kind != review.Completed || !out.Completion.Settled || o.Guard.Proof == nil {
		return o.handover(m, failure("TURN_UNCERTAIN", "Pane recovery has no checked completion", nil))
	}
	if !missing && out.Delivery.Cursor != turn["cursor"] {
		return o.handover(m, failure("STATE_INVALID", "Recovered pane cursor differs", nil))
	}
	// Preserve the first inventory and exact controller deltas across both owners.
	changes = immutableCheckpointChanges(changes)
	for _, change := range o.Guard.Proof.Changes {
		change.Before = proof.Before.Files[change.Path]
		changes = append(changes, change)
	}
	o.Guard.Proof = &GuardProof{Before: proof.Before, Changes: changes}
	checked := CompletedProof{RecordVersion: 1, RequestHash: fmt.Sprint(turn["intent_hash"]), Guard: *o.Guard.Proof, Outcome: out}
	b, err = contract.Canonical(checked)
	if err != nil {
		return err
	}
	guardPath := "state/turns/" + id + "/guard-result.json"
	hash, err := o.Store.StagePrivateText(guardPath, b)
	if err != nil {
		return err
	}
	_, inputs, err := o.directives(m, q.Role)
	if err != nil {
		return err
	}
	if q.Purpose == "advisory" {
		inputs = nil
	}
	record, err := o.receipt(q, out, inputs, guardPath, hash)
	if err != nil {
		return err
	}
	receipt, err := o.Store.SaveTurnReceipt(id, record)
	if err != nil {
		return err
	}
	m, err = o.snapshot()
	if err != nil {
		return err
	}
	return o.finishTurn(ctx, m, q, out, receipt)
}

// A settled failed wait can publish its exact controller deltas before the
// handover commit. An abrupt death without this proof stays conservative.
func (o *Owner) saveGashkiHandoverGuard(m store.Snapshot) error {
	if o.Gashki == nil || o.Guard.Proof == nil {
		return nil
	}
	turn := m.Object("current_turn")
	id, _ := turn["id"].(string)
	if id == "" || turn["recovery_guard"] == nil || o.Guard.Proof.Before.Files["state/turns/"+id+"/engine-request.json"].Hash != turn["intent_hash"] {
		return nil
	}
	b, err := contract.Canonical(o.Guard.Proof)
	if err != nil {
		return err
	}
	checkpoint, err := store.NewID()
	if err != nil {
		return err
	}
	path := "state/turns/" + id + "/gk-handover-" + checkpoint + ".json"
	hash, err := o.Store.StagePrivateText(path, b)
	if err != nil {
		return err
	}
	turn["recovery_guard"] = map[string]any{"path": path, "sha256": hash}
	m["recovery"] = map[string]any{"code": "SEND_UNCERTAIN", "missing_receipt": o.Gashki.missingSend, "bindings_verified": o.Gashki.missingSend}
	if o.Gashki.budgetTurn == id {
		if left, finite := o.Gashki.budget.Remaining(); finite {
			if left < 0 {
				left = 0
			}
			turn["remaining"] = left.String()
		}
	}
	return nil
}

func (o *Owner) clearGashkiRecovery(m store.Snapshot) error {
	if o.State.RecordVersion != 1 || o.State.Settings.Backend != "gashki" || o.State.RunID != m.String("run_id") || m.Object("recovery")["bindings_verified"] != true {
		return nil
	}
	m["recovery"] = map[string]any{"code": "SEND_UNCERTAIN", "missing_receipt": false, "bindings_verified": false}
	return saveState(o.Store, m, o.State, "control", nil, nil)
}

func recoveryGuardPath(turn, path string) bool {
	base := "state/turns/" + turn + "/"
	if path == base+"gk-recovery-guard.json" || path == base+"gk-send-guard.json" {
		return true
	}
	prefix := base + "gk-handover-"
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), ".json")
	return strings.HasPrefix(path, prefix) && strings.HasSuffix(path, ".json") && len(id) == 26 && strings.Trim(id, "abcdefghijklmnopqrstuvwxyz234567") == ""
}
