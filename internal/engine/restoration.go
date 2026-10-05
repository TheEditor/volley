//go:build darwin || linux

package engine

import (
	"bytes"
	"context"
	"fmt"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/human"
	"github.com/TheEditor/volley/internal/store"
)

type Restoration struct {
	ID           string                `json:"id"`
	Before       store.FileObservation `json:"before"`
	Candidate    store.FileObservation `json:"candidate"`
	ApprovedHash string                `json:"approved_hash"`
	RejectedHash string                `json:"rejected_hash"`
	InputsHash   string                `json:"inputs_hash"`
	Intent       store.ReceiptRef      `json:"intent"`
}

func (o *Owner) settledHistory() error {
	history, err := o.Store.History()
	if err != nil {
		return err
	}
	intents, results := make(map[string]bool), make(map[string]bool)
	for _, tx := range history {
		if tx.Kind == "intent" {
			var snapshot store.Snapshot
			if err = decode(tx.Next, &snapshot); err != nil {
				return err
			}
			if turn := snapshot.Object("current_turn"); len(turn) > 0 {
				intents[fmt.Sprint(turn["id"])] = true
			}
		}
		if tx.Receipt != nil {
			receipt, _, e := o.Store.ReadRecord("receipt", tx.Receipt.Path)
			if e != nil {
				return e
			}
			completion, _ := receipt["completion"].(map[string]any)
			if completion["kind"] != "completed" || completion["settled"] != true {
				return failure("TURN_UNCERTAIN", "Restoration requires all owned turns to be settled", nil)
			}
			results[fmt.Sprint(receipt["turn_id"])] = true
		}
	}
	for id := range intents {
		if !results[id] {
			return failure("TURN_UNCERTAIN", "Restoration cannot abandon an unsettled intent", map[string]any{"turn_id": id})
		}
	}
	return o.validateHistory()
}

func (o *Owner) restorationEligible(ctx context.Context, m store.Snapshot) (bool, error) {
	a := o.State.Auxiliary
	if a.Original == nil || !a.ClosingDone || !a.Changed || len(m.Object("current_turn")) > 0 {
		return false, nil
	}
	if err := o.settledHistory(); err != nil {
		return false, err
	}
	if err := o.Prepared.Gate.Check(); err != nil {
		return false, err
	}
	basis, err := o.inputBasis(m)
	if err != nil {
		return false, err
	}
	if basis != a.Original.InputsHash || m.String("request_hash") != a.Original.RequestHash {
		return false, nil
	}
	if question := m.Object("question"); len(question) > 0 && question["answered"] != true {
		return false, nil
	}
	qb, _, err := human.StableRead(ctx, o.Store, "QUESTIONS.md", o.Options.Clock)
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(qb)) != 0 {
		return false, nil
	}
	pending, err := o.PendingInputs(ctx)
	return len(pending) == 0, err
}

