package gashki

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAGK08NativeOutcomeRules(t *testing.T) {
	cases := []struct {
		upstream, verb, native string
		exit                   int
		noAction               bool
	}{
		{"UNKNOWN_FLAG", "spawn", "UPSTREAM_FAILURE", 8, true}, {"UNKNOWN_COMMAND", "status", "UPSTREAM_FAILURE", 8, true}, {"INVALID_INPUT", "send", "UPSTREAM_FAILURE", 8, true}, {"MISSING_REQUIRED", "wait", "UPSTREAM_FAILURE", 8, true},
		{"INVALID_CONFIG", "config get", "INVALID_CONFIG", 3, true}, {"TMUX_NO_SERVER", "spawn", "DEPENDENCY_MISSING", 3, true}, {"TMUX_TOO_OLD", "spawn", "DEPENDENCY_MISSING", 3, true}, {"AGENT_CLI_MISSING", "spawn", "DEPENDENCY_MISSING", 3, true}, {"STATE_DIR_UNWRITABLE", "config show", "DEPENDENCY_MISSING", 3, true},
		{"STATE_DIR_UNWRITABLE", "spawn", "UPSTREAM_FAILURE", 8, false}, {"STATE_DIR_UNWRITABLE", "kill", "UPSTREAM_FAILURE", 8, false},
		{"AGENT_NOT_LOGGED_IN", "wait", "UPSTREAM_FAILURE", 8, false}, {"CODEX_HOOKS_UNTRUSTED", "spawn", "UPSTREAM_FAILURE", 8, false}, {"LAUNCH_PROMPT_UNHANDLED", "spawn", "UPSTREAM_FAILURE", 8, false},
		{"HERE_UNAVAILABLE", "spawn", "PANE_CONFLICT", 5, true}, {"TMUX_SPLIT_FAILED", "spawn", "EXECUTION_ENVIRONMENT_FAILED", 3, false}, {"CONFLICT", "spawn", "PANE_CONFLICT", 5, true}, {"IDEMPOTENCY_CONFLICT", "send", "IDEMPOTENCY_CONFLICT", 5, true},
		{"NOT_FOUND", "status", "NOT_FOUND", 1, true}, {"NOT_FOUND", "send", "SESSION_LOST", 8, false}, {"NOT_FOUND", "wait", "SESSION_LOST", 8, false}, {"NOT_FOUND", "observe", "SESSION_LOST", 8, false}, {"PANE_DEAD", "wait", "SESSION_LOST", 8, false},
		{"COMPOSER_NOT_EMPTY", "send", "SEND_UNCERTAIN", 8, false}, {"NOT_SAFE_TO_SEND", "send", "SEND_UNCERTAIN", 8, false}, {"LOCKED", "kill", "LOCKED", 4, true},
		{"APPROVAL_REQUIRED", "wait", "TURN_FAILED", 8, false}, {"TURN_FAILED", "wait", "TURN_FAILED", 8, false},
		{"SEND_STUCK_IN_COMPOSER", "send", "SEND_UNCERTAIN", 8, false}, {"SEND_UNCONFIRMED", "send", "SEND_UNCERTAIN", 8, false}, {"SEND_INTERRUPTED", "send", "SEND_UNCERTAIN", 8, false}, {"SEND_INPUT_MIXED", "send", "SEND_UNCERTAIN", 8, false}, {"SUBMIT_UNVERIFIED", "send", "SEND_UNCERTAIN", 8, false},
		{"AGENT_UNVERIFIABLE", "send", "UPSTREAM_FAILURE", 8, false}, {"ENV_NOT_SET", "spawn", "UPSTREAM_FAILURE", 8, false}, {"CONFIRMATION_REQUIRED", "kill", "UPSTREAM_FAILURE", 8, false}, {"INTERNAL", "send", "UPSTREAM_FAILURE", 8, false}, {"CONFORMANCE_FAILED", "status", "UPSTREAM_FAILURE", 8, false},
		{"TTY_REQUIRED", "config show", "UPSTREAM_FAILURE", 8, false}, {"CONFIG_UNWRITABLE", "config get", "UPSTREAM_FAILURE", 8, false}, {"EDITOR_FAILED", "config show", "UPSTREAM_FAILURE", 8, false}, {"DATA_DIR_UNWRITABLE", "status", "UPSTREAM_FAILURE", 8, false}, {"UNKNOWN", "observe", "UPSTREAM_FAILURE", 8, false},
	}
	for _, q := range cases {
		t.Run(q.upstream+"/"+q.verb, func(t *testing.T) {
			d := MapFailure(q.upstream, ActionContext{Verb: q.verb, Checked: true})
			if d.Code != q.native || d.Exit != q.exit || !d.Retain || d.NoNewAction != q.noAction || d.Continue {
				t.Fatal(d)
			}
			if q.verb == "send" && q.upstream != "IDEMPOTENCY_CONFLICT" && q.upstream != "NOT_FOUND" {
				d = MapFailure(q.upstream, ActionContext{Verb: q.verb, Checked: true, MayHaveSent: true})
				if d.Code != "SEND_UNCERTAIN" || d.Exit != 8 || !d.Retain {
					t.Fatal("uncertain send got safe result", d)
				}
			}
		})
	}
	for _, code := range []string{"INVALID_CONFIG", "TMUX_NO_SERVER", "TMUX_TOO_OLD", "AGENT_CLI_MISSING", "STATE_DIR_UNWRITABLE", "TMUX_SPLIT_FAILED", "LOCKED"} {
		d := MapFailure(code, ActionContext{Verb: "wait", Checked: true, Unfinished: true})
		if d.Code != "TURN_UNCERTAIN" || d.Exit != 8 {
			t.Fatal(code, d)
		}
	}
	for _, code := range []string{"NOT_FOUND", "PANE_DEAD"} {
		d := MapFailure(code, ActionContext{Verb: "kill", Checked: true, Final: true, OwnedAbsent: true})
		if d.Code != "" || d.Exit != 0 || d.Retain || d.CleanupPending {
			t.Fatal(d)
		}
	}
	for _, code := range []string{"INTERNAL", "LOCKED", "NOT_FOUND", "INVALID_CONFIG"} {
		d := MapFailure(code, ActionContext{Verb: "kill", Checked: true, Final: true})
		if !d.CleanupPending || !d.Retain {
			t.Fatal(d)
		}
	}
	for _, expired := range []bool{false, true} {
		d := MapFailure("WAIT_TIMEOUT", ActionContext{Verb: "wait", Checked: true, BudgetExpired: expired})
		if expired {
			if d.Code != "TURN_TIMEOUT" || d.Exit != 8 || d.Continue {
				t.Fatal(d)
			}
		} else if d.Code != "" || !d.Continue {
			t.Fatal(d)
		}
	}
	d := MapFailure("NOT_SAFE_TO_SEND", ActionContext{Verb: "send", Checked: true, NoPaste: true})
	if d.Code != "" || !d.Continue || !d.NoNewAction {
		t.Fatal(d)
	}
	d = MapFailure("NOT_SAFE_TO_SEND", ActionContext{Verb: "send", Checked: true, NoPaste: true, BudgetExpired: true})
	if d.Code != "TURN_TIMEOUT" || d.Continue {
		t.Fatal(d)
	}
	d = MapFailure("APPROVAL_REQUIRED", ActionContext{Verb: "wait", Checked: true, Unfinished: true})
	if d.Reason != "approval_required" {
		t.Fatal(d)
	}
}

