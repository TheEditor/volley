//go:build darwin || linux

package engine

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/human"
	"github.com/TheEditor/volley/internal/prompt"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

type CompletedProof struct {
	RecordVersion int                `json:"record_version"`
	RequestHash   string             `json:"request_hash"`
	Guard         GuardProof         `json:"guard"`
	Outcome       review.TurnOutcome `json:"outcome"`
}

func (o *Owner) directives(m store.Snapshot, role string) ([]prompt.Directive, []string, error) {
	var directives []prompt.Directive
	var ids []string
	entries, _ := m["applications"].([]any)
	for _, entry := range entries {
		d := entry.(map[string]any)
		if d[role+"_delivered"] == true {
			continue
		}
		if role == "critic" && d["planner_delivered"] != true {
			return nil, nil, failure("TURN_UNCERTAIN", "Directive has not reached the planner", nil)
		}
		id := fmt.Sprint(d["receipt_hash"])
		a, err := human.LoadApplication(o.Store, id)
		if err != nil {
			return nil, nil, err
		}
		answer, err := o.Store.ReadText(a.AnswerPath)
		if err != nil {
			return nil, nil, err
		}
		question := ""
		if a.QuestionPath != "" {
			b, e := o.Store.ReadText(a.QuestionPath)
			if e != nil {
				return nil, nil, e
			}
			question = string(b)
		}
		prior, err := o.Store.ReadText(a.PriorPath)
		if err != nil {
			return nil, nil, err
		}
		if len(prior) > 0 {
			directives = append(directives, prompt.Directive{ID: id + "/prior", Answer: string(prior)})
		}
		if a.Withdrawn {
			answer = []byte("The user withdrew this question. Remove it from the open question file. Retain any proposed choice as a proposal. Withdrawal does not approve the recommended choice or remove a binding requirement.")
		}
		directives = append(directives, prompt.Directive{ID: id, Question: question, Answer: string(answer)})
		ids = append(ids, id)
		if role == "planner" {
			break
		} // one fixed logical application turn per receipt
	}
	return directives, ids, nil
}

