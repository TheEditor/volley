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
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

type SendIntent struct {
	RunID       string      `json:"run_id"`
	TurnID      string      `json:"turn_id"`
	Pane        PaneBinding `json:"pane"`
	ConfigHash  string      `json:"config_hash"`
	StateDir    string      `json:"state_dir"`
	PromptPath  string      `json:"prompt_path"`
	PromptHash  string      `json:"prompt_hash"`
	Payload     string      `json:"payload"`
	PayloadHash string      `json:"payload_hash"`
	Key         string      `json:"key"`
}
type sendAttempt struct {
	CallID       string `json:"call_id"`
	PlanHash     string `json:"plan_hash"`
	PreviousHash string `json:"previous_hash"`
	Reason       string `json:"reason"`
}

// MissingReceiptRecovery supplies advice only for a source-bound, settled
// missing response. Resume must repeat all binding checks before its call.
func (m *PaneManager) MissingReceiptRecovery(ctx context.Context, plan SendIntent, budget Budget) (bool, error) {
	if !m.Client.SourceChecked() {
		return false, nil
	}
	attempts, err := m.attempts(plan)
	if err != nil || len(attempts) == 0 {
		return false, err
	}
	missing := false
	for i, attempt := range attempts {
		call, exists, err := m.Client.LoadCall(attempt.CallID)
		if err != nil || !exists || !call.Process.Settled {
			return false, err
		}
		if i == len(attempts)-1 {
			missing = call.Missing
		}
	}
	if !missing {
		return false, nil
	}
	if err = m.CheckSend(ctx, plan, budget); err != nil {
		return false, err
	}
	return true, nil
}

func sendBase(turnID string) string { return "state/turns/" + turnID + "/gk-send" }
func intentHash(v any) string       { return contract.HashBytes(mustOwnedJSON(v)) }
func sendKey(plan SendIntent) string {
	return "vly:" + intentHash(map[string]any{"run": plan.RunID, "turn": plan.TurnID, "pane": plan.Pane.UUID, "provider": plan.Pane.Provider, "session": plan.Pane.SessionID, "session_reason": plan.Pane.SessionReason, "config": plan.ConfigHash, "payload": plan.PayloadHash})
}
func (m *PaneManager) PrepareSend(ctx context.Context, turnID string, pane PaneBinding, prompt []byte, budget Budget) (SendIntent, error) {
	var plan SendIntent
	if !validID(turnID) || pane.RunID != m.Client.options.RunID || len(prompt) == 0 || len(prompt) > store.TextLimit || !utf8.Valid(prompt) || bytes.ContainsRune(prompt, 0) {
		return plan, fmt.Errorf("Invalid owned prompt context")
	}
	if e := m.CheckPane(ctx, pane, budget); e != nil {
		return plan, e
	}
	if e := m.Client.options.Store.PrepareTurn(turnID); e != nil {
		return plan, e
	}
	h, e := m.Client.ConfigHash()
	if e != nil {
		return plan, e
	}
	path := "state/prompts/" + pane.RunID + "-" + turnID + ".md"
	pointer, e := pointerPayload(filepath.Join(m.Client.options.Store.Path, path))
	if e != nil {
		return plan, e
	}
	normal := bytes.Trim(pointer, " \t\r\n")
	plan = SendIntent{RunID: pane.RunID, TurnID: turnID, Pane: pane, ConfigHash: h, StateDir: m.Client.stateDir, PromptPath: path, PromptHash: contract.HashBytes(prompt), Payload: string(pointer), PayloadHash: contract.HashBytes(normal)}
	plan.Key = sendKey(plan)
	if e := m.Client.options.Register([]string{path, sendBase(turnID) + "-intent.json"}); e != nil {
		return plan, e
	}
	if old, e := m.Client.options.Store.ReadText(path); e == nil {
		if !bytes.Equal(old, prompt) {
			return plan, NativeError(decision("IDEMPOTENCY_CONFLICT", "logical_prompt_changed"), nil)
		}
	} else if !os.IsNotExist(e) {
		return plan, e
	} else if e := m.Client.writeText(path, prompt); e != nil {
		return plan, e
	}
	if e := m.Client.saveExact(sendBase(turnID)+"-intent.json", plan); e != nil {
		return plan, e
	}
	return plan, nil
}
func (m *PaneManager) CheckSend(ctx context.Context, plan SendIntent, budget Budget) error {
	if plan.RunID != m.Client.options.RunID || !validID(plan.TurnID) || plan.Key != sendKey(plan) || plan.Pane.RunID != plan.RunID || plan.PayloadHash != contract.HashBytes(bytes.Trim([]byte(plan.Payload), " \t\r\n")) || plan.ConfigHash != m.Client.configHash || plan.StateDir != m.Client.stateDir {
		return NativeError(decision("IDEMPOTENCY_CONFLICT", "saved_send_binding_changed"), nil)
	}
	b, e := m.Client.options.Store.ReadText(sendBase(plan.TurnID) + "-intent.json")
	if e != nil {
		return e
	}
	if !bytes.Equal(b, mustOwnedJSON(plan)) {
		return NativeError(decision("IDEMPOTENCY_CONFLICT", "durable_send_intent_changed"), nil)
	}
	b, e = m.Client.options.Store.ReadText(plan.PromptPath)
	if e != nil || contract.HashBytes(b) != plan.PromptHash {
		return NativeError(decision("ARTIFACT_CHANGED", "immutable_prompt_changed"), nil)
	}
	pointer, e := pointerPayload(filepath.Join(m.Client.options.Store.Path, plan.PromptPath))
	if e != nil || string(pointer) != plan.Payload {
		return NativeError(decision("IDEMPOTENCY_CONFLICT", "pointer_path_binding_changed"), nil)
	}
	if e := m.CheckPane(ctx, plan.Pane, budget); e != nil {
		return e
	}
	// Recovery queries the selected frozen mechanism rather than reading a
	// retired variable or Gashki's private ledger.
	call, e := m.Client.FreshCall(ctx, "config get", nil, budget, "config", "get", "state_dir")
	if e != nil {
		return e
	}
	if call.ValidationError != "" || !call.Response.OK || Map(call.Response.Data)["key"] != "state_dir" || String(Map(call.Response.Data)["value"]) != plan.StateDir {
		return NativeError(decision("IDEMPOTENCY_CONFLICT", "queried_state_root_changed"), map[string]any{"call_id": call.ID})
	}
	return nil
}