func TestSourceBoundNoPasteEvidence(t *testing.T) {
	pane := "01234567-1234-7123-8123-0123456789ab"
	p, e := LoadProtocol()
	if e != nil {
		t.Fatal(e)
	}
	for _, code := range []string{"NOT_SAFE_TO_SEND", "COMPOSER_NOT_EMPTY", "PANE_DEAD"} {
		t.Run(code, func(t *testing.T) {
			raw := rewrite(t, envelope(t, nil, code), func(m map[string]any) {
				Map(m["errors"].([]any)[0])["evidence"] = map[string]any{"id": pane, "state": "working", "source": "hook", "confidence": json.Number("1")}
			})
			response, e := p.Check("send", raw, int(Integer(Map(Map(p.Capabilities["error_codes"])[code])["exit_code"])))
			if e != nil {
				t.Fatal(e)
			}
			if !NoPasteRefusal(response, "send", pane, true) {
				t.Fatal("pinned refusal rejected")
			}
			if NoPasteRefusal(response, "wait", pane, true) || NoPasteRefusal(response, "send", pane, false) || NoPasteRefusal(response, "send", strings.Replace(pane, "ab", "ff", 1), true) {
				t.Fatal("unbound refusal accepted")
			}
			for _, field := range []string{"barrier_cursor", "submit_evidence"} {
				for _, where := range []string{"data", "evidence", "nested"} {
					changed := rewrite(t, raw, func(m map[string]any) {
						switch where {
						case "data":
							m["data"] = map[string]any{field: nil}
						case "evidence":
							Map(Map(m["errors"].([]any)[0])["evidence"])[field] = nil
						case "nested":
							Map(Map(m["errors"].([]any)[0])["evidence"])["nested"] = []any{map[string]any{field: "present"}}
						}
					})
					r, e := p.Check("send", rehash(t, changed), int(response.Errors[0].Exit))
					if e != nil {
						t.Fatal(e)
					}
					if NoPasteRefusal(r, "send", pane, true) {
						t.Fatal("contradictory refusal accepted", field, where)
					}
				}
			}
		})
	}
}

func TestProtocolOutcomeVerbAndRegistryBindings(t *testing.T) {
	p, e := LoadProtocol()
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range []struct{ code, right, wrong string }{{"CONFLICT", "spawn", "send"}, {"IDEMPOTENCY_CONFLICT", "send", "spawn"}, {"ENV_NOT_SET", "spawn", "config get"}, {"APPROVAL_REQUIRED", "wait", "kill"}, {"AGENT_UNVERIFIABLE", "send", "config show"}} {
		raw := envelope(t, nil, q.code)
		exit := int(Integer(Map(Map(p.Capabilities["error_codes"])[q.code])["exit_code"]))
		if _, e := p.Check(q.right, raw, exit); e != nil {
			t.Fatal(q, e)
		}
		if _, e := p.Check(q.wrong, raw, exit); e == nil {
			t.Fatal("wrong verb accepted", q)
		}
	}
	changed := document(t, p.Capabilities)
	Map(Map(changed["error_codes"])["SEND_INTERRUPTED"])["retryable"] = true
	if e := p.RequiredSubset(changed, p.SchemaData); e == nil {
		t.Fatal("changed registry accepted")
	}
}
