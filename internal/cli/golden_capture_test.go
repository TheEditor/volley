//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
	"github.com/TheEditor/volley/tests/ownedagent"
)

// Capture one real response per schema. No payload is invented from a schema.
func TestResponseGoldenWitnesses(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "100")
	f := newContractFixture(t, true)
	examples := map[string]any{}
	roots := []string{f.root}
	observations := []any{}
	call := func(g *contractFixture, name string, args ...string) contract.Result {
		r := g.call(t, "golden-"+name, "", args...)
		if e := contract.Validate(name+".json", r.Data); e != nil {
			t.Fatal(name, e)
		}
		examples[name] = r.Data
		observations = append(observations, r)
		return r
	}
	call(f, "help", "--help")
	call(f, "version", "--version")
	call(f, "data-capabilities", "capabilities")
	call(f, "data-schema", "schema")
	call(f, "data-robot-docs-guide", "robot-docs", "guide")
	call(f, "data-conformance", "conformance")
	call(f, "data-plan", "plan", f.ws, "--config", f.config)
	call(f, "data-config-show", "config", "show", "--config", f.config)
	call(f, "data-config-get", "config", "get", "max_rounds", "--config", f.config)
	call(f, "data-config-validate", "config", "validate", f.config)
	call(f, "data-config-schema", "config", "schema")
	edit := filepath.Join(f.root, "edit.toml")
	os.WriteFile(edit, []byte("# Owned example\nmax_rounds = 3\n"), 0600)
	call(f, "data-config-set", "config", "set", "max_rounds", "4", "--config", edit)
	f.opts.Input = strings.NewReader(`{"max_rounds":5}`)
	call(f, "data-config-patch", "config", "patch", "--config", edit, "--from-stdin")
	editor := filepath.Join(f.root, "tools/editor")
	os.WriteFile(editor, []byte("#!/bin/sh\nexit 0\n"), 0700)
	f.opts.RunOptions.Env = append(f.opts.RunOptions.Env, "EDITOR="+editor)
	yes := true
	f.opts.Terminal = &yes
	call(f, "data-config-edit", "config", "edit", "--config", edit)
	f.opts.Terminal = nil
	r := call(f, "data-run", "run", f.ws, "--config", f.config)
	examples["envelope"] = r
	examples["final-result"] = r.Data.(map[string]any)["final_result"]
	call(f, "data-runs-resume", "runs", "resume", f.ws)
	first := call(f, "data-status", "status", f.ws)
	second := f.call(t, "golden-stable-status", "", "status", f.ws)
	a, _ := contract.Canonical(first.Data)
	b, _ := contract.Canonical(second.Data)
	if !bytes.Equal(a, b) || first.Meta.DataHash != second.Meta.DataHash || first.Meta.Time != second.Meta.Time {
		t.Fatal("saved response is not deterministic")
	}
	var human, stderr bytes.Buffer
	if exit := Execute(context.Background(), []string{"status", f.ws}, &human, &stderr, f.opts); exit != 0 {
		t.Fatal(stderr.String())
	}
	var humanData any
	d := json.NewDecoder(&human)
	d.UseNumber()
	if e := d.Decode(&humanData); e != nil {
		t.Fatal(e)
	}
	b, _ = contract.Canonical(humanData)
	if !bytes.Equal(a, b) {
		t.Fatal("human and machine data differ")
	}
	call(f, "data-runs-get", "runs", "get", f.ws)
	call(f, "data-runs-list", "runs", "list")
	call(f, "data-runs-events", "runs", "events", f.ws)
	call(f, "data-doctor", "doctor", "--workspace", f.ws)
	call(f, "data-runs-prune", "runs", "prune", "--older-than=1h", "--dry-run")
	call(f, "data-feedback", "feedback", "Owned example", "--idempotency-key=golden")
	call(f, "data-delivery", "config", "show", "--config", edit, "--deliver=null")
	// Raw mode delivers exact TOML to an owned file. Its stdout is a receipt.
	raw := filepath.Join(f.root, "raw.toml")
	receipt := f.call(t, "golden-raw", "", "config", "show", "--config", edit, "--toml", "--deliver=file:"+raw)
	rawBytes, _ := os.ReadFile(raw)
	examples["raw-config-show"] = string(rawBytes)
	if !strings.Contains(string(rawBytes), "max_rounds = 5") {
		t.Fatal("raw setting absent")
	}
	rb, _ := json.Marshal(receipt.Data)
	var rd map[string]any
	json.Unmarshal(rb, &rd)
	if rd["sha256"] != contract.HashBytes(rawBytes) {
		t.Fatal("raw receipt differs")
	}
	examples["error"] = f.call(t, "golden-error", "INVALID_INPUT", "--json=false").Errors[0]
	examples["warning"] = r.Warnings[0]
	// Questions, inputs, and their receipt records need one draft turn.
	q := newContractFixture(t, false)
	roots = append(roots, q.root)
	q.setPlan(t, ownedagent.Plan{QuestionPurpose: "draft"})
	q.run(t, "question-setup", "ANSWER_REQUIRED")
	questions := call(q, "data-human-questions", "human", "questions", q.ws)
	qb, _ := json.Marshal(questions.Data)
	var qd map[string]any
	json.Unmarshal(qb, &qd)
	qid := qd["question"].(map[string]any)["id"].(string)
	q.opts.Input = strings.NewReader(`{"text":"Owned answer","idempotency_key":"golden-answer"}`)
	call(q, "data-human-answer", "human", "answer", q.ws, "--question-id="+qid, "--from-stdin")
	q.opts.Input = strings.NewReader(`{"text":"Owned directive","idempotency_key":"golden-steer"}`)
	call(q, "data-human-steer", "human", "steer", q.ws, "--from-stdin")
	call(q, "data-human-skip", "human", "skip", q.ws, "--question-id="+qid, "--yes")
	call(q, "data-runs-stop", "runs", "stop", q.ws, "--yes")
	legacy := newContractFixture(t, true)
	roots = append(roots, legacy.root)
	os.Mkdir(filepath.Join(legacy.ws, "state"), 0700)
	os.WriteFile(filepath.Join(legacy.ws, "state/run"), []byte("old owned run"), 0600)
	call(legacy, "data-workspace-legacy-report", "workspace", "legacy-report", legacy.ws)
	// An actual interrupted intent is explicitly abandoned without another call.
	resolve := newContractFixture(t, true)
	roots = append(roots, resolve.root)
	resolve.opts.RunOptions.Hook = func(name string, s *store.Store) error {
		if name == "engine.intent.committed" {
			return fmt.Errorf("owned interruption")
		}
		return nil
	}
	resolve.run(t, "resolve-setup", "INTERNAL")
	resolve.opts.RunOptions.Hook = nil
	s, e := store.Open(resolve.ws)
	if e != nil {
		t.Fatal(e)
	}
	m, _, e := s.LoadSnapshot()
	s.Close()
	if e != nil {
		t.Fatal(e)
	}
	resolve.opts.Input = strings.NewReader(`{"resolution":"abandon","artifact_paths":[],"evidence_paths":[],"note":"Owned example keeps evidence"}`)
	call(resolve, "data-runs-resolve", "runs", "resolve", resolve.ws, "--turn="+m.Object("current_turn")["id"].(string), "--from-stdin", "--yes")
	inboxStore, e := store.Open(q.ws)
	if e != nil {
		t.Fatal(e)
	}
	inboxManifest, _, e := inboxStore.LoadSnapshot()
	if e != nil {
		t.Fatal(e)
	}
	inbox, e := inboxStore.ReadInbox(context.Background(), inboxManifest.String("run_id"), 0)
	inboxStore.Close()
	if e != nil || len(inbox.Entries) == 0 {
		t.Fatal("input inbox absent", e)
	}
	examples["inbox-entry"] = inbox.Entries[0]
	// Retain actual durable record examples before automatic fixture deletion.
	for _, root := range roots {
		filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
			if e != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
				return nil
			}
			b, e := os.ReadFile(path)
			if e != nil {
				return nil
			}
			var v any
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.UseNumber()
			if dec.Decode(&v) != nil {
				return nil
			}
			for _, name := range []string{"manifest", "receipt", "input-receipt", "event-page", "event", "preparation", "final-result", "resolution-record", "inbox-entry", "transaction"} {
				if _, exists := examples[name]; !exists && contract.Validate(name+".json", v) == nil {
					examples[name] = v
				}
			}
			return nil
		})
	}
	// Event pages are the actual inspection payload; settings are the resolved
	// typed values, both already emitted and schema checked by command handlers.
	examples["event-page"] = examples["data-runs-events"]
	events := examples["data-runs-events"].(map[string]any)["events"].([]any)
	if len(events) > 0 {
		examples["event"] = events[0]
	}
	examples["settings"] = examples["data-config-show"].(map[string]any)["config"]
	if e := contract.Validate("settings.json", examples["settings"]); e != nil {
		t.Fatal(e)
	}
	payload, _ := json.Marshal(map[string]any{"examples": examples, "responses": observations, "roots": roots, "binary_fixture": "owned test executable", "live_vendor_calls": 0})
	t.Log("GOLDEN_EVIDENCE", string(payload))
}