func (m *PaneManager) attempts(plan SendIntent) ([]sendAttempt, error) {
	a := []sendAttempt{}
	previous := ""
	for n := 1; n <= 10000; n++ {
		path := fmt.Sprintf("%s-attempt-%04d.json", sendBase(plan.TurnID), n)
		b, e := m.Client.options.Store.ReadText(path)
		if os.IsNotExist(e) {
			return a, nil
		}
		if e != nil {
			return a, e
		}
		var attempt sendAttempt
		if e := decodeClosed(b, &attempt); e != nil {
			return a, e
		}
		if !validID(attempt.CallID) || attempt.PlanHash != intentHash(plan) || attempt.PreviousHash != previous {
			return a, NativeError(decision("STATE_INVALID", "send_attempt_chain_changed"), nil)
		}
		a = append(a, attempt)
		previous = contract.HashBytes(b)
	}
	return a, NativeError(decision("TURN_UNCERTAIN", "send_attempt_bound_exceeded"), nil)
}
func (m *PaneManager) nextAttempt(plan SendIntent, prior []sendAttempt, reason string) (sendAttempt, error) {
	id, e := store.NewID()
	if e != nil {
		return sendAttempt{}, e
	}
	previous := ""
	if len(prior) > 0 {
		previous = intentHash(prior[len(prior)-1])
	}
	a := sendAttempt{id, intentHash(plan), previous, reason}
	path := fmt.Sprintf("%s-attempt-%04d.json", sendBase(plan.TurnID), len(prior)+1)
	if e := m.Client.saveExact(path, a); e != nil {
		return a, e
	}
	return a, nil
}
func confirmedSend(r Response, plan SendIntent) (review.DeliveryReceipt, error) {
	d := Map(r.Data)
	receipt := review.DeliveryReceipt{Kind: review.Uncertain, PaneUUID: plan.Pane.UUID, PayloadHash: plan.PayloadHash}
	for _, w := range r.Warnings {
		if w.Code == "SUBMIT_UNVERIFIED" {
			return receipt, NativeError(decision("SEND_UNCERTAIN", "submit_unverified_warning"), nil)
		}
	}
	match := contract.HashBytes(bytes.Trim([]byte(plan.Payload), " \t\r\n"))
	if r.OK && d["submitted"] == true && d["id"] == plan.Pane.UUID && d["name"] == plan.Pane.Selector && d["target"] == plan.Pane.Target && d["payload_sha256"] == plan.PayloadHash && d["match_sha256"] == match {
		cursor, e := cursorSequence(String(d["turn_cursor"]))
		barrier, be := cursorSequence(String(d["barrier_cursor"]))
		if e == nil && be == nil && cursor > barrier {
			receipt.Kind = review.WithCursor
			receipt.Cursor = String(d["turn_cursor"])
			receipt.Replayed, _ = d["replayed"].(bool)
			return receipt, nil
		}
	}
	return receipt, NativeError(decision("SEND_UNCERTAIN", "send_confirmation_not_bound"), nil)
}
func cursorSequence(cursor string) (int64, error) {
	if !strings.HasPrefix(cursor, "e1.") {
		return 0, fmt.Errorf("Unknown pinned cursor format")
	}
	number := strings.TrimPrefix(cursor, "e1.")
	n, e := strconv.ParseInt(number, 10, 64)
	if e != nil || n < 0 || strconv.FormatInt(n, 10) != number {
		return 0, fmt.Errorf("Invalid pinned cursor")
	}
	return n, nil
}