func (o *Owner) turn(ctx context.Context, m store.Snapshot) error {
	if err := checkContextBinding(o.State.Settings.ContextDir, o.State.Context); err != nil {
		return err
	}
	phase := review.Phase(m.String("phase"))
	role, purpose := "planner", prompt.Draft
	switch phase {
	case review.Draft:
	case review.ApplyDirective:
		purpose = prompt.ApplyDirective
	case review.Revise:
		purpose = prompt.Revision
	case review.Critique:
		role = "critic"
		purpose = prompt.Critique
		if o.State.Auxiliary.ConfirmationPending {
			purpose = prompt.Confirmation
			m["phase"] = string(review.ConfirmClosing)
		}
	case review.ConfirmClosing:
		role, purpose = "critic", prompt.Confirmation
	case review.SecondOpinion:
		role, purpose = "critic", prompt.Advisory
	case review.Closing:
		purpose = prompt.Closing
	case review.CritiqueRetry:
		role = "critic"
		purpose = prompt.Reminder
	default:
		return failure("STATE_INVALID", "No ordinary turn for the saved phase", nil)
	}
	id, err := store.NewID()
	if err != nil {
		return err
	}
	directives, inputs, err := o.directives(m, role)
	if err != nil {
		return err
	}
	if purpose == prompt.ApplyDirective {
		if len(inputs) == 0 {
			m["phase"] = "critique"
			return saveState(o.Store, m, o.State, "control", nil, nil)
		}
		a, err := human.LoadApplication(o.Store, inputs[0])
		if err != nil {
			return err
		}
		id = a.TurnID
	} else if role == "planner" {
		directives = nil
		inputs = nil
	}
	if purpose == prompt.Advisory {
		directives, inputs = nil, nil
	}
	provider := fmt.Sprint(m.Object("roles")[role])
	constraints, err := o.Store.ReadText("CONSTRAINTS.md")
	if err != nil && o.State.Bindings["CONSTRAINTS.md"].Kind != "absent" {
		return err
	}
	attempt := 1
	if purpose == prompt.Reminder {
		attempt = 2
	}
	advisoryPath := ""
	if purpose == prompt.Closing && o.State.Auxiliary.AdvisoryDone {
		advisoryPath = filepath.Join(o.Store.Path, "rounds/second-opinion.md")
	}
	rendered, err := prompt.Render(prompt.Request{Purpose: purpose, Role: role, Provider: provider, Backend: o.State.Settings.Backend, Round: number(m["round"]), AttemptID: id, PreviousAttemptID: o.State.LastTurn, Constraints: string(constraints), ContextPath: o.State.Settings.ContextDir, Directives: directives, Rubric: o.State.Settings.Rubric, SecondOpinionPath: advisoryPath, ApprovedReviewPath: filepath.Join(o.Store.Path, o.State.LastCritique)})
	if err != nil {
		return err
	}
	if purpose == prompt.Revision && m.Object("verdict")["value"] == "MISSING" {
		rendered.Text = append(rendered.Text, []byte("\n\nMISSING verdict diagnostic: neither critic attempt supplied a valid final verdict. Revise conservatively from the actual saved critiques. Latest critique: "+o.State.LastCritique+". A missing verdict is not a received REVISE or approval.\n")...)
		rendered.RenderedHash = contract.HashBytes(rendered.Text)
	}
	promptPath := "state/prompts/" + id + ".md"
	if _, err = o.Store.StagePrivateText(promptPath, rendered.Text); err != nil {
		return err
	}
	timeout, err := time.ParseDuration(o.State.Settings.WaitTimeout)
	if err != nil {
		return err
	}
	q := review.TurnRequest{RunID: m.String("run_id"), TurnID: id, Purpose: string(purpose), Role: role, Provider: provider, Round: number(m["round"]), Attempt: attempt, Workspace: o.Store.Path, Prompt: rendered.Text, PromptHash: rendered.RenderedHash, SpecBeforeHash: m.String("spec_hash"), Timeout: timeout}
	if o.State.Settings.Persistent && purpose != prompt.Advisory {
		session, _ := m.Object("sessions")[role].(map[string]any)
		q.SessionID, _ = session["observed_id"].(string)
		if q.SessionID == "" {
			q.SessionID, _ = session["intended_id"].(string)
		}
	}
	q.ExpectedArtifacts = []string{"SPEC.md"}
	if role == "critic" {
		q.ExpectedArtifacts = []string{critiquePath(q)}
		if purpose == prompt.Advisory {
			q.ExpectedArtifacts = []string{"rounds/second-opinion.md"}
		}
	}
	p, err := o.Adapter.Prepare(ctx, q)
	if err != nil {
		return err
	}
	requestBytes, err := contract.Canonical(q)
	if err != nil {
		return err
	}
	requestPath := "state/turns/" + id + "/engine-request.json"
	requestHash, err := o.Store.StagePrivateText(requestPath, requestBytes)
	if err != nil {
		return err
	}
	m["status"] = "running"
	m["current_turn"] = map[string]any{"id": id, "purpose": q.Purpose, "role": role, "round": q.Round, "attempt": attempt, "intent_hash": requestHash, "prompt_path": promptPath, "prompt_hash": q.PromptHash, "operation": "intent_committed", "delivery_uncertain": false, "receipt_path": "", "cursor": "", "started_at": time.Now().UTC().Format(time.RFC3339Nano), "budget": timeout.String(), "remaining": timeout.String()}
	m["prompt_hashes"] = map[string]any{"template": rendered.SourceBundleHash, "rendered": rendered.RenderedHash}
	artifacts, err := o.prepareAuxiliaryIntent(m, q)
	if err != nil {
		return err
	}
	if err = saveState(o.Store, m, o.State, "intent", artifacts, nil); err != nil {
		return err
	}
	if purpose == prompt.Closing {
		if err = o.hook("closing.approved.after"); err != nil {
			return err
		}
	}
	if err = o.Store.ReadyIntent(m.String("last_transaction_id")); err != nil {
		return err
	}
	if err = o.hook("engine.intent.committed"); err != nil {
		return err
	}
	out, err := o.Adapter.Execute(ctx, p, o.Guard)
	if ctx.Err() != nil {
		return o.interrupted(ctx)
	}
	if err != nil {
		if out.Delivery.Kind == review.NotStarted && o.Guard.Proof != nil {
			path := "state/turns/" + id + "/direct-result.json"
			result, _, readErr := o.Store.ReadRaw(path)
			if readErr == nil {
				proof := NoStartProof{RecordVersion: 1, TurnID: id, RequestHash: requestHash, ResultHash: contract.HashBytes(result), Guard: *o.Guard.Proof}
				b, e := contract.Canonical(proof)
				if e != nil {
					return e
				}
				proofPath := "state/turns/" + id + "/no-start-proof.json"
				hash, e := o.Store.StagePrivateText(proofPath, b)
				if e != nil {
					return e
				}
				m["no_start"] = map[string]any{"path": proofPath, "sha256": hash}
			}
		}
		return o.handover(m, err)
	}
	if out.Kind != review.Completed || !out.Completion.Settled || o.Guard.Proof == nil {
		return o.handover(m, failure("TURN_UNCERTAIN", "Turn lacks checked completion", nil))
	}
	if err = o.hook("engine.turn.checked"); err != nil {
		return err
	}
	proof := CompletedProof{1, requestHash, *o.Guard.Proof, out}
	proofBytes, err := contract.Canonical(proof)
	if err != nil {
		return err
	}
	proofPath := "state/turns/" + id + "/guard-result.json"
	proofHash, err := o.Store.StagePrivateText(proofPath, proofBytes)
	if err != nil {
		return err
	}
	record, err := o.receipt(q, out, inputs, proofPath, proofHash)
	if err != nil {
		return err
	}
	ref, err := o.Store.SaveTurnReceipt(id, record)
	if err != nil {
		return err
	}
	if err = o.hook("engine.receipt.saved"); err != nil {
		return err
	}
	return o.finishTurn(ctx, m, q, out, ref)
}