func (o *Owner) tryRestoration(ctx context.Context, m store.Snapshot) (bool, error) {
	eligible, err := o.restorationEligible(ctx, m)
	if err != nil || !eligible {
		return false, err
	}
	if err = checkBindings(o.Store, o.State); err != nil {
		return false, err
	}
	r := o.State.Auxiliary.PreparedRestoration
	if r == nil {
		if err = o.hook("restoration.rejected.before"); err != nil {
			return false, err
		}
		rejected, before, e := o.Store.ReadObserved("SPEC.md", store.TextLimit)
		if e != nil {
			return false, e
		}
		if before != o.State.Bindings["SPEC.md"] {
			return false, failure("ARTIFACT_CHANGED", "Rejected artifact changed", nil)
		}
		approved, e := o.Store.ReadText("rounds/closing-approved.spec.md")
		if e != nil {
			return false, e
		}
		if contract.HashBytes(approved) != o.State.Auxiliary.Original.SpecHash {
			return false, failure("STATE_INVALID", "Approved snapshot differs", nil)
		}
		id, e := store.NewID()
		if e != nil {
			return false, e
		}
		stage := "state/control/restoration-rejected-" + id + ".md"
		hash, e := o.Store.StageText(stage, rejected)
		if e != nil {
			return false, e
		}
		observed, e := observation(o.Store, stage)
		if e != nil {
			return false, e
		}
		observed.Path = "rounds/closing-rejected.spec.md"
		candidatePath := "state/control/restoration-candidate-" + id + ".md"
		if _, e = o.Store.StagePrivateText(candidatePath, approved); e != nil {
			return false, e
		}
		candidate, e := observation(o.Store, candidatePath)
		if e != nil {
			return false, e
		}
		r = &Restoration{ID: id, Before: before, Candidate: candidate, ApprovedHash: candidate.Hash, RejectedHash: hash, InputsHash: o.State.Auxiliary.Original.InputsHash}
		b, e := contract.Canonical(r)
		if e != nil {
			return false, e
		}
		path := "state/control/restoration-intent-" + id + ".json"
		intentHash, e := o.Store.StagePrivateText(path, b)
		if e != nil {
			return false, e
		}
		r.Intent = store.ReceiptRef{Path: path, Hash: intentHash}
		o.State.Auxiliary.PreparedRestoration = r
		o.State.Bindings[observed.Path] = observed
		if err = saveState(o.Store, m, o.State, "control", []store.Artifact{{StagedPath: stage, TargetPath: observed.Path, Hash: hash}}, nil); err != nil {
			return false, err
		}
		if err = o.hook("restoration.rejected.after"); err != nil {
			return false, err
		}
		m, err = o.snapshot()
		if err != nil {
			return false, err
		}
	}
	if err = o.checkRestoration(*r); err != nil {
		return false, err
	}
	o.State.Auxiliary.PreparedRestoration = nil
	o.State.Auxiliary.Restoration = r
	m.Object("auxiliary")["restoration_intent"] = r.Intent.Path
	if err = o.hook("restoration.intent.before"); err != nil {
		return false, err
	}
	if err = saveState(o.Store, m, o.State, "control", nil, nil); err != nil {
		return false, err
	}
	if err = o.hook("restoration.intent.after"); err != nil {
		return false, err
	}
	return true, nil
}

func (o *Owner) checkRestoration(r Restoration) error {
	if r.ID == "" || r.Intent.Path != "state/control/restoration-intent-"+r.ID+".json" || r.Candidate.Path != "state/control/restoration-candidate-"+r.ID+".md" || r.Before.Path != "SPEC.md" || r.ApprovedHash != r.Candidate.Hash || r.RejectedHash != r.Before.Hash {
		return failure("STATE_INVALID", "Restoration bindings differ", nil)
	}
	b, err := o.Store.ReadText(r.Intent.Path)
	if err != nil {
		return err
	}
	if contract.HashBytes(b) != r.Intent.Hash {
		return failure("STATE_INVALID", "Restoration intent changed", nil)
	}
	var saved Restoration
	if err = decode(b, &saved); err != nil {
		return err
	}
	// The reference binds the payload; the payload has no self-referential hash.
	saved.Intent = r.Intent
	left, _ := contract.Canonical(saved)
	right, _ := contract.Canonical(r)
	if !bytes.Equal(left, right) {
		return failure("STATE_INVALID", "Saved restoration intent differs", nil)
	}
	return nil
}