func (m *PaneManager) classifySend(call CheckedCall, plan SendIntent, budget Budget) (review.DeliveryReceipt, Decision, error) {
	receipt := review.DeliveryReceipt{Kind: review.Uncertain, PaneUUID: plan.Pane.UUID, PayloadHash: plan.PayloadHash}
	if call.ValidationError != "" || call.Missing {
		d := decision("SEND_UNCERTAIN", "send_response_unchecked")
		return receipt, d, NativeError(d, map[string]any{"call_id": call.ID})
	}
	if call.Intent.Verb != "send" || call.Intent.InputHash != contract.HashBytes([]byte(plan.Payload)) || !containsArg(call.Intent.Args, plan.Pane.UUID) || !containsArg(call.Intent.Args, "--idempotency-key="+plan.Key) {
		d := decision("IDEMPOTENCY_CONFLICT", "send_call_binding_differs")
		return receipt, d, NativeError(d, nil)
	}
	if call.Response.OK {
		r, e := confirmedSend(call.Response, plan)
		if e != nil {
			return r, decision("SEND_UNCERTAIN", "send_confirmation_not_bound"), e
		}
		return r, Decision{Retain: true}, nil
	}
	u := call.Response.Errors[0]
	noPaste := NoPasteRefusal(call.Response, "send", plan.Pane.UUID, m.Client.sourceChecked)
	if barrier := String(Map(u.Evidence)["barrier_cursor"]); barrier != "" {
		receipt.Cursor = barrier
	}
	left, finite := budget.Remaining()
	d := MapFailure(u.Code, ActionContext{Verb: "send", Checked: true, MayHaveSent: true, NoPaste: noPaste, BudgetExpired: finite && left <= 0})
	return receipt, d, NativeError(d, map[string]any{"call_id": call.ID, "upstream": u})
}
func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
func (m *PaneManager) submit(ctx context.Context, plan SendIntent, a sendAttempt, budget Budget) (CheckedCall, error) {
	return m.Client.Call(ctx, CallRequest{ID: a.CallID, Verb: "send", Args: []string{"send", plan.Pane.UUID, "--from-stdin", "--idempotency-key=" + plan.Key}, Input: []byte(plan.Payload), Budget: budget})
}

