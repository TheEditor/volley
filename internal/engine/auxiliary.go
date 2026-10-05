//go:build darwin || linux

package engine

import (
	"fmt"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

const approvalMeaning = "No material objection remains under the recorded review contract for these exact bytes. This does not validate premises, approve implementation, or prove runtime behavior."

type ApprovalBasis struct {
	SpecHash    string           `json:"spec_hash"`
	Receipt     store.ReceiptRef `json:"receipt"`
	Verdict     review.Verdict   `json:"verdict"`
	Round       int              `json:"round"`
	InputsHash  string           `json:"inputs_hash"`
	RequestHash string           `json:"request_hash"`
}
type RejectedReview struct {
	Path     string           `json:"path"`
	Hash     string           `json:"sha256"`
	SpecHash string           `json:"reviewed_spec_hash"`
	Verdict  string           `json:"verdict"`
	Round    int              `json:"round"`
	Receipt  store.ReceiptRef `json:"receipt"`
}
type Auxiliary struct {
	AdvisoryStarted     bool              `json:"advisory_started"`
	AdvisoryDone        bool              `json:"advisory_done"`
	ClosingStarted      bool              `json:"closing_started"`
	ClosingDone         bool              `json:"closing_done"`
	ConfirmationPending bool              `json:"confirmation_pending"`
	Changed             bool              `json:"changed"`
	Result              string            `json:"result"`
	AdvisoryHash        string            `json:"advisory_hash"`
	AdvisorySpecHash    string            `json:"advisory_spec_hash"`
	AdvisoryReceipt     *store.ReceiptRef `json:"advisory_receipt"`
	Original            *ApprovalBasis    `json:"original"`
	RejectedReviews     []RejectedReview  `json:"rejected_reviews"`
	PreparedRestoration *Restoration      `json:"prepared_restoration"`
	Restoration         *Restoration      `json:"restoration"`
	RestorationReceipt  *store.ReceiptRef `json:"restoration_receipt"`
	Warnings            []string          `json:"warnings"`
}

func (o *Owner) inputBasis(m store.Snapshot) (string, error) {
	request, err := o.requestHash()
	if err != nil {
		return "", err
	}
	b, err := contract.Canonical(map[string]any{"request_hash": request, "applications": m["applications"], "brief": o.State.Bindings["BRIEF.md"], "constraints": o.State.Bindings["CONSTRAINTS.md"], "prompt_set_hash": o.State.PromptSetHash})
	return contract.HashBytes(b), err
}

func (o *Owner) ordinaryReceipt() *store.ReceiptRef {
	if o.State.OrdinaryReceipt != nil {
		return o.State.OrdinaryReceipt
	}
	// This permits saved ordinary runs from the first runnable slice.
	return o.State.LastReceipt
}

func (o *Owner) scheduleAuxiliary(m store.Snapshot) (bool, error) {
	a := &o.State.Auxiliary
	manifest := m.Object("auxiliary")
	if o.State.Settings.SecondOpinion && !a.AdvisoryStarted {
		m["phase"] = string(review.SecondOpinion)
		return true, saveState(o.Store, m, o.State, "control", nil, nil)
	}
	if !o.State.Settings.SecondOpinion {
		manifest["second_opinion"] = "disabled"
	}
	if !o.State.Settings.ClosingPass {
		manifest["closing"] = "disabled"
		a.Result = "disabled"
	} else if !a.ClosingStarted {
		if number(m["completed_rounds"]) >= number(m["max_rounds"]) {
			manifest["closing"], manifest["skip_reason"] = "skipped", "no_round_left"
			a.Result = "skipped_no_round_left"
		} else {
			m["phase"] = string(review.Closing)
			return true, saveState(o.Store, m, o.State, "control", nil, nil)
		}
	}
	return false, nil
}

// Closing's approved snapshot is promoted in the same transaction as its
// model intent. There is no saved snapshot that can imply agent completion.
func (o *Owner) prepareAuxiliaryIntent(m store.Snapshot, q review.TurnRequest) ([]store.Artifact, error) {
	a := &o.State.Auxiliary
	manifest := m.Object("auxiliary")
	if q.Purpose == "advisory" {
		if a.AdvisoryStarted {
			return nil, failure("STATE_INVALID", "Advisory turn already started", nil)
		}
		a.AdvisoryStarted = true
		manifest["second_opinion"] = "started"
		return nil, nil
	}
	if q.Purpose != "closing" {
		return nil, nil
	}
	if a.ClosingStarted || number(m["completed_rounds"]) >= number(m["max_rounds"]) {
		return nil, failure("STATE_INVALID", "Closing turn is not eligible", nil)
	}
	ref := o.ordinaryReceipt()
	if ref == nil || m.Object("verdict")["value"] != "APPROVE" || m.String("reviewed_spec_hash") != q.SpecBeforeHash {
		return nil, failure("STATE_INVALID", "Closing lacks an ordinary approval", nil)
	}
	basis, err := o.inputBasis(m)
	if err != nil {
		return nil, err
	}
	b, obs, err := o.Store.ReadObserved("SPEC.md", store.TextLimit)
	if err != nil || obs != o.State.Bindings["SPEC.md"] || obs.Hash != q.SpecBeforeHash {
		return nil, failure("ARTIFACT_CHANGED", "Closing approved bytes changed", nil)
	}
	if err = o.hook("closing.approved.before"); err != nil {
		return nil, err
	}
	stage := "state/turns/" + q.TurnID + "/closing-approved.spec.md"
	hash, err := o.Store.StageText(stage, b)
	if err != nil {
		return nil, err
	}
	observed, err := observation(o.Store, stage)
	if err != nil {
		return nil, err
	}
	observed.Path = "rounds/closing-approved.spec.md"
	o.State.Bindings[observed.Path] = observed
	v := m.Object("verdict")
	a.Original = &ApprovalBasis{SpecHash: hash, Receipt: *ref, Verdict: review.Verdict{Value: "APPROVE", Hash: fmt.Sprint(v["hash"]), Line: number(v["line"]), ParserVersion: number(v["parser_version"])}, Round: number(m["completed_rounds"]), InputsHash: basis, RequestHash: m.String("request_hash")}
	a.ClosingStarted = true
	manifest["closing"], manifest["approved_hash"] = "started", hash
	return []store.Artifact{{StagedPath: stage, TargetPath: observed.Path, Hash: hash}}, nil
}

func (o *Owner) auxiliaryData(m store.Snapshot) map[string]any {
	a := o.State.Auxiliary
	result := map[string]any{"meaning": approvalMeaning, "basis": m["approval"], "ordinary_verdict": m["verdict"], "auxiliary": m["auxiliary"], "closing_result": a.Result, "advisory": nil, "rejected_review_evidence": []RejectedReview{}, "warnings": a.Warnings}
	if a.Warnings == nil {
		result["warnings"] = []string{}
	}
	if a.AdvisoryDone {
		result["advisory"] = map[string]any{"path": "rounds/second-opinion.md", "sha256": a.AdvisoryHash, "reviewed_spec_hash": a.AdvisorySpecHash, "receipt": a.AdvisoryReceipt, "scope": "advisory; ordinary verdict unchanged"}
	}
	if a.Result == "rejected_at_cap" {
		result["rejected_review_evidence"] = a.RejectedReviews
		result["rejected_review_meaning"] = "These critiques reviewed rejected changes. They did not review the restored final artifact. Restoration does not claim that their objections were resolved."
	}
	return result
}