func critiquePath(q review.TurnRequest) string {
	if q.Purpose == "reminder" {
		return fmt.Sprintf("rounds/r%02d.critique-retry.md", q.Round)
	}
	return fmt.Sprintf("rounds/r%02d.critique.md", q.Round)
}

func obsRecord(o store.FileObservation) map[string]any {
	return map[string]any{"path": o.Path, "sha256": o.Hash, "bytes": o.Bytes, "type": o.Kind, "device": o.Device, "inode": o.Inode}
}

func (o *Owner) receipt(q review.TurnRequest, out review.TurnOutcome, inputs []string, proofPath, proofHash string) (map[string]any, error) {
	artifacts := []any{}
	for path, hash := range out.ArtifactHashes {
		if q.Role == "critic" {
			path = out.Reply.Path
		}
		observed, err := observation(o.Store, path)
		if err != nil {
			return nil, err
		}
		if observed.Hash != hash {
			return nil, failure("ARTIFACT_CHANGED", "Turn artifact changed before receipt", map[string]any{"path": path})
		}
		artifacts = append(artifacts, obsRecord(observed))
	}
	if inputs == nil {
		inputs = []string{}
	}
	return map[string]any{"record_version": 1, "run_id": q.RunID, "turn_id": q.TurnID, "purpose": q.Purpose, "role": q.Role, "round": q.Round, "prompt_hash": q.PromptHash, "spec_before_hash": q.SpecBeforeHash,
		"delivery":   map[string]any{"kind": string(out.Delivery.Kind), "cursor": out.Delivery.Cursor, "payload_hash": out.Delivery.PayloadHash, "pane_uuid": out.Delivery.PaneUUID, "replayed": out.Delivery.Replayed},
		"completion": map[string]any{"kind": string(out.Kind), "exit": out.Completion.Exit, "signal": "", "deadline": false, "settled": out.Completion.Settled, "cursor": out.Completion.Cursor, "source": "owned-direct-process"},
		"artifacts":  artifacts, "identity": map[string]any{"session_id": out.Completion.SessionID, "pane_uuid": "", "reason": "owned process completion"}, "reply": out.Reply.Record(),
		"retention":         map[string]any{"kind": "retain", "reason": "Keep owned evidence", "panes": []string{}, "processes": []any{}},
		"evidence":          map[string]any{"stdout_path": "state/turns/" + q.TurnID + "/stdout.pending", "stderr_path": "state/turns/" + q.TurnID + "/stderr.pending", "protocol_path": "state/turns/" + q.TurnID + "/direct-result.json", "upstream_code": "", "upstream_exit": out.Completion.Exit},
		"input_receipt_ids": inputs, "guard": map[string]any{"path": proofPath, "sha256": proofHash}}, nil
}