// Deliver performs only this logical send. A malformed nonempty response is
// terminal handover evidence. An empty lost response gets one checked public
// same-key recovery in this invocation, with every prior subprocess settled.
func (m *PaneManager) Deliver(ctx context.Context, plan SendIntent, budget Budget) (review.DeliveryReceipt, error) {
	var receipt review.DeliveryReceipt
	if e := m.CheckSend(ctx, plan, budget); e != nil {
		return receipt, e
	}
	path := sendBase(plan.TurnID) + "-delivery.json"
	if b, e := m.Client.options.Store.ReadText(path); e == nil {
		if e := decodeClosed(b, &receipt); e != nil {
			return receipt, e
		}
		if receipt.PaneUUID != plan.Pane.UUID || receipt.PayloadHash != plan.PayloadHash || receipt.Kind != review.WithCursor {
			return receipt, NativeError(decision("STATE_INVALID", "saved_delivery_binding_changed"), nil)
		}
		if e := m.validateSavedDelivery(plan, receipt); e != nil {
			return receipt, e
		}
		return receipt, nil
	} else if !os.IsNotExist(e) {
		return receipt, e
	}
	attempts, e := m.attempts(plan)
	if e != nil {
		return receipt, e
	}
	resumed := len(attempts) > 0
	recovered := false
	for {
		if ctx.Err() != nil {
			return receipt, NativeError(decision("SEND_UNCERTAIN", "send_interrupted"), nil)
		}
		left, finite := budget.Remaining()
		if finite && left <= 0 {
			return receipt, NativeError(decision("TURN_TIMEOUT", "turn_budget_exhausted"), nil)
		}
		var a sendAttempt
		if len(attempts) == 0 {
			a, e = m.nextAttempt(plan, attempts, "initial")
			if e != nil {
				return receipt, e
			}
			attempts = append(attempts, a)
		} else {
			a = attempts[len(attempts)-1]
		}
		call, exists, e := m.Client.LoadCall(a.CallID)
		if e != nil {
			return receipt, NativeError(decision("SEND_UNCERTAIN", "prior_send_subprocess_unsettled"), map[string]any{"call_id": a.CallID})
		}
		if !exists {
			call, e = m.submit(ctx, plan, a, budget)
			if e != nil {
				return receipt, e
			}
		}
		if call.Missing {
			if recovered || !call.Process.Settled || !m.Client.sourceChecked {
				return receipt, NativeError(decision("SEND_UNCERTAIN", "lost_send_recovery_unverified"), map[string]any{"call_id": a.CallID})
			}
			if e := m.CheckSend(ctx, plan, budget); e != nil {
				return receipt, e
			}
			for _, previous := range attempts {
				old, exists, e := m.Client.LoadCall(previous.CallID)
				if e != nil || exists && !old.Process.Settled {
					return receipt, NativeError(decision("SEND_UNCERTAIN", "prior_send_subprocess_unsettled"), nil)
				}
			}
			a, e = m.nextAttempt(plan, attempts, "same_key_missing_receipt_recovery")
			if e != nil {
				return receipt, e
			}
			attempts = append(attempts, a)
			recovered = true
			continue
		}
		if resumed && exists && !recovered && call.ValidationError == "" && call.Response.OK {
			if _, err := confirmedSend(call.Response, plan); err != nil {
				return receipt, err
			}
			if !call.Process.Settled || !m.Client.sourceChecked {
				return receipt, NativeError(decision("SEND_UNCERTAIN", "resume_confirmation_recovery_unverified"), nil)
			}
			if e := m.CheckSend(ctx, plan, budget); e != nil {
				return receipt, e
			}
			a, e = m.nextAttempt(plan, attempts, "same_key_resume_before_confirmation_save")
			if e != nil {
				return receipt, e
			}
			attempts = append(attempts, a)
			recovered = true
			continue
		}
		receipt, d, e := m.classifySend(call, plan, budget)
		if receipt.Kind == review.WithCursor && e == nil {
			if e := m.Client.saveExact(path, receipt); e != nil {
				return receipt, e
			}
			return receipt, nil
		}
		if !d.Continue {
			return receipt, e
		}
		if e := m.waitSafeIdle(ctx, plan, budget); e != nil {
			return receipt, e
		}
		if e := m.CheckSend(ctx, plan, budget); e != nil {
			return receipt, e
		}
		a, e = m.nextAttempt(plan, attempts, "checked_no_paste_refusal_retry")
		if e != nil {
			return receipt, e
		}
		attempts = append(attempts, a)
	}
}

func (m *PaneManager) validateSavedDelivery(plan SendIntent, receipt review.DeliveryReceipt) error {
	attempts, e := m.attempts(plan)
	if e != nil || len(attempts) == 0 {
		return NativeError(decision("STATE_INVALID", "saved_delivery_has_no_call"), nil)
	}
	call, exists, e := m.Client.LoadCall(attempts[len(attempts)-1].CallID)
	if e != nil {
		return e
	}
	if !exists || call.ValidationError != "" || !call.Response.OK || call.Intent.Verb != "send" || call.Intent.InputHash != contract.HashBytes([]byte(plan.Payload)) || !containsArg(call.Intent.Args, plan.Pane.UUID) || !containsArg(call.Intent.Args, "--idempotency-key="+plan.Key) {
		return NativeError(decision("STATE_INVALID", "saved_delivery_call_binding_changed"), nil)
	}
	checked, e := confirmedSend(call.Response, plan)
	if e != nil || !bytes.Equal(mustOwnedJSON(checked), mustOwnedJSON(receipt)) {
		return NativeError(decision("STATE_INVALID", "saved_delivery_confirmation_changed"), nil)
	}
	return nil
}

