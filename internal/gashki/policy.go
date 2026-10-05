package gashki

import (
	"fmt"
	"time"

	"github.com/TheEditor/volley/internal/contract"
)

type ActionContext struct {
	Verb                                      string
	Checked, Unfinished, MayHaveSent, NoPaste bool
	Final, OwnedAbsent, BudgetExpired         bool
}
type Decision struct {
	Code           string
	Exit           int
	Reason         string
	Retain         bool
	NoNewAction    bool
	Continue       bool
	CleanupPending bool
}

func decision(code, reason string) Decision {
	d := Decision{Code: code, Reason: reason, Retain: true}
	if code != "" {
		r, e := contract.Load()
		if e != nil {
			d.Code = "INTERNAL"
			d.Exit = 6
			return d
		}
		decl, ok := r.Find(code)
		if !ok {
			d.Code = "INTERNAL"
			d.Exit = 6
			return d
		}
		d.Exit = decl.Exit
	}
	return d
}

// MapFailure uses checked action context, never the upstream retryable flag.
// A cleanup diagnostic cannot replace a previously recorded final verdict.
func MapFailure(code string, c ActionContext) Decision {
	if !c.Checked {
		d := decision("UPSTREAM_FAILURE", "unchecked_upstream_evidence")
		if c.Verb == "send" && c.MayHaveSent {
			d = decision("SEND_UNCERTAIN", "unchecked_send_evidence")
		}
		if c.Final && c.Verb == "kill" {
			d.CleanupPending = true
		}
		return d
	}
	var d Decision
	switch code {
	case "UNKNOWN_FLAG", "UNKNOWN_COMMAND", "INVALID_INPUT", "MISSING_REQUIRED":
		d = decision("UPSTREAM_FAILURE", "generated_call_refused")
		d.NoNewAction = true
	case "INVALID_CONFIG":
		d = decision("INVALID_CONFIG", "frozen_config_read_failed")
		d.NoNewAction = true
	case "TMUX_NO_SERVER", "TMUX_TOO_OLD", "AGENT_CLI_MISSING":
		d = decision("DEPENDENCY_MISSING", "missing_execution_prerequisite")
		d.NoNewAction = !c.Unfinished
	case "STATE_DIR_UNWRITABLE":
		if c.Verb == "spawn" || c.Verb == "send" || c.Verb == "kill" {
			d = decision("UPSTREAM_FAILURE", "mutation_outcome_unknown")
		} else {
			d = decision("DEPENDENCY_MISSING", "state_prerequisite_unavailable")
			d.NoNewAction = !c.Unfinished
		}
	case "AGENT_NOT_LOGGED_IN", "CODEX_HOOKS_UNTRUSTED", "LAUNCH_PROMPT_UNHANDLED":
		d = decision("UPSTREAM_FAILURE", "agent_prerequisite_failed")
	case "HERE_UNAVAILABLE", "CONFLICT":
		d = decision("PANE_CONFLICT", "pane_placement_or_identity_conflict")
		d.NoNewAction = true
	case "TMUX_SPLIT_FAILED":
		d = decision("EXECUTION_ENVIRONMENT_FAILED", "pane_split_failed")
	case "IDEMPOTENCY_CONFLICT":
		d = decision("IDEMPOTENCY_CONFLICT", "saved_action_binding_conflict")
		d.NoNewAction = true
	case "NOT_FOUND", "PANE_DEAD":
		if c.Verb == "kill" && c.OwnedAbsent {
			d = decision("", "owned_pane_already_absent")
			d.Retain = false
			d.NoNewAction = true
		} else if c.Unfinished || c.Verb == "send" || c.Verb == "wait" || c.Verb == "observe" {
			d = decision("SESSION_LOST", "saved_pane_unavailable")
			d.NoNewAction = c.NoPaste
		} else {
			d = decision("NOT_FOUND", "discovery_target_absent")
			d.NoNewAction = true
		}
	case "COMPOSER_NOT_EMPTY":
		d = decision("SEND_UNCERTAIN", "foreign_composer_text")
		d.NoNewAction = c.NoPaste
	case "NOT_SAFE_TO_SEND":
		if c.Verb == "send" && c.NoPaste {
			d = decision("", "checked_no_paste_refusal")
			d.NoNewAction = true
			d.Continue = true
			if c.BudgetExpired {
				d = decision("TURN_TIMEOUT", "turn_budget_exhausted")
			}
		} else {
			d = decision("SEND_UNCERTAIN", "unproved_no_paste_refusal")
		}
	case "LOCKED":
		d = decision("LOCKED", "upstream_operation_locked")
		d.NoNewAction = !c.Unfinished && (!c.MayHaveSent || c.NoPaste)
	case "APPROVAL_REQUIRED":
		d = decision("TURN_FAILED", "approval_required")
	case "TURN_FAILED":
		d = decision("TURN_FAILED", "upstream_turn_failed")
	case "WAIT_TIMEOUT":
		if c.BudgetExpired {
			d = decision("TURN_TIMEOUT", "turn_budget_exhausted")
		} else {
			d = decision("", "checked_wait_timeout")
			d.Continue = true
		}
	case "SEND_STUCK_IN_COMPOSER", "SEND_UNCONFIRMED", "SEND_INTERRUPTED", "SEND_INPUT_MIXED", "SUBMIT_UNVERIFIED":
		d = decision("SEND_UNCERTAIN", "submission_not_proved")
	default:
		d = decision("UPSTREAM_FAILURE", "unsupported_or_failed_upstream_operation")
	}
	if c.Unfinished && d.Exit == 3 {
		d = decision("TURN_UNCERTAIN", "unfinished_action_prerequisite_failed")
	}
	if code == "LOCKED" && c.Unfinished {
		d = decision("TURN_UNCERTAIN", "unfinished_action_locked")
	}
	if c.Verb == "send" && c.MayHaveSent && !c.NoPaste && code != "IDEMPOTENCY_CONFLICT" && code != "NOT_FOUND" && code != "PANE_DEAD" {
		d = decision("SEND_UNCERTAIN", "send_outcome_not_proved")
	}
	if c.Final && c.Verb == "kill" && d.Code != "" {
		d.CleanupPending = true
		d.Continue = false
	}
	return d
}