func (o *Owner) finishTurn(ctx context.Context, m store.Snapshot, q review.TurnRequest, out review.TurnOutcome, ref store.ReceiptRef) error {
	for _, code := range out.Warnings {
		exists := false
		for _, saved := range o.State.Warnings {
			if saved.Code == code {
				exists = true
				break
			}
		}
		if !exists {
			o.State.Warnings = append(o.State.Warnings, contract.Warning{Code: code, Message: code, Evidence: map[string]any{"turn_id": q.TurnID, "path": ref.Path, "reason": "Checked turn evidence records this limit"}})
		}
	}
	if err := oCheckReply(o.Store, out.Reply); err != nil {
		return err
	}
	var artifacts []store.Artifact
	add := func(target string, b []byte) error {
		path := "state/turns/" + q.TurnID + "/promote-" + filepath.Base(target)
		hash, err := o.Store.StageText(path, b)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, store.Artifact{StagedPath: path, TargetPath: target, Hash: hash})
		obs, err := observation(o.Store, path)
		if err != nil {
			return err
		}
		obs.Path = target
		o.State.Bindings[target] = obs
		return nil
	}
	verdict := ""
	questions := false
	if q.Purpose == "advisory" {
		b, err := o.Store.ReadText(out.Reply.Path)
		if err != nil {
			return err
		}
		if err = add("rounds/second-opinion.md", b); err != nil {
			return err
		}
		a := &o.State.Auxiliary
		a.AdvisoryDone, a.AdvisoryHash, a.AdvisorySpecHash, a.AdvisoryReceipt = true, contract.HashBytes(b), q.SpecBeforeHash, &ref
		m.Object("auxiliary")["second_opinion"] = "completed"
	} else if q.Role == "critic" {
		b, err := o.Store.ReadText(out.Reply.Path)
		if err != nil {
			return err
		}
		v := review.ParseVerdict(string(b))
		v.Hash = contract.HashBytes(b)
		verdict = v.Value
		m["verdict"] = map[string]any{"value": v.Value, "hash": v.Hash, "line": v.Line, "parser_version": v.ParserVersion}
		m["reviewed_spec_hash"] = q.SpecBeforeHash
		if err = add(critiquePath(q), b); err != nil {
			return err
		}
		o.State.LastCritique = critiquePath(q)
		o.State.OrdinaryReceipt = &ref
		if verdict == "APPROVE" && o.State.Auxiliary.ClosingDone && o.State.Auxiliary.Changed {
			o.State.Auxiliary.Result = "confirmed"
		}
		if a := &o.State.Auxiliary; a.ClosingDone && a.Changed {
			a.RejectedReviews = append(a.RejectedReviews, RejectedReview{Path: critiquePath(q), Hash: v.Hash, SpecHash: q.SpecBeforeHash, Verdict: v.Value, Round: q.Round, Receipt: ref})
		}
		if o.State.Auxiliary.ConfirmationPending && (verdict != "MISSING" || q.Purpose == "reminder") {
			o.State.Auxiliary.ConfirmationPending = false
			m.Object("auxiliary")["confirmation"] = verdict
		}
		if q.Purpose == "reminder" && verdict == "MISSING" {
			if err = add(fmt.Sprintf("rounds/r%02d.verdict-missing.md", q.Round), []byte("MISSING: Both critic attempts lack a valid final verdict. Use the actual saved critiques for a conservative revision. This is not a received REVISE or approval.\n")); err != nil {
				return err
			}
		}
		if verdict != "MISSING" || q.Purpose == "reminder" {
			m["completed_rounds"] = q.Round
		}
	} else {
		b, obs, err := o.Store.ReadObserved("SPEC.md", store.TextLimit)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(b)) == 0 || obs.Hash != out.ArtifactHashes["SPEC.md"] {
			return failure("ARTIFACT_CHANGED", "Planner result differs from checked artifact", nil)
		}
		o.State.Bindings["SPEC.md"] = obs
		m["spec_hash"] = obs.Hash
		if a := &o.State.Auxiliary; a.ClosingDone && a.Original != nil && obs.Hash != a.Original.SpecHash {
			a.Changed = true
		}
		if q.Purpose == "draft" || q.Purpose == "revision" {
			if err = add(fmt.Sprintf("rounds/r%02d.spec.md", q.Round), b); err != nil {
				return err
			}
		}
		if q.Purpose == "closing" {
			if err = add("rounds/closing.spec.md", b); err != nil {
				return err
			}
			a := &o.State.Auxiliary
			a.ClosingDone, a.Changed = true, obs.Hash != q.SpecBeforeHash
			a.ConfirmationPending = a.Changed
			a.Result = "unchanged"
			if a.Changed {
				a.Result = "confirmation_pending"
			}
			m.Object("auxiliary")["closing"] = "completed"
		}
		if out.Reply.Kind == "found" {
			reply, err := o.Store.ReadText(out.Reply.Path)
			if err != nil {
				return err
			}
			target := fmt.Sprintf("rounds/r%02d.response.md", q.Round)
			if q.Purpose == "directive" {
				target = "rounds/input-" + q.TurnID + ".response.md"
			}
			if q.Purpose == "closing" {
				target = "rounds/closing-response.md"
			}
			if err = add(target, reply); err != nil {
				return err
			}
		}
		if out.Reply.Kind != "found" {
			o.State.Auxiliary.Warnings = append(o.State.Auxiliary.Warnings, "Planner reply unavailable: "+out.Reply.Reason)
		}
		qb, qobs, err := human.StableRead(ctx, o.Store, "QUESTIONS.md", o.Options.Clock)
		if err != nil {
			return err
		}
		questions = len(bytes.TrimSpace(qb)) > 0
		o.State.QuestionObserved = qobs
		if q.Purpose == "closing" && questions {
			o.State.Auxiliary.ConfirmationPending = true
		}
	}
	var next review.Snapshot
	var err error
	if q.Purpose == "advisory" || q.Purpose == "closing" {
		next, err = review.AfterAuxiliary(pure(m), o.State.Auxiliary.Changed, questions)
	} else {
		next, err = review.AfterTurn(pure(m), verdict, questions)
	}
	if err != nil {
		return err
	}
	m["status"] = string(next.Status)
	m["phase"] = string(next.Phase)
	m["round"] = next.Round
	m["current_turn"] = nil
	if next.Status == review.AwaitingAnswer {
		// The generation is a separate durable effect. Record the need before
		// it starts so a crash cannot leave an unbound answer gate.
		o.State.QuestionPending = true
		m["status"] = "ready"
		m["phase"] = "critique"
	}
	o.State.LastTurn = q.TurnID
	o.State.LastReceipt = &ref
	if o.State.Settings.Persistent && out.Completion.SessionID != "" && q.Purpose != "advisory" {
		session, _ := m.Object("sessions")[q.Role].(map[string]any)
		session["observed_id"] = out.Completion.SessionID
	}
	if err = saveState(o.Store, m, o.State, "result", artifacts, &ref); err != nil {
		return err
	}
	if err = o.hook("engine.result.committed"); err != nil {
		return err
	}
	if err = o.repairDeliveries(); err != nil {
		return err
	}
	if next.Status == review.Impasse {
		return o.commitImpasse()
	}
	return nil
}