func (m *PaneManager) validateSavedCompletion(plan SendIntent, delivery review.DeliveryReceipt, saved review.CompletionReceipt) error {
	if len(saved.EvidencePaths) == 0 || len(saved.EvidencePaths)%2 != 0 {
		return NativeError(decision("STATE_INVALID", "completion_call_records_missing"), nil)
	}
	var last CheckedCall
	for i := 0; i < len(saved.EvidencePaths); i += 2 {
		path := saved.EvidencePaths[i]
		id := strings.TrimSuffix(strings.TrimPrefix(path, "state/control/gk-call-"), "-stdout.raw")
		paths := callPaths(id)
		if !validID(id) || path != paths[2] || saved.EvidencePaths[i+1] != paths[3] {
			return NativeError(decision("STATE_INVALID", "completion_call_path_changed"), nil)
		}
		call, exists, e := m.Client.LoadCall(id)
		if e != nil {
			return e
		}
		if !exists || call.ValidationError != "" || call.Intent.Verb != "wait" || !containsArg(call.Intent.Args, plan.Pane.UUID) || !containsArg(call.Intent.Args, "--until=idle") || !containsArg(call.Intent.Args, "--since="+delivery.Cursor) {
			return NativeError(decision("STATE_INVALID", "completion_call_binding_changed"), nil)
		}
		last = call
	}
	d := Map(last.Response.Data)
	event := Map(d["event"])
	seq, e := cursorSequence(saved.Cursor)
	if e != nil || !last.Response.OK || d["cursor"] != saved.Cursor || d["id"] != plan.Pane.UUID || d["name"] != plan.Pane.Selector || d["until"] != "idle" || d["state"] != "idle" || event["kind"] != "turn_ended" || event["source"] != "hook" || Integer(event["seq"]) != seq {
		return NativeError(decision("STATE_INVALID", "completion_evidence_changed"), nil)
	}
	return nil
}

func (m *PaneManager) waitSafeIdle(ctx context.Context, plan SendIntent, budget Budget) error {
	for {
		chunk, e := budget.WaitChunk()
		if e != nil {
			return e
		}
		call, e := m.Client.FreshCall(ctx, "wait", nil, budget, "wait", plan.Pane.UUID, "--until=idle", "--wait-timeout="+chunk.String())
		if e != nil {
			return e
		}
		if call.ValidationError != "" {
			return NativeError(decision("TURN_UNCERTAIN", "safe_idle_wait_unchecked"), nil)
		}
		if call.Response.OK {
			d := Map(call.Response.Data)
			if d["id"] != plan.Pane.UUID || d["name"] != plan.Pane.Selector || d["state"] != "idle" {
				return NativeError(decision("TURN_UNCERTAIN", "safe_idle_binding_changed"), nil)
			}
			return nil
		}
		if call.Response.Errors[0].Code != "WAIT_TIMEOUT" {
			return NativeError(MapFailure(call.Response.Errors[0].Code, ActionContext{Verb: "wait", Checked: true, Unfinished: true}), map[string]any{"upstream": call.Response.Errors[0]})
		}
		if e := m.checkedWorking(ctx, plan.Pane, budget); e != nil {
			return e
		}
	}
}
func (m *PaneManager) checkedWorking(ctx context.Context, pane PaneBinding, budget Budget) error {
	if e := m.CheckPane(ctx, pane, budget); e != nil {
		return e
	}
	left, finite := budget.Remaining()
	if finite && left < time.Second {
		return NativeError(decision("TURN_TIMEOUT", "turn_budget_exhausted"), nil)
	}
	call, e := m.Client.FreshCall(ctx, "observe", nil, budget, "observe", pane.UUID)
	if e != nil {
		return e
	}
	if call.ValidationError != "" {
		return NativeError(decision("TURN_UNCERTAIN", "continuation_observation_unchecked"), nil)
	}
	if !call.Response.OK {
		return NativeError(MapFailure(call.Response.Errors[0].Code, ActionContext{Verb: "observe", Checked: true, Unfinished: true}), map[string]any{"upstream": call.Response.Errors[0]})
	}
	d := Map(call.Response.Data)
	if d["id"] != pane.UUID || d["agent"] != pane.Provider || d["name"] != pane.Selector || d["target"] != pane.Target || d["state"] != "working" && d["state"] != "compacting" {
		return NativeError(decision("TURN_UNCERTAIN", "unlimited_wait_has_no_working_evidence"), nil)
	}
	return nil
}