func (o *Owner) finishRestoration(ctx context.Context, m store.Snapshot) error {
	r := o.State.Auxiliary.Restoration
	if r == nil {
		return failure("STATE_INVALID", "No committed restoration", nil)
	}
	if err := o.checkRestoration(*r); err != nil {
		return err
	}
	bound := o.State
	bound.Bindings = make(map[string]store.FileObservation)
	for path, observed := range o.State.Bindings {
		if path != "SPEC.md" {
			bound.Bindings[path] = observed
		}
	}
	if err := checkBindings(o.Store, bound); err != nil {
		return err
	}
	eligible, err := o.restorationEligible(ctx, m)
	if err != nil {
		return err
	}
	_, current, err := o.Store.ReadObserved("SPEC.md", store.TextLimit)
	if err != nil {
		return err
	}
	installed := r.Candidate
	installed.Path = "SPEC.md"
	if current != r.Before && current != installed {
		return failure("ARTIFACT_CHANGED", "Restoration found an unrelated artifact", nil)
	}
	if !eligible {
		if current == installed {
			if _, err = o.Store.ReplaceForRestoration(r.Candidate, r.Before); err != nil {
				return err
			}
			if err = o.saveRestorationReceipt(m, r, current); err != nil {
				return err
			}
			m.Object("auxiliary")["restoration_receipt"] = o.State.Auxiliary.RestorationReceipt.Path
		}
		// A new decision can block finalization even after the recorded move.
		// Keep that input and the actual bytes, then require a new review boundary.
		o.State.Bindings["SPEC.md"] = current
		m["spec_hash"] = current.Hash
		o.State.Auxiliary.Restoration = nil
		o.State.Auxiliary.Result = "restoration_blocked_inputs_changed"
		m["status"], m["phase"] = "impasse", "critique"
		if err = saveState(o.Store, m, o.State, "control", nil, nil); err != nil {
			return err
		}
		return failure("REVIEW_IMPASSE", "New input blocks restoration approval", nil)
	}
	inbox, err := o.Store.ReadInbox(ctx, m.String("run_id"), 0)
	if err != nil {
		return err
	}
	sequence, head := inbox.Head()
	var after store.FileObservation
	err = o.Store.InboxBoundary(ctx, func(in store.Inbox) error {
		n, h := in.Head()
		if n != sequence || h != head {
			return failure("REVIEW_IMPASSE", "New input blocks restoration", nil)
		}
		// File channels do not use the journal lock. Check them again before the
		// bound atomic move, without a second inbox-lock acquisition.
		for _, path := range []string{"HUMAN.md", "QUESTIONS.md"} {
			b, _, err := o.Store.ReadObserved(path, human.Limit)
			if err != nil {
				return err
			}
			if len(bytes.TrimSpace(b)) != 0 {
				return failure("REVIEW_IMPASSE", "File input blocks restoration", nil)
			}
		}
		var err error
		after, err = o.Store.ReplaceForRestoration(r.Candidate, r.Before)
		return err
	})
	if err != nil {
		return err
	}
	if err = o.saveRestorationReceipt(m, r, after); err != nil {
		return err
	}
	a := &o.State.Auxiliary
	a.Result, a.Restoration = "rejected_at_cap", nil
	o.State.Bindings["SPEC.md"] = after
	o.State.OrdinaryReceipt = &a.Original.Receipt
	m["spec_hash"], m["reviewed_spec_hash"] = a.Original.SpecHash, a.Original.SpecHash
	v := a.Original.Verdict
	m["verdict"] = map[string]any{"value": v.Value, "hash": v.Hash, "line": v.Line, "parser_version": v.ParserVersion}
	m.Object("auxiliary")["restoration_receipt"], m.Object("auxiliary")["rejected_hash"] = a.RestorationReceipt.Path, r.RejectedHash
	m["status"], m["phase"] = "ready", "commit_final"
	return saveState(o.Store, m, o.State, "control", nil, nil)
}

func (o *Owner) saveRestorationReceipt(m store.Snapshot, r *Restoration, after store.FileObservation) error {
	var err error
	a := &o.State.Auxiliary
	if err = o.hook("restoration.receipt.before"); err != nil {
		return err
	}
	receiptBytes, err := contract.Canonical(map[string]any{"record_version": 1, "run_id": m.String("run_id"), "restoration_id": r.ID, "intent": r.Intent, "before": r.Before, "after": after, "approved_hash": r.ApprovedHash, "rejected_hash": r.RejectedHash, "inputs_hash": r.InputsHash, "original_approval": o.State.Auxiliary.Original.Receipt, "rejected_review_evidence": o.State.Auxiliary.RejectedReviews})
	if err != nil {
		return err
	}
	path := "state/control/restoration-receipt-" + r.ID + ".json"
	hash, err := o.Store.StagePrivateText(path, receiptBytes)
	if err != nil {
		return err
	}
	if err = o.hook("restoration.receipt.after"); err != nil {
		return err
	}
	a.RestorationReceipt = &store.ReceiptRef{Path: path, Hash: hash}
	obs, err := observation(o.Store, path)
	if err != nil {
		return err
	}
	o.State.Bindings[path] = obs
	return nil
}