func oCheckReply(s *store.Store, reply review.Reply) error {
	if reply.Kind != "found" {
		return nil
	}
	b, err := s.ReadText(reply.Path)
	if err != nil {
		return err
	}
	if contract.HashBytes(b) != reply.Hash {
		return failure("ARTIFACT_CHANGED", "Checked reply changed", nil)
	}
	return nil
}

type checkedTurn struct {
	Request review.TurnRequest
	Outcome review.TurnOutcome
	Receipt store.ReceiptRef
}

func (o *Owner) checkTurn(ctx context.Context, m store.Snapshot, extraPaths ...string) (result checkedTurn, err error) {
	err = func() error {
		turn := m.Object("current_turn")
		id := fmt.Sprint(turn["id"])
		r, err := o.Store.Recover()
		if err != nil {
			return err
		}
		if r.Receipt == nil {
			return failure("TURN_UNCERTAIN", "Saved direct intent has no checked receipt; do not repeat it", nil)
		}
		record, _, err := o.Store.ReadRecord("receipt", r.Receipt.Path)
		if err != nil {
			return err
		}
		completion, _ := record["completion"].(map[string]any)
		if completion["kind"] != "completed" || completion["settled"] != true {
			return failure("TURN_UNCERTAIN", "Saved receipt is not complete", nil)
		}
		guard, _ := record["guard"].(map[string]any)
		path, _ := guard["path"].(string)
		if path != "state/turns/"+id+"/guard-result.json" {
			return failure("STATE_INVALID", "Guard proof binding differs", nil)
		}
		b, err := o.Store.ReadText(path)
		if err != nil {
			return err
		}
		if contract.HashBytes(b) != guard["sha256"] {
			return failure("STATE_INVALID", "Guard proof hash differs", nil)
		}
		var proof CompletedProof
		if err = decode(b, &proof); err != nil {
			return err
		}
		requestPath := "state/turns/" + id + "/engine-request.json"
		requestBytes, err := o.Store.ReadText(requestPath)
		if err != nil {
			return err
		}
		if contract.HashBytes(requestBytes) != turn["intent_hash"] || proof.RequestHash != turn["intent_hash"] {
			return failure("STATE_INVALID", "Recovery intent hash differs", nil)
		}
		var q review.TurnRequest
		if err = decode(requestBytes, &q); err != nil {
			return err
		}
		if q.RunID != m.String("run_id") || q.TurnID != id || q.Purpose != turn["purpose"] || q.Role != turn["role"] || q.PromptHash != turn["prompt_hash"] || q.SpecBeforeHash != m.String("spec_hash") {
			return failure("STATE_INVALID", "Recovery request binding differs", nil)
		}
		changes := append([]store.AuthorizedChange{}, proof.Guard.Changes...)
		for _, extra := range append([]string{path, r.Receipt.Path}, extraPaths...) {
			obs, err := observation(o.Store, extra)
			if err != nil {
				return err
			}
			obs.Path = filepath.Join(o.Store.Path, extra)
			changes = append(changes, store.AuthorizedChange{Path: extra, Before: proof.Guard.Before.Files[extra], After: obs, Kind: "checked receipt publication"})
		}
		checkpoint, err := checkpointChanges(o.Store, proof.Guard.Before)
		if err != nil {
			return err
		}
		changes = append(changes, checkpoint...)
		mutation, err := o.Store.Compare(context.WithoutCancel(ctx), proof.Guard.Before, changes)
		if err != nil {
			return err
		}
		if mutation != nil {
			return failure("ARTIFACT_CHANGED", "Protected evidence changed after the checked turn", map[string]any{"actor": "external_or_unknown", "paths": mutation.Paths})
		}
		if proof.Outcome.Kind != review.Completed || !proof.Outcome.Completion.Settled {
			return failure("STATE_INVALID", "Recovery proof is not a checked completion", nil)
		}
		result = checkedTurn{q, proof.Outcome, *r.Receipt}
		return nil

	}()
	return result, err
}
func (o *Owner) recoverTurn(ctx context.Context, m store.Snapshot) error {
	checked, err := o.checkTurn(ctx, m)
	if err != nil {
		return o.handover(m, err)
	}
	return o.finishTurn(ctx, m, checked.Request, checked.Outcome, checked.Receipt)
}

