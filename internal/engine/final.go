//go:build darwin || linux

package engine

import (
	"bytes"
	"context"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/human"
	"github.com/TheEditor/volley/internal/store"
)

func (o *Owner) validateFinal(m store.Snapshot) error {
	a := m.Object("approval")
	if a["spec_hash"] != m["spec_hash"] || a["reviewed_hash"] != m["spec_hash"] || m.String("spec_hash") == "" || a["basis"] != "ordinary-critic" {
		return failure("STATE_INVALID", "Final approval basis differs", nil)
	}
	path, _ := a["receipt_path"].(string)
	receipt, _, err := o.Store.ReadRecord("receipt", path)
	if err != nil {
		return err
	}
	completion, _ := receipt["completion"].(map[string]any)
	if receipt["role"] != "critic" || receipt["spec_before_hash"] != m["spec_hash"] || completion["kind"] != "completed" || completion["settled"] != true {
		return failure("STATE_INVALID", "Final critic receipt is not a checked approval", nil)
	}
	if err := o.validateHistory(); err != nil {
		return err
	}
	return checkBindings(o.Store, o.State)
}

func (o *Owner) validateHistory() error {
	history, err := o.Store.History()
	if err != nil {
		return err
	}
	for _, tx := range history {
		if tx.Receipt == nil {
			continue
		}
		r, _, e := o.Store.ReadRecord("receipt", tx.Receipt.Path)
		if e != nil {
			return e
		}
		guard, _ := r["guard"].(map[string]any)
		path, _ := guard["path"].(string)
		b, e := o.Store.ReadText(path)
		if e != nil {
			return e
		}
		if contract.HashBytes(b) != guard["sha256"] {
			return failure("STATE_INVALID", "Saved guard proof changed", nil)
		}
		var proof CompletedProof
		if e = decode(b, &proof); e != nil {
			return e
		}
		for _, change := range proof.Guard.Changes {
			obs, e := observation(o.Store, change.Path)
			if e != nil {
				return e
			}
			obs.Path = change.After.Path
			if obs != change.After {
				return failure("ARTIFACT_CHANGED", "Saved turn evidence changed", map[string]any{"path": change.Path})
			}
		}
		if e = oCheckReply(o.Store, proof.Outcome.Reply); e != nil {
			return e
		}
	}
	return nil
}

func (o *Owner) finalize(ctx context.Context, m store.Snapshot) error {
	if err := o.hook("engine.final.before"); err != nil {
		return err
	}
	if err := o.consumeInputs(ctx, m); err != nil {
		return err
	}
	m, err := o.snapshot()
	if err != nil {
		return err
	}
	if m.String("phase") != "commit_final" {
		return nil
	}
	question, _, err := human.StableRead(ctx, o.Store, "QUESTIONS.md", o.Options.Clock)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(question)) != 0 {
		if err = o.commitImpasse(); err != nil {
			return err
		}
		return failure("REVIEW_IMPASSE", "An open question blocks final approval", nil)
	}
	if scheduled, err := o.scheduleAuxiliary(m); err != nil {
		return err
	} else if scheduled {
		return nil
	}
	if err = checkBindings(o.Store, o.State); err != nil {
		return err
	}
	if m.String("spec_hash") != m.String("reviewed_spec_hash") || m.Object("verdict")["value"] != "APPROVE" || o.ordinaryReceipt() == nil {
		return failure("STATE_INVALID", "Approval does not review the current spec", nil)
	}
	inputsHash, err := o.inputBasis(m)
	if err != nil {
		return err
	}
	inbox, err := o.Store.ReadInbox(ctx, m.String("run_id"), 0)
	if err != nil {
		return err
	}
	sequence, head := inbox.Head()
	changed := false
	err = o.Store.InboxBoundary(ctx, func(current store.Inbox) error {
		n, h := current.Head()
		if n != sequence || h != head {
			changed = true
			return nil
		}
		m["approval"] = map[string]any{"spec_hash": m["spec_hash"], "reviewed_hash": m["reviewed_spec_hash"], "inputs_hash": inputsHash, "receipt_path": o.ordinaryReceipt().Path, "basis": "ordinary-critic", "meaning": approvalMeaning}
		m["status"] = "approved"
		m["phase"] = "cleanup"
		return saveState(o.Store, m, o.State, "final", nil, nil)
	})
	if err != nil {
		return err
	}
	if changed {
		return nil
	}
	return o.hook("engine.final.committed")
}

func (o *Owner) PendingInputs(ctx context.Context) ([]string, error) {
	m, err := o.snapshot()
	if err != nil {
		return nil, err
	}
	inbox, err := o.Store.ReadInbox(ctx, m.String("run_id"), 0)
	if err != nil {
		return nil, err
	}
	used := make(map[string]bool)
	for _, id := range append(stringsOf(m["answers"]), stringsOf(m["steering"])...) {
		used[id] = true
	}
	var pending []string
	for _, entry := range inbox.Entries {
		if !used[entry.ReceiptHash] {
			pending = append(pending, entry.ReceiptHash)
		}
	}
	for _, path := range []string{"HUMAN.md", "QUESTIONS.md"} {
		b, obs, err := o.Store.ReadObserved(path, 1<<20)
		if err != nil {
			return nil, err
		}
		if obs.Kind == "file" && len(bytes.TrimSpace(b)) > 0 {
			prefix := "legacy-file:"
			if path == "QUESTIONS.md" {
				prefix = "legacy-question-file:"
			}
			pending = append(pending, prefix+obs.Hash)
		}
	}
	return pending, nil
}