func (m *PaneManager) AwaitCompletion(ctx context.Context, plan SendIntent, delivery review.DeliveryReceipt, budget Budget) (review.CompletionReceipt, error) {
	result := review.CompletionReceipt{Kind: review.Uncertain, Cursor: delivery.Cursor, Exit: -1, EvidencePaths: []string{}}
	if delivery.Kind != review.WithCursor || delivery.PaneUUID != plan.Pane.UUID || delivery.PayloadHash != plan.PayloadHash {
		return result, NativeError(decision("STATE_INVALID", "completion_delivery_binding_changed"), nil)
	}
	since, e := cursorSequence(delivery.Cursor)
	if e != nil {
		return result, NativeError(decision("TURN_UNCERTAIN", "saved_turn_cursor_invalid"), nil)
	}
	if e := m.CheckSend(ctx, plan, budget); e != nil {
		return result, e
	}
	binding, e := m.Client.options.Store.ReadText(sendBase(plan.TurnID) + "-delivery.json")
	if e != nil || !bytes.Equal(binding, mustOwnedJSON(delivery)) {
		return result, NativeError(decision("STATE_INVALID", "completion_saved_delivery_changed"), nil)
	}
	if e := m.validateSavedDelivery(plan, delivery); e != nil {
		return result, e
	}
	if b, e := m.Client.options.Store.ReadText(sendBase(plan.TurnID) + "-completion.json"); e == nil {
		var saved review.CompletionReceipt
		if e := decodeClosed(b, &saved); e != nil {
			return result, e
		}
		seq, e := cursorSequence(saved.Cursor)
		if e != nil || seq <= since || saved.Kind != review.Completed || !saved.Settled || saved.Exit != 0 || len(saved.EvidencePaths) == 0 {
			return result, NativeError(decision("STATE_INVALID", "saved_completion_binding_changed"), nil)
		}
		if e := m.validateSavedCompletion(plan, delivery, saved); e != nil {
			return result, e
		}
		return saved, nil
	} else if !os.IsNotExist(e) {
		return result, e
	}
	for {
		chunk, e := budget.WaitChunk()
		if e != nil {
			return result, e
		}
		if e := m.CheckPane(ctx, plan.Pane, budget); e != nil {
			return result, e
		}
		call, e := m.Client.FreshCall(ctx, "wait", nil, budget, "wait", plan.Pane.UUID, "--until=idle", "--since="+delivery.Cursor, "--wait-timeout="+chunk.String())
		if e != nil {
			return result, e
		}
		result.EvidencePaths = append(result.EvidencePaths, call.RawPath, call.StderrPath)
		if call.ValidationError != "" {
			return result, NativeError(decision("TURN_UNCERTAIN", "completion_response_unchecked"), map[string]any{"call_id": call.ID})
		}
		if call.Response.OK {
			d := Map(call.Response.Data)
			event := Map(d["event"])
			sequence, err := cursorSequence(String(d["cursor"]))
			if err != nil || sequence <= since || Integer(event["seq"]) != sequence || event["kind"] != "turn_ended" || event["source"] != "hook" || d["id"] != plan.Pane.UUID || d["name"] != plan.Pane.Selector || d["until"] != "idle" || d["state"] != "idle" {
				return result, NativeError(decision("TURN_UNCERTAIN", "qualified_completion_not_proved"), map[string]any{"call_id": call.ID})
			}
			result.Kind = review.Completed
			result.Settled = true
			result.Exit = 0
			result.Cursor = String(d["cursor"])
			if e := m.Client.saveExact(sendBase(plan.TurnID)+"-completion.json", result); e != nil {
				return result, e
			}
			return result, nil
		}
		u := call.Response.Errors[0]
		if u.Code != "WAIT_TIMEOUT" {
			return result, NativeError(MapFailure(u.Code, ActionContext{Verb: "wait", Checked: true, Unfinished: true}), map[string]any{"call_id": call.ID, "upstream": u})
		}
		ev := Map(u.Evidence)
		if ev["id"] != plan.Pane.UUID || ev["since"] != delivery.Cursor {
			return result, NativeError(decision("TURN_UNCERTAIN", "wait_timeout_binding_changed"), nil)
		}
		if e := m.checkedWorking(ctx, plan.Pane, budget); e != nil {
			return result, e
		}
	}
}