func (o *Owner) repairDeliveries() error {
	if o.State.LastReceipt == nil {
		return nil
	}
	record, _, err := o.Store.ReadRecord("receipt", o.State.LastReceipt.Path)
	if err != nil {
		return err
	}
	role := fmt.Sprint(record["role"])
	m, err := o.snapshot()
	if err != nil {
		return err
	}
	for _, id := range stringsOf(record["input_receipt_ids"]) {
		delivered := false
		entries, _ := m["applications"].([]any)
		for _, entry := range entries {
			d := entry.(map[string]any)
			if d["receipt_hash"] == id && d[role+"_delivered"] == true {
				delivered = true
			}
		}
		if delivered {
			continue
		}
		a, err := human.LoadApplication(o.Store, id)
		if err != nil {
			return err
		}
		if err = human.RecordDelivery(o.Store, a, role, *o.State.LastReceipt); err != nil {
			return err
		}
	}
	return nil
}

func (o *Owner) commitImpasse() error {
	m, err := o.snapshot()
	if err != nil {
		return err
	}
	q, _, err := o.Store.ReadObserved("QUESTIONS.md", human.Limit)
	if err != nil {
		return err
	}
	text := []byte("No reviewed approval remains within the round cap. The final revision is retained and is not approved. Resume with a higher round cap.\n\nOpen questions (exact bytes):\n" + string(q))
	path := fmt.Sprintf("state/control/impasse-%08d.md", m.Revision()+1)
	hash, err := o.Store.StageText(path, text)
	if err != nil {
		return err
	}
	obs, err := observation(o.Store, "state/IMPASSE.md")
	if err != nil {
		return err
	}
	a := store.Artifact{StagedPath: path, TargetPath: "state/IMPASSE.md", Hash: hash}
	if obs.Kind != "absent" {
		a.Replace = true
		a.Before = &obs
	}
	m["status"] = "impasse"
	m["phase"] = "critique"
	return saveState(o.Store, m, o.State, "control", []store.Artifact{a}, nil)
}
