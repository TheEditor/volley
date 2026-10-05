//go:build darwin || linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/contract"
)

func schemaFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := contract.Schema(name)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	var value func(map[string]any) any
	value = func(s map[string]any) any {
		if c, ok := s["const"]; ok {
			return c
		}
		if enum, ok := s["enum"].([]any); ok {
			return enum[0]
		}
		if choices, ok := s["anyOf"].([]any); ok {
			for _, choice := range choices {
				m := choice.(map[string]any)
				if m["type"] == "null" {
					return nil
				}
			}
			return value(choices[0].(map[string]any))
		}
		switch s["type"] {
		case "object":
			m := make(map[string]any)
			props := s["properties"].(map[string]any)
			for _, key := range s["required"].([]any) {
				m[key.(string)] = value(props[key.(string)].(map[string]any))
			}
			return m
		case "array":
			return []any{}
		case "integer":
			return 0
		case "boolean":
			return false
		case "string":
			return ""
		}
		return nil
	}
	return value(schema).(map[string]any)
}
func fixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err := s.AcquireOwner(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	m := Snapshot(schemaFixture(t, "manifest"))
	runID, _ := NewID()
	o := observationOf(".", nil, s.identity)
	m["run_id"] = runID
	m["canonical_workspace"] = s.Path
	m["workspace"] = s.Path
	m["ownership"] = map[string]any{"device": o.Device, "inode": o.Inode}
	tx, err := s.NewTransaction("initialize", m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitTransaction(tx); err != nil {
		t.Fatal(err)
	}
	return s
}
func nextIntent(t *testing.T, s *Store) Transaction {
	t.Helper()
	m, _, err := s.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	turnID, _ := NewID()
	turn := map[string]any{"id": turnID, "purpose": "revise", "role": "planner", "round": 1, "attempt": 1, "intent_hash": "fixture-intent", "prompt_path": "state/prompts/fixture.md", "prompt_hash": contract.HashBytes([]byte("prompt")), "operation": "turn", "delivery_uncertain": false, "receipt_path": "", "cursor": "", "started_at": "", "budget": "", "remaining": ""}
	m["current_turn"] = turn
	m["status"] = "running"
	m["phase"] = "revise"
	m["spec_hash"] = contract.HashBytes([]byte("old spec"))
	if _, err := s.StageText("state/prompts/fixture.md", []byte("prompt")); err != nil {
		t.Fatal(err)
	}
	tx, err := s.NewTransaction("intent", m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
func receiptFixture(t *testing.T, s *Store) (string, map[string]any) {
	t.Helper()
	m, _, err := s.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	turn := m.Object("current_turn")
	r := schemaFixture(t, "receipt")
	r["record_version"] = 1
	r["run_id"] = m["run_id"]
	r["turn_id"] = turn["id"]
	for _, key := range []string{"purpose", "role", "round", "prompt_hash"} {
		r[key] = turn[key]
	}
	r["spec_before_hash"] = m["spec_hash"]
	r["completion"].(map[string]any)["kind"] = "exited"
	r["completion"].(map[string]any)["settled"] = true
	return turn["id"].(string), r
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func put(t *testing.T, s *Store, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(s.Path, path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Path, path), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func faultSites(t *testing.T, prepare func(*testing.T, *Store) func() error) []string {
	t.Helper()
	s := fixture(t)
	operation := prepare(t, s)
	var sites []string
	s.Fault = func(site string) error { sites = append(sites, site); return nil }
	if err := operation(); err != nil {
		t.Fatal(err)
	}
	s.Fault = nil
	t.Logf("%s boundaries=%d sites=%v", runtime.GOOS, len(sites), sites)
	return sites
}
func TestASTATE01Intent(t *testing.T) {
	prep := func(t *testing.T, s *Store) func() error {
		tx := nextIntent(t, s)
		return func() error { return s.CommitTransaction(tx) }
	}
	for _, site := range faultSites(t, prep) {
		t.Run(site, func(t *testing.T) {
			s := fixture(t)
			operation := prep(t, s)
			fired := false
			actions := 0
			s.Fault = func(name string) error {
				if name == site && !fired {
					fired = true
					return fmt.Errorf("interrupt %s", name)
				}
				return nil
			}
			if err := operation(); err == nil || !fired {
				t.Fatal("fault did not stop commit")
			}
			if actions != 0 {
				t.Fatal("action before commit")
			}
			s.Fault = nil
			r, err := s.Recover()
			if err != nil {
				t.Fatal(err)
			}
			if r.Unfinished {
				if err := s.ReadyIntent(activeIntentID(t, s)); err != nil {
					t.Fatal(err)
				}
				actions++
			}
			if actions > 1 {
				t.Fatal("repeated action")
			}
			if _, err := s.Recover(); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("partial-transaction", func(t *testing.T) {
		s := fixture(t)
		tx := nextIntent(t, s)
		put(t, s, txPath(2, tx.ID), `{"record_version":1`)
		if _, err := s.Recover(); err == nil {
			t.Fatal("partial transaction advanced")
		}
		m, _, _ := s.LoadSnapshot()
		if m.Revision() != 1 {
			t.Fatal("revision changed")
		}
	})
	t.Run("uncommitted-intent", func(t *testing.T) {
		s := fixture(t)
		tx := nextIntent(t, s)
		requireCode(t, s.ReadyIntent(tx.ID), "STATE_INVALID")
	})
	t.Run("partial-spec-retained", func(t *testing.T) {
		s := fixture(t)
		tx := nextIntent(t, s)
		if err := s.CommitTransaction(tx); err != nil {
			t.Fatal(err)
		}
		put(t, s, "SPEC.md", "partial after crash")
		r, err := s.Recover()
		if err != nil || !r.Unfinished || r.Receipt != nil {
			t.Fatalf("%+v %v", r, err)
		}
		b, _ := os.ReadFile(filepath.Join(s.Path, "SPEC.md"))
		if string(b) != "partial after crash" {
			t.Fatal("partial spec overwritten")
		}
	})
}
func TestASTATE01Receipt(t *testing.T) {
	prep := func(t *testing.T, s *Store) func() error {
		tx := nextIntent(t, s)
		if err := s.CommitTransaction(tx); err != nil {
			t.Fatal(err)
		}
		id, r := receiptFixture(t, s)
		return func() error { _, err := s.SaveTurnReceipt(id, r); return err }
	}
	for _, site := range faultSites(t, prep) {
		t.Run(site, func(t *testing.T) {
			s := fixture(t)
			operation := prep(t, s)
			fired := false
			s.Fault = func(name string) error {
				if name == site && !fired {
					fired = true
					return errors.New("receipt interrupted")
				}
				return nil
			}
			if err := operation(); err == nil || !fired {
				t.Fatal("no receipt interruption")
			}
			s.Fault = nil
			r, err := s.Recover()
			if err != nil || !r.Unfinished {
				t.Fatalf("%+v %v", r, err)
			}
			if err := operation(); err != nil {
				t.Fatal(err)
			}
			r, err = s.Recover()
			if err != nil || r.Receipt == nil {
				t.Fatalf("receipt not recovered: %+v %v", r, err)
			}
		})
	}
	t.Run("bindings", func(t *testing.T) {
		s := fixture(t)
		tx := nextIntent(t, s)
		if err := s.CommitTransaction(tx); err != nil {
			t.Fatal(err)
		}
		id, r := receiptFixture(t, s)
		r["prompt_hash"] = "wrong"
		_, err := s.SaveTurnReceipt(id, r)
		requireCode(t, err, "STATE_INVALID")
	})
}
func resultOperation(t *testing.T, s *Store, final bool) func() error {
	t.Helper()
	intent := nextIntent(t, s)
	if err := s.CommitTransaction(intent); err != nil {
		t.Fatal(err)
	}
	id, r := receiptFixture(t, s)
	ref, err := s.SaveTurnReceipt(id, r)
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, "SPEC.md", "old spec")
	before, err := s.Observe("SPEC.md")
	if err != nil {
		t.Fatal(err)
	}
	stage := "state/turns/" + id + "/spec.md"
	hash, err := s.StageText(stage, []byte("accepted spec"))
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := s.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	m["spec_hash"] = hash
	m["current_turn"] = nil
	kind := "result"
	if final {
		kind = "final"
		m["status"] = "approved"
		m["phase"] = "commit_final"
	}
	tx, err := s.NewTransaction(kind, m, []Artifact{{stage, "rounds/r01.spec.md", hash, false, nil}, {stage, "SPEC.md", hash, true, &before}}, &ref)
	if err != nil {
		t.Fatal(err)
	}
	return func() error { return s.CommitTransaction(tx) }
}
func TestASTATE02Results(t *testing.T) {
	for _, final := range []bool{false, true} {
		kind := "result"
		if final {
			kind = "final"
		}
		prep := func(t *testing.T, s *Store) func() error { return resultOperation(t, s, final) }
		for _, site := range faultSites(t, prep) {
			t.Run(kind+"/"+site, func(t *testing.T) {
				s := fixture(t)
				operation := prep(t, s)
				fired := false
				s.Fault = func(name string) error {
					if name == site && !fired {
						fired = true
						return errors.New("result interrupted")
					}
					return nil
				}
				if err := operation(); err == nil || !fired {
					t.Fatal("no interruption")
				}
				s.Fault = nil
				r, err := s.Recover()
				if err != nil {
					t.Fatal(err)
				}
				if r.Unfinished && r.Receipt == nil {
					t.Fatal("saved receipt was lost")
				}
				// Missing publication leaves a receipt for the engine's supplied transition.
				if r.Unfinished {
					if err := operation(); err != nil {
						t.Fatal(err)
					}
				}
				r, err = s.Recover()
				if err != nil || r.Unfinished {
					t.Fatalf("%+v %v", r, err)
				}
				b, _ := os.ReadFile(filepath.Join(s.Path, "SPEC.md"))
				if string(b) != "accepted spec" {
					t.Fatal("artifact not promoted")
				}
				if _, err := s.Recover(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	t.Run("bad-staged-hash", func(t *testing.T) {
		s := fixture(t)
		operation := resultOperation(t, s, false)
		names, _ := s.names("state/turns")
		put(t, s, "state/turns/"+names[0]+"/spec.md", "partial")
		if err := operation(); err == nil {
			t.Fatal("partial stage advanced")
		}
		m, _, _ := s.LoadSnapshot()
		if m.Revision() != 2 {
			t.Fatal("bad stage advanced revision")
		}
	})
	t.Run("unexpected-round", func(t *testing.T) {
		s := fixture(t)
		operation := resultOperation(t, s, false)
		put(t, s, "rounds/r01.spec.md", "other evidence")
		requireCode(t, operation(), "OUTPUT_CONFLICT")
		b, _ := os.ReadFile(filepath.Join(s.Path, "rounds/r01.spec.md"))
		if string(b) != "other evidence" {
			t.Fatal("overwritten")
		}
	})
	t.Run("missing-receipt", func(t *testing.T) {
		s := fixture(t)
		operation := resultOperation(t, s, false)
		names, _ := s.names("state/turns")
		_ = os.Remove(filepath.Join(s.Path, "state/turns", names[0], "receipt.json"))
		if err := operation(); err == nil {
			t.Fatal("missing receipt advanced")
		}
	})
}
func TestASTATE03Audit(t *testing.T) {
	t.Run("missing-and-partial", func(t *testing.T) {
		s := fixture(t)
		if err := os.Remove(filepath.Join(s.Path, "state/events.jsonl")); err != nil {
			t.Fatal(err)
		}
		if ok, err := s.EvidenceComplete(); err != nil || ok {
			t.Fatalf("complete=%v %v", ok, err)
		}
		if _, err := s.Recover(); err != nil {
			t.Fatal(err)
		}
		f, _ := os.OpenFile(filepath.Join(s.Path, "state/events.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
		_, _ = f.WriteString(`{"partial":`)
		_ = f.Close()
		events, err := s.Events()
		if err != nil || len(events) != 1 {
			t.Fatalf("events=%v %v", events, err)
		}
		if _, err := s.Recover(); err != nil {
			t.Fatal(err)
		}
		events, _ = s.Events()
		if len(events) != 1 {
			t.Fatal("duplicate projection")
		}
	})
	t.Run("interior-corruption", func(t *testing.T) {
		s := fixture(t)
		put(t, s, "state/events.jsonl", "not JSON\n")
		if _, err := s.Recover(); err == nil {
			t.Fatal("interior corruption repaired")
		}
		m, _, _ := s.LoadSnapshot()
		if m["audit_pending"] != true {
			t.Fatal("audit_pending not set")
		}
		ok, err := s.EvidenceComplete()
		if err == nil || ok {
			t.Fatal("corrupt audit complete")
		}
	})
	t.Run("projection-failure", func(t *testing.T) {
		s := fixture(t)
		tx := nextIntent(t, s)
		s.Fault = func(name string) error {
			if strings.HasPrefix(name, "events.") {
				return errors.New("audit unavailable")
			}
			return nil
		}
		if err := s.CommitTransaction(tx); err == nil {
			t.Fatal("failure hidden")
		}
		m, _, err := s.LoadSnapshot()
		if err != nil || m["audit_pending"] != true {
			t.Fatalf("%v %v", m["audit_pending"], err)
		}
		if ok, err := s.EvidenceComplete(); err != nil || ok {
			t.Fatalf("complete=%v %v", ok, err)
		}
		s.Fault = nil
		if _, err := s.Recover(); err != nil {
			t.Fatal(err)
		}
		if ok, err := s.EvidenceComplete(); err != nil || !ok {
			t.Fatalf("complete=%v %v", ok, err)
		}
	})
	t.Run("index-warning", func(t *testing.T) {
		s := fixture(t)
		if RegisterIndex(func() error { return os.WriteFile(filepath.Join(s.Path, "state"), []byte("index"), 0600) }) != "INDEX_UNAVAILABLE" {
			t.Fatal("missing warning")
		}
		if _, err := s.Recover(); err != nil {
			t.Fatal(err)
		}
	})
}

func activeIntentID(t *testing.T, s *Store) string {
	t.Helper()
	history, err := s.History()
	if err != nil {
		t.Fatal(err)
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Kind == "intent" {
			return history[i].ID
		}
	}
	t.Fatal("No active intent")
	return ""
}

func TestMutableSpecDoesNotChangeSavedStage(t *testing.T) {
	s := fixture(t)
	operation := resultOperation(t, s, false)
	if err := operation(); err != nil {
		t.Fatal(err)
	}
	put(t, s, "SPEC.md", "next planner edits in place")
	if _, err := s.History(); err != nil {
		t.Fatal("mutable spec changed saved history:", err)
	}
}
func TestStoreLimitsAndRecords(t *testing.T) {
	s := fixture(t)
	if _, err := s.StageText("state/logs/invalid", []byte{0xff}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := s.StageText("state/logs/large", make([]byte, TextLimit+1)); err == nil {
		t.Fatal("oversize accepted")
	}
	for _, b := range []string{`{"record_version":1,"record_version":1}`, `{"record_version":1} {}`, `{"record_version":1,"unknown":null}`} {
		if _, err := parseRecord("manifest", []byte(b)); err == nil {
			t.Fatal("invalid record accepted", b)
		}
	}
	put(t, s, "rounds/conflict.md", "keep")
	if _, err := s.StageText("rounds/conflict.md", []byte("replace")); err == nil {
		t.Fatal("immutable conflict accepted")
	}
}

func TestCheckpointAuthorityCannotBeStagedAsText(t *testing.T) {
	s := fixture(t)
	for _, path := range []string{"state/manifest.json", "state/owner.lock", "state/events.jsonl", "state/transactions/new.json", "state/inputs/answer.json", "state/turns/x/receipt.json"} {
		if _, err := s.StageText(path, []byte("text")); err == nil {
			t.Fatal("authority staged as text:", path)
		}
	}
	m, _, err := s.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.NewTransaction("initialize", m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, s.CommitTransaction(tx), "STATE_INVALID")
}

func TestPrivateRootArtifactStaging(t *testing.T) {
	s := fixture(t)
	b := []byte("state_dir = \"/owned/state\"\n")
	hash, e := s.StagePrivateText("gashki.config.toml", b)
	if e != nil || hash != contract.HashBytes(b) {
		t.Fatal(hash, e)
	}
	saved, e := s.ReadText("gashki.config.toml")
	if e != nil || string(saved) != string(b) {
		t.Fatal(e)
	}
	info, e := os.Stat(filepath.Join(s.Path, "gashki.config.toml"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
	if _, e = s.StagePrivateText("gashki.config.toml", []byte("changed")); e == nil {
		t.Fatal("immutable file replaced")
	}
	for _, path := range []string{"../outside", ".", "/outside"} {
		if _, e = s.StagePrivateText(path, b); e == nil {
			t.Fatal("invalid leaf accepted", path)
		}
	}
}
