//go:build darwin || linux

package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/TheEditor/volley/internal/prompt"
	"github.com/TheEditor/volley/internal/store"
)

type State struct {
	Context            *store.FileObservation           `json:"context,omitempty"`
	RecordVersion      int                              `json:"record_version"`
	RunID              string                           `json:"run_id"`
	Settings           config.Settings                  `json:"settings"`
	Bindings           map[string]store.FileObservation `json:"bindings"`
	Seeds              map[string]store.FileObservation `json:"seeds"`
	PromptSetHash      string                           `json:"prompt_set_hash"`
	LastTurn           string                           `json:"last_turn"`
	LastCritique       string                           `json:"last_critique"`
	LastReceipt        *store.ReceiptRef                `json:"last_receipt"`
	ActiveApplications []string                         `json:"active_applications"`
	QuestionPending    bool                             `json:"question_pending"`
	QuestionObserved   store.FileObservation            `json:"question_observed"`
	OrdinaryReceipt    *store.ReceiptRef                `json:"ordinary_receipt,omitempty"`
	Warnings           []contract.Warning               `json:"warnings,omitempty"`
	Auxiliary          Auxiliary                        `json:"auxiliary"`
}

func decode(b []byte, target any) error {
	if _, err := input.Record(bytes.NewReader(b), store.RecordLimit); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(target)
}

func LoadState(s *store.Store, m store.Snapshot) (State, error) {
	var state State
	ref := m.Object("engine")
	path, _ := ref["path"].(string)
	if !stringsHasEnginePrefix(path) {
		return state, failure("STATE_INVALID", "Engine record is absent", nil)
	}
	b, err := s.ReadText(path)
	if err != nil {
		return state, err
	}
	if contract.HashBytes(b) != ref["sha256"] {
		return state, failure("STATE_INVALID", "Engine record hash differs", nil)
	}
	if err := decode(b, &state); err != nil {
		return state, err
	}
	if state.RecordVersion != 1 || state.RunID != m.String("run_id") {
		return state, failure("STATE_INVALID", "Engine record run differs", nil)
	}
	return state, nil
}

func stringsHasEnginePrefix(path string) bool {
	return filepath.Dir(path) == "state/control" && len(filepath.Base(path)) == len("engine-")+26+len(".json") && filepath.Base(path)[:7] == "engine-"
}

func saveState(s *store.Store, m store.Snapshot, state State, kind string, artifacts []store.Artifact, receipt *store.ReceiptRef) error {
	if len(state.Warnings) > 0 {
		m["warnings"] = state.Warnings
	}
	id, err := store.NewID()
	if err != nil {
		return err
	}
	b, err := contract.Canonical(state)
	if err != nil {
		return err
	}
	path := "state/control/engine-" + id + ".json"
	hash, err := s.StagePrivateText(path, b)
	if err != nil {
		return err
	}
	m["engine"] = map[string]any{"path": path, "sha256": hash}
	tx, err := s.NewTransaction(kind, m, artifacts, receipt)
	if err != nil {
		return err
	}
	return s.CommitTransaction(tx)
}

func observation(s *store.Store, path string) (store.FileObservation, error) {
	_, o, err := s.ReadObserved(path, store.TextLimit)
	return o, err
}

func bindInputs(s *store.Store) (map[string]store.FileObservation, error) {
	result := make(map[string]store.FileObservation)
	for _, path := range []string{"BRIEF.md", "CONSTRAINTS.md", "SPEC.md"} {
		o, err := observation(s, path)
		if err != nil {
			return nil, err
		}
		result[path] = o
	}
	return result, nil
}

func checkBindings(s *store.Store, state State) error {
	for path, before := range state.Bindings {
		after, err := observation(s, path)
		if err != nil || before != after {
			return failure("ARTIFACT_CHANGED", "Binding input changed", map[string]any{"actor": "external_or_unknown", "path": path, "before": before, "after": after})
		}
	}
	return nil
}

func promptSetHash() (string, error) {
	hashes := make(map[string]string)
	for _, name := range []string{"planner-init.md", "planner-revise.md", "directive-apply.md", "critic.md", "verdict-reminder.md", "advisory-review.md", "closing-pass.md", "closing-native.md", "confirm-closing.md", "answer-record.md", "questions-cli.md", "questions-gashki.md", "planner-boundary.md", "critic-boundary.md", "common-review-rule.md", "profiles/security.md", "profiles/data.md", "profiles/decision-memo.md", "profiles/plan-spec.md"} {
		b, err := prompt.Template(name)
		if err != nil {
			return "", err
		}
		hashes[name] = contract.HashBytes(b)
	}
	b, err := contract.Canonical(hashes)
	return contract.HashBytes(b), err
}

