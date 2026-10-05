//go:build darwin || linux

package ops

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
	"github.com/TheEditor/volley/internal/store"
)

func manifestFixture(t *testing.T) store.Snapshot {
	t.Helper()
	b, err := contract.Schema("manifest")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	json.Unmarshal(b, &schema)
	var sample func(map[string]any) any
	sample = func(s map[string]any) any {
		if v, ok := s["const"]; ok {
			return v
		}
		if v, ok := s["enum"].([]any); ok {
			return v[0]
		}
		if v, ok := s["anyOf"].([]any); ok {
			for _, c := range v {
				if c.(map[string]any)["type"] == "null" {
					return nil
				}
			}
			return sample(v[0].(map[string]any))
		}
		switch s["type"] {
		case "object":
			m := map[string]any{}
			p := s["properties"].(map[string]any)
			for _, k := range s["required"].([]any) {
				m[k.(string)] = sample(p[k.(string)].(map[string]any))
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
	return store.Snapshot(sample(schema).(map[string]any))
}
func fixture(t *testing.T, status string) (*store.Store, store.Snapshot) {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err = s.AcquireOwner(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	m := manifestFixture(t)
	id, _ := store.NewID()
	obs, err := s.RootObservation()
	if err != nil {
		t.Fatal(err)
	}
	m["run_id"] = id
	m["canonical_workspace"] = s.Path
	m["workspace"] = s.Path
	m["ownership"] = map[string]any{"device": obs.Device, "inode": obs.Inode}
	m["created_at"] = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	m["status"] = status
	m["phase"] = "critique"
	m["round"] = 1
	m["max_rounds"] = 3
	commit(t, s, m, "initialize")
	_, hash, err := s.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture run_id=%s target_os=%s artifact_manifest_sha256=%s agent_launches=0 settings=not_prepared", id, runtime.GOOS, hash)
	return s, m
}
func commit(t *testing.T, s *store.Store, m store.Snapshot, kind string) {
	t.Helper()
	tx, err := s.NewTransaction(kind, m, nil, nil)
	if err == nil {
		err = s.CommitTransaction(tx)
	}
	if err != nil {
		t.Fatal(err)
	}
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var e *contract.Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}
func handle(t *testing.T, o Options, r Request) map[string]any {
	t.Helper()
	d, err := o.Handle(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	schema := map[string]string{"status": "data-status", "runs get": "data-runs-get", "runs list": "data-runs-list", "runs events": "event-page", "runs stop": "data-runs-stop", "runs prune": "data-runs-prune", "human questions": "data-human-questions", "doctor": "data-doctor"}[r.Command]
	if err = contract.Validate(schema, d); err != nil {
		t.Fatal(err)
	}
	b, _ := contract.Canonical(d)
	t.Logf("observed command=%s data_sha256=%s", r.Command, contract.HashBytes(b))
	return d
}
func tree(t *testing.T, root string) string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		m[rel] = contract.HashBytes(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := contract.Canonical(m)
	return contract.HashBytes(b)
}

func TestAOPS01IndexAndLists(t *testing.T) {
	o := Options{IndexDir: filepath.Join(t.TempDir(), "index")}
	s, m := fixture(t, "ready")
	path := s.Path
	s.Close()
	before := tree(t, path)
	handle(t, o, Request{Command: "status", Selector: path})
	if before != tree(t, path) {
		t.Fatal("read changed workspace")
	}
	if _, err := os.Stat(o.IndexDir); !os.IsNotExist(err) {
		t.Fatal("read created index")
	}
	if err := o.Register(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if resolved, err := o.Resolve(m.String("run_id")); err != nil || resolved != path {
		t.Fatal(resolved, err)
	}
	for range 25 {
		s, m := fixture(t, "approved")
		s.Close()
		if err := o.Register(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	first := handle(t, o, Request{Command: "runs list", Fields: "status"})
	if len(first["items"].([]any)) != 25 || first["truncated"] != true {
		t.Fatal(first)
	}
	for _, v := range first["items"].([]any) {
		if len(v.(map[string]any)) != 3 {
			t.Fatal(v)
		}
	}
	cursor := first["cursor"].(string)
	second := handle(t, o, Request{Command: "runs list", Fields: "status", Cursor: cursor})
	if len(second["items"].([]any)) != 1 || second["truncated"] != false {
		t.Fatal(second)
	}
	for _, r := range []Request{{Command: "runs list", Cursor: cursor, Fields: "revision"}, {Command: "runs list", Cursor: cursor, Fields: "status", Filter: "ready"}, {Command: "runs list", Limit: 101}, {Command: "runs list", Fields: "bogus"}} {
		_, err := o.Handle(context.Background(), r)
		code(t, err, "INVALID_INPUT")
	}
	other := Options{IndexDir: filepath.Join(t.TempDir(), "other")}
	_, err := other.Handle(context.Background(), Request{Command: "runs list", Fields: "status", Cursor: cursor})
	code(t, err, "INVALID_INPUT")
	if err = os.Rename(path, path+"-moved"); err != nil {
		t.Fatal(err)
	}
	_, err = o.Resolve(m.String("run_id"))
	code(t, err, "NOT_FOUND")
	// A replacement at the old path cannot inherit index authority.
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	_, err = o.Resolve(m.String("run_id"))
	if err == nil {
		t.Fatal("stale index selected replacement")
	}
}

func TestAOPS02ReadAndProbe(t *testing.T) {
	s, m := fixture(t, "impasse")
	path := s.Path
	s.Close()
	o := Options{IndexDir: filepath.Join(t.TempDir(), "index")}
	before := tree(t, path)
	for _, command := range []string{"status", "runs get", "human questions", "doctor"} {
		handle(t, o, Request{Command: command, Selector: path})
	}
	if before != tree(t, path) {
		t.Fatal("default inspection changed records")
	}
	d := handle(t, o, Request{Command: "status", Selector: path})
	a := d["recommended_action"].(map[string]any)
	if !strings.Contains(a["command"].(string), "'--max-rounds' '4'") {
		t.Fatal(a)
	}
	defaultProbe := handle(t, o, Request{Command: "status", Selector: path, Probe: true})
	if len(defaultProbe["probes"].([]Probe)) != 2 {
		t.Fatal(defaultProbe)
	}
	o.Probe = func(ctx context.Context, s *store.Store, m store.Snapshot) ([]Probe, error) {
		if err := s.AcquireOwner(ctx, time.Millisecond); err != nil {
			return nil, err
		}
		tx, err := s.NewTransaction("control", m, nil, nil)
		if err == nil {
			err = s.CommitTransaction(tx)
		}
		return []Probe{{"owned observation", "Appends a checked observation transaction", true, "Observed fixture state"}}, err
	}
	handle(t, o, Request{Command: "status", Selector: path, Probe: true})
	if before == tree(t, path) {
		t.Fatal("explicit probe did not append observation")
	}
	if Command("volley", "status", "a'b;$HOME") != "'volley' 'status' 'a'\"'\"'b;$HOME'" {
		t.Fatal("unsafe display quoting")
	}
	m["status"] = "handover"
	m["recovery"] = map[string]any{"code": "SEND_UNCERTAIN", "missing_receipt": true, "bindings_verified": true}
	if !strings.Contains(action(m, "absent")["command"].(string), "'runs' 'resume'") {
		t.Fatal("checked missing receipt did not recommend same-key resume")
	}
	m.Object("recovery")["bindings_verified"] = false
	if strings.Contains(action(m, "absent")["command"].(string), "'runs' 'resume'") {
		t.Fatal("unverified uncertainty recommended automatic recovery")
	}
}

func TestAOPS03EventsAndReadWait(t *testing.T) {
	s, m := fixture(t, "running")
	o := Options{IndexDir: filepath.Join(t.TempDir(), "index")}
	r := Request{Command: "runs events", Selector: s.Path}
	first := handle(t, o, r)
	cursor := first["cursor"].(string)
	r.Cursor = cursor
	r.Wait = true
	r.Timeout = 40 * time.Millisecond
	quiet := handle(t, o, r)
	if len(quiet["events"].([]map[string]any)) != 0 || quiet["cursor"] != cursor {
		t.Fatal(quiet)
	}
	appended := make(chan error, 1)
	r.Timeout = 2 * time.Second
	go func() {
		tx, err := s.NewTransaction("control", m, nil, nil)
		if err == nil {
			err = s.CommitTransaction(tx)
		}
		appended <- err
	}()
	next := handle(t, o, r)
	if err := <-appended; err != nil {
		t.Fatal(err)
	}
	if len(next["events"].([]map[string]any)) != 1 || next["cursor"] == cursor {
		t.Fatal(next)
	}
	other, _ := fixture(t, "ready")
	_, err := o.Handle(context.Background(), Request{Command: "runs events", Selector: other.Path, Cursor: cursor})
	code(t, err, "INVALID_INPUT")
	_, err = o.Handle(context.Background(), Request{Command: "runs events", Selector: s.Path, Cursor: "bad"})
	code(t, err, "INVALID_INPUT")
	_, err = o.Handle(context.Background(), Request{Command: "runs get", Selector: s.Path, Wait: true, Timeout: 30 * time.Millisecond})
	code(t, err, "WAIT_TIMEOUT")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = o.Handle(ctx, Request{Command: "runs get", Selector: s.Path, Wait: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.ObserveOwner() != "active" {
		t.Fatal("reader stopped owner")
	}
	commit(t, s, m, "control")
	path := s.Path
	s.Close()
	d := handle(t, o, Request{Command: "runs get", Selector: path, Wait: true})
	if d["owner"] != "absent" {
		t.Fatal(d)
	}
}

func TestAOPS04StopAndPrune(t *testing.T) {
	o := Options{IndexDir: filepath.Join(t.TempDir(), "index")}
	s, m := fixture(t, "ready")
	path := s.Path
	s.Close()
	if err := o.Register(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	before := tree(t, path)
	handle(t, o, Request{Command: "runs stop", Selector: path, DryRun: true})
	if before != tree(t, path) {
		t.Fatal("dry stop wrote data")
	}
	_, err := o.Handle(context.Background(), Request{Command: "runs stop", Selector: path})
	code(t, err, "ACK_REQUIRED")
	stopped := handle(t, o, Request{Command: "runs stop", Selector: path, Yes: true})
	if stopped["status"] != "stopped" || stopped["control_receipt"].(map[string]any)["applied"] != true {
		t.Fatal(stopped)
	}
	before = tree(t, path)
	dry := handle(t, o, Request{Command: "runs prune", OlderThan: time.Hour, DryRun: true})
	if len(dry["effects"].([]any)) != 1 || len(dry["removed"].([]any)) != 0 {
		t.Fatal(dry)
	}
	for _, variant := range []string{"active", "cleanup", "question", "uncertain"} {
		s, m := fixture(t, "approved")
		if variant == "cleanup" {
			m.Object("cleanup")["pending"] = true
		}
		if variant == "question" {
			m["status"] = "awaiting_answer"
		}
		if variant == "uncertain" {
			m["status"] = "handover"
		}
		commit(t, s, m, "control")
		if variant != "active" {
			s.Close()
		}
		if err := o.Register(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	pruned := handle(t, o, Request{Command: "runs prune", OlderThan: time.Hour, Yes: true})
	if len(pruned["removed"].([]any)) != 1 || before != tree(t, path) {
		t.Fatal(pruned, "prune changed workspace")
	}
	handle(t, o, Request{Command: "status", Selector: path})
	_, err = o.Resolve(m.String("run_id"))
	code(t, err, "NOT_FOUND")
}

func TestAOPS04CooperativeOwner(t *testing.T) {
	s, m := fixture(t, "running")
	o := Options{IndexDir: filepath.Join(t.TempDir(), "index")}
	done := make(chan error, 1)
	go func() {
		_, err := o.Handle(context.Background(), Request{Command: "runs stop", Selector: s.Path, Yes: true})
		done <- err
	}()
	end := time.Now().Add(time.Second)
	for {
		inbox, err := s.SavedInbox(m.String("run_id"))
		if err != nil {
			t.Fatal(err)
		}
		if len(inbox.Entries) > 0 {
			entry := inbox.Entries[0]
			m["status"] = "stopped"
			m["stop_control"] = map[string]any{"path": entry.ReceiptPath, "sha256": entry.ReceiptHash, "applied": true}
			commit(t, s, m, "control")
			break
		}
		if time.Now().After(end) {
			t.Fatal("no control submission")
		}
		time.Sleep(time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.ObserveOwner() != "active" {
		t.Fatal("operator killed or released owner")
	}
	t.Log(fmt.Sprint("Cooperative acknowledgment; no PID or pane kill"))
}

func TestAOPS04UnknownTargets(t *testing.T) {
	s, m := fixture(t, "ready")
	m["retention"] = map[string]any{"kind": "retain", "reason": "Unknown target identity", "panes": []string{"foreign-pane"}, "processes": []any{map[string]any{"pid": 1, "pgid": 1, "start_identity": "unknown", "verified": false}}}
	commit(t, s, m, "control")
	path := s.Path
	s.Close()
	d := handle(t, Options{}, Request{Command: "runs stop", Selector: path, Yes: true})
	if d["retention"].(map[string]any)["reason"] != "Unknown target identity" {
		t.Fatal(d)
	}
	s, _ = fixture(t, "ready")
	path = s.Path
	s.Close()
	if err := os.Remove(filepath.Join(path, "state/owner.lock")); err != nil {
		t.Fatal(err)
	}
	before := tree(t, path)
	_, err := (Options{}).Handle(context.Background(), Request{Command: "runs stop", Selector: path, Yes: true})
	code(t, err, "LOCKED")
	if before != tree(t, path) {
		t.Fatal("unknown owner stop wrote records")
	}
}