func NativeError(d Decision, evidence map[string]any) error {
	if d.Code == "" {
		return nil
	}
	r, e := contract.Load()
	if e != nil {
		return e
	}
	err := r.Error(d.Code, d.Reason)
	err.Evidence = evidence
	return err
}

// NoPasteRefusal applies only to the source-pinned send refusal path. Missing
// fields alone do not prove a no-paste result from an arbitrary operation.
func NoPasteRefusal(r Response, verb, pane string, checked bool) bool {
	if !checked || verb != "send" || r.OK || len(r.Errors) != 1 {
		return false
	}
	e := r.Errors[0]
	if e.Code != "NOT_SAFE_TO_SEND" && e.Code != "COMPOSER_NOT_EMPTY" && e.Code != "PANE_DEAD" {
		return false
	}
	for _, v := range []any{r.Data, e.Evidence} {
		if containsEvidenceField(v, "barrier_cursor") || containsEvidenceField(v, "submit_evidence") {
			return false
		}
		m := Map(v)
		if _, ok := m["barrier_cursor"]; ok {
			return false
		}
		if _, ok := m["submit_evidence"]; ok {
			return false
		}
		if id, ok := m["id"]; ok && id != pane {
			return false
		}
	}
	if e.Code == "PANE_DEAD" {
		return Map(e.Evidence)["id"] == pane
	}
	m := Map(e.Evidence)
	if _, ok := m["state"].(string); !ok {
		return false
	}
	if _, ok := m["source"].(string); !ok {
		return false
	}
	if _, ok := m["confidence"].(interface{ String() string }); !ok {
		return false
	}
	return true
}
func containsEvidenceField(v any, name string) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if k == name || containsEvidenceField(child, name) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if containsEvidenceField(child, name) {
				return true
			}
		}
	}
	return false
}

type Budget struct {
	Limit, ElapsedBefore time.Duration
	Started              time.Time
	Now                  func() time.Time
}

func (b Budget) Elapsed() time.Duration {
	now := time.Now()
	if b.Now != nil {
		now = b.Now()
	}
	elapsed := now.Sub(b.Started) + b.ElapsedBefore
	if elapsed < 0 {
		return b.ElapsedBefore
	}
	return elapsed
}
func (b Budget) Remaining() (time.Duration, bool) {
	if b.Limit == 0 {
		return 0, false
	}
	return b.Limit - b.Elapsed(), true
}
func (b Budget) WaitChunk() (time.Duration, error) {
	if b.Limit < 0 || b.ElapsedBefore < 0 {
		return 0, fmt.Errorf("Invalid turn budget")
	}
	left, finite := b.Remaining()
	if finite && left < time.Second {
		return 0, NativeError(decision("TURN_TIMEOUT", "remaining_budget_below_upstream_minimum"), nil)
	}
	if !finite || left > 24*time.Hour {
		return 24 * time.Hour, nil
	}
	if b.Limit < 0 || b.ElapsedBefore < 0 {
		return 0, fmt.Errorf("Invalid turn budget")
	}
	return left, nil
}