func initialize(s *store.Store, settings config.Settings, creation Creation) (store.Snapshot, error) {
	id, err := store.NewID()
	if err != nil {
		return nil, err
	}
	root, err := s.RootObservation()
	if err != nil {
		return nil, err
	}
	critic := "codex"
	if settings.Planner == "codex" {
		critic = "claude"
	}
	sources := make(map[string]config.Source)
	registry, err := contract.Load()
	if err != nil {
		return nil, err
	}
	for _, setting := range registry.Settings {
		sources[setting.Key] = config.Source{Source: "default", Rule: "preparation pending"}
	}
	absent := func() map[string]any {
		return map[string]any{"path": "", "sha256": "", "bytes": 0, "type": "absent", "device": 0, "inode": 0}
	}
	variable := func() map[string]any { return map[string]any{"present": false, "value": nil, "reason": "not prepared"} }
	session := func() map[string]any {
		return map[string]any{"intended_id": "", "observed_id": "", "observed_model": "", "observed_effort": "", "reason": "not prepared"}
	}
	pane := func() map[string]any {
		return map[string]any{"uuid": "", "target": "", "selector": "", "provider": "", "ready_cursor": ""}
	}
	exe := func() map[string]any { return map[string]any{"path": "", "sha256": "", "version": ""} }
	m := store.Snapshot{
		"record_version": 1, "run_id": id, "tool_version": "dev", "source_commit": "", "workspace": s.Path, "canonical_workspace": s.Path,
		"ownership": map[string]any{"device": root.Device, "inode": root.Inode}, "created_at": time.Now().UTC().Format(time.RFC3339Nano),
		"request_keys": []string{}, "request_hash": "", "status": "ready", "phase": "prepare", "round": 1, "completed_rounds": 0, "max_rounds": settings.MaxRounds,
		"amendments": []any{}, "setting_sources": sources, "config_records": map[string]any{"volley": absent(), "gashki": absent()},
		"prompt_hashes": map[string]any{"template": "", "rendered": ""}, "executables": map[string]any{"claude": exe(), "codex": exe(), "gashki": exe()},
		"roles": map[string]any{"planner": settings.Planner, "critic": critic}, "backend_capabilities": map[string]any{"process_per_turn": settings.Backend == "cli", "exact_resume_id": settings.Backend == "cli", "read_only_critic": settings.Backend == "cli" && critic == "codex"},
		"sessions": map[string]any{"planner": session(), "critic": session()}, "panes": map[string]any{"planner": pane(), "critic": pane(), "second": pane()},
		"server":   map[string]any{"socket_path": nil, "caller_window": "", "identity_reason": "direct backend", "home": nil, "claude_root": variable(), "codex_root": variable(), "state_dir": "", "socket_device": 0, "socket_inode": 0, "socket_kind": "absent"},
		"identity": map[string]any{"home": nil, "claude_root": variable(), "codex_root": variable()}, "current_turn": nil, "spec_hash": "", "reviewed_spec_hash": "",
		"verdict": map[string]any{"value": "MISSING", "hash": "", "line": 0, "parser_version": 1}, "approval": map[string]any{"spec_hash": "", "reviewed_hash": "", "inputs_hash": "", "receipt_path": "", "basis": ""},
		"question": nil, "answers": []string{}, "steering": []string{}, "auxiliary": map[string]any{"second_opinion": "", "closing": "", "confirmation": "", "approved_hash": "", "rejected_hash": "", "skip_reason": "", "restoration_intent": "", "restoration_receipt": ""},
		"retention": map[string]any{"kind": "retain", "reason": "Keep durable evidence", "panes": []string{}, "processes": []any{}}, "cleanup": map[string]any{"pending": false, "actions": []string{}},
		"errors": []string{}, "recommended_commands": []string{}, "event_sequence": 0, "audit_pending": false, "index_registered": false, "preparation": nil,
	}
	m["creation"], err = stageCreation(s, id, creation)
	if err != nil {
		return nil, err
	}
	tx, err := s.NewTransaction("initialize", m, nil, nil)
	if err != nil {
		return nil, err
	}
	if err = s.CommitTransaction(tx); err != nil {
		return nil, err
	}
	return m, nil
}

func number(v any) int { var n int; _, _ = fmt.Sscan(fmt.Sprint(v), &n); return n }
