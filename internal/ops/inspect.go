//go:build darwin || linux

// Package ops reads committed review state and manages the derived run index.
package ops

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
	"golang.org/x/sys/unix"
)

type Options struct {
	IndexDir   string
	Env        []string
	ConfigFile string
	Now        func() time.Time
	// Probe is an explicit dependency seam. Default inspection executes no tool.
	Probe func(context.Context, *store.Store, store.Snapshot) ([]Probe, error)
}
type Probe struct {
	Name    string `json:"name"`
	Effect  string `json:"effect"`
	Checked bool   `json:"checked"`
	Detail  string `json:"detail"`
}
type Request struct {
	Command, Selector, Cursor, Fields, Filter string
	Limit                                     int
	Wait, Probe, DryRun, Yes                  bool
	Timeout, OlderThan                        time.Duration
}

func failure(code, message string) error { r, _ := contract.Load(); return r.Error(code, message) }
func StateDir(env []string) string {
	value := func(k string) string {
		for _, v := range env {
			if strings.HasPrefix(v, k+"=") {
				return strings.TrimPrefix(v, k+"=")
			}
		}
		return ""
	}
	if p := value("XDG_STATE_HOME"); p != "" {
		return filepath.Join(p, "volley")
	}
	if p := value("HOME"); p != "" {
		return filepath.Join(p, ".local/state/volley")
	}
	return ""
}
func IsID(v string) bool {
	if len(v) != 26 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}

// Command quotes each argument separately. It is display text, never executed.
func Command(args ...string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\"'\"'") + "'"
	}
	return strings.Join(quoted, " ")
}
func recommendation(command, rationale string) map[string]any {
	return map[string]any{"command": command, "rationale": rationale, "is_destructive": false, "alternatives": []any{}}
}
func action(m store.Snapshot, owner string) map[string]any {
	path := m.String("canonical_workspace")
	command := Command("volley", "status", path)
	reason := "Inspect the saved state before another action."
	switch m.String("status") {
	case "approved":
		reason = "Read the approved specification and saved review basis."
	case "stopped":
		reason = "This run is stopped. Keep its records and use a fresh workspace."
	case "impasse":
		command = Command("volley", "runs", "resume", path, "--max-rounds", fmt.Sprint(number(m["max_rounds"])+1))
		reason = "Increase the round cap to continue this review."
	case "awaiting_answer":
		command = Command("volley", "human", "questions", path)
		reason = "Read the current question before submitting its answer."
	case "handover":
		reason = "Inspect the uncertain turn and its records before resolution. Do not create a new send key."
		// Only a checked recovery record can recommend automatic same-key resume.
		if recovery := m.Object("recovery"); recovery["code"] == "SEND_UNCERTAIN" && recovery["missing_receipt"] == true && recovery["bindings_verified"] == true {
			command = Command("volley", "runs", "resume", path)
			reason = "Resume the checked missing receipt with the recorded key."
		}
	default:
		if owner == "absent" {
			command = Command("volley", "runs", "resume", path)
			reason = "The controller is absent. Resume from saved state."
		}
	}
	return recommendation(command, reason)
}
func number(v any) int { var n int; fmt.Sscan(fmt.Sprint(v), &n); return n }
func read(s *store.Store) (store.Snapshot, error) {
	for range 4 {
		m, h, err := s.LoadSnapshot()
		if err != nil {
			return nil, err
		}
		_, err = s.History()
		if err != nil {
			return nil, err
		}
		_, after, err := s.LoadSnapshot()
		if err != nil {
			return nil, err
		}
		if h == after {
			return m, nil
		}
	}
	return nil, failure("LOCKED", "The checkpoint changed during inspection. Try again.")
}
func (o Options) Resolve(selector string) (string, error) {
	if !IsID(selector) {
		return filepath.Abs(selector)
	}
	idx, err := o.loadIndex()
	if err != nil {
		return "", err
	}
	for _, e := range idx.Entries {
		if e.RunID == selector {
			s, err := store.Open(e.Workspace)
			if err != nil {
				return "", failure("NOT_FOUND", "Indexed workspace is unavailable")
			}
			defer s.Close()
			m, err := read(s)
			if err != nil {
				return "", err
			}
			if s.Path != e.Workspace || m.String("run_id") != selector {
				return "", failure("STATE_INVALID", "Index identity differs from the workspace")
			}
			return s.Path, nil
		}
	}
	return "", failure("NOT_FOUND", "Run ID is not in the derived index")
}
func data(s *store.Store, m store.Snapshot) map[string]any {
	var q any
	if question := m.Object("question"); len(question) > 0 {
		q = question["id"]
	}
	owner := s.ObserveOwner()
	a := action(m, owner)
	value := map[string]any{"run_id": m["run_id"], "workspace": s.Path, "status": m["status"], "phase": m["phase"], "round": m["round"], "max_rounds": m["max_rounds"], "turn": m["current_turn"], "question_id": q, "spec_hash": m["spec_hash"], "owner": owner, "retention": m["retention"], "next_action": a["rationale"], "recommended_action": a}
	if warnings, ok := m["warnings"]; ok {
		value["warning_records"] = warnings
	}
	return value
}
func probes(ctx context.Context, o Options, s *store.Store, m store.Snapshot) ([]Probe, error) {
	if o.Probe != nil {
		return o.Probe(ctx, s, m)
	}
	return []Probe{{"vendor authentication", "May execute a vendor identity check; no check was requested from an adapter", false, "not checked"}, {"Gashki pane state", "An observe call can append Gashki observation events", false, "not checked"}}, nil
}
func pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(25 * time.Millisecond):
		return nil
	}
}
func (o Options) Handle(ctx context.Context, r Request) (map[string]any, error) {
	if r.Limit == 0 {
		r.Limit = 25
	}
	if r.Limit < 1 || r.Limit > 100 || r.Timeout < 0 {
		return nil, failure("INVALID_INPUT", "Limit must be 1 through 100 and timeout must be non-negative")
	}
	if r.Command == "runs list" {
		return o.List(r)
	}
	if r.Command == "runs prune" {
		return o.Prune(ctx, r)
	}
	if r.Command == "doctor" && r.Selector == "" {
		return o.doctor(ctx, r, nil, nil)
	}
	path, err := o.Resolve(r.Selector)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, failure("NOT_FOUND", "Workspace is absent")
		}
		return nil, err
	}
	defer s.Close()
	m, err := read(s)
	if err != nil {
		return nil, err
	}
	if r.Command == "runs stop" {
		return o.stop(ctx, r, s, m)
	}
	if r.Command == "runs events" {
		return o.events(ctx, r, s, m)
	}
	if r.Command == "doctor" {
		return o.doctor(ctx, r, s, m)
	}
	if r.Command == "human questions" {
		var question any
		if q := m.Object("question"); len(q) > 0 {
			question = map[string]any{"id": q["id"], "sha256": q["sha256"], "text": q["text"], "observed": q["observed"], "answered": q["answered"]}
		}
		return map[string]any{"run_id": m["run_id"], "workspace": s.Path, "question": question}, nil
	}
	end := time.Time{}
	if r.Timeout > 0 {
		end = time.Now().Add(r.Timeout)
	}
	for {
		result := data(s, m)
		if !r.Wait || terminal(m.String("status")) || m.String("status") == "handover" || m.String("status") == "awaiting_answer" || result["owner"] != "active" {
			if r.Probe {
				result["probes"], err = probes(ctx, o, s, m)
			}
			return result, err
		}
		if !end.IsZero() && !time.Now().Before(end) {
			return result, failure("WAIT_TIMEOUT", "Inspection wait expired; the run was not stopped")
		}
		if err = pause(ctx); err != nil {
			return result, err
		}
		m, err = read(s)
		if err != nil {
			return result, err
		}
	}
}
func terminal(status string) bool {
	return status == "approved" || status == "impasse" || status == "stopped"
}

type eventCursor struct {
	RunID    string `json:"run_id"`
	Sequence int    `json:"sequence"`
	Hash     string `json:"hash"`
}

func encodeCursor(v any) string {
	b, _ := contract.Canonical(v)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeCursor(text string, v any) error {
	if len(text) > 8192 {
		return failure("INVALID_INPUT", "Cursor is too large")
	}
	b, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return failure("INVALID_INPUT", "Invalid cursor")
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return failure("INVALID_INPUT", "Invalid cursor")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return failure("INVALID_INPUT", "Cursor has trailing data")
	}
	return nil
}
func (o Options) events(ctx context.Context, r Request, s *store.Store, m store.Snapshot) (map[string]any, error) {
	c := eventCursor{RunID: m.String("run_id")}
	if r.Cursor != "" {
		if err := decodeCursor(r.Cursor, &c); err != nil {
			return nil, err
		}
	}
	end := time.Time{}
	if r.Timeout > 0 {
		end = time.Now().Add(r.Timeout)
	}
	for {
		events, err := s.Events()
		if err != nil {
			return nil, err
		}
		if c.RunID != m.String("run_id") || c.Sequence < 0 || c.Sequence > len(events) {
			return nil, failure("INVALID_INPUT", "Cursor does not select this event history")
		}
		h := ""
		if c.Sequence > 0 {
			b, _ := contract.Canonical(events[c.Sequence-1])
			h = contract.HashBytes(b)
		}
		if h != c.Hash {
			return nil, failure("INVALID_INPUT", "Cursor event generation differs")
		}
		n := min(len(events), c.Sequence+r.Limit)
		page := events[c.Sequence:n]
		if n > c.Sequence {
			b, _ := contract.Canonical(events[n-1])
			c.Hash = contract.HashBytes(b)
			c.Sequence = n
		}
		result := map[string]any{"run_id": c.RunID, "events": page, "cursor": encodeCursor(c), "truncated": n < len(events)}
		if len(page) > 0 || !r.Wait || !end.IsZero() && !time.Now().Before(end) {
			return result, nil
		}
		if err = pause(ctx); err != nil {
			return result, err
		}
	}
}
func (o Options) doctor(ctx context.Context, r Request, s *store.Store, m store.Snapshot) (map[string]any, error) {
	reg, _ := contract.Load()
	settings := map[string]any{}
	for _, v := range reg.Settings {
		settings[v.Key] = v.Default
	}
	a := recommendation(Command("volley", "doctor"), "Inspect settings and dependency records before starting a review.")
	checks := []any{}
	warnings := []string{}
	if s != nil {
		a = action(m, s.ObserveOwner())
		checks = append(checks, map[string]any{"name": "committed state", "ok": true, "detail": "Checkpoint and transaction history are valid; no repair was made", "recommended_action": a})
		if p, ok := m.Object("config_records")["volley"].(map[string]any); ok && p["path"] != "" {
			path, _ := p["path"].(string)
			rel, e := filepath.Rel(s.Path, path)
			if e != nil {
				return nil, e
			}
			b, e := s.ReadText(rel)
			if e != nil {
				return nil, e
			}
			if contract.HashBytes(b) != p["sha256"] {
				return nil, failure("STATE_INVALID", "Saved settings hash differs")
			}
			doc, e := config.Parse(path, b)
			if e != nil {
				return nil, e
			}
			settings = doc.Values
		} else {
			checks = append(checks, map[string]any{"name": "saved settings", "ok": false, "detail": "Settings preparation is not recorded; displayed values are defaults", "recommended_action": a})
		}
		cleanup := m.Object("cleanup")
		pending := cleanup["pending"] == true
		checks = append(checks, map[string]any{"name": "cleanup", "ok": !pending, "detail": fmt.Sprintf("Saved cleanup pending: %t", pending), "recommended_action": a})
		for _, name := range []string{"claude", "codex", "gashki"} {
			if name == "gashki" && settings["backend"] != "gashki" {
				continue
			}
			binding, _ := m.Object("executables")[name].(map[string]any)
			path, _ := binding["path"].(string)
			hash, _ := binding["sha256"].(string)
			ok, detail := checkDependency(path, hash)
			checks = append(checks, map[string]any{"name": name + " executable binding", "ok": ok, "detail": detail, "recommended_action": a})
		}
	} else {
		env := o.Env
		if env == nil {
			env = os.Environ()
		}
		value := func(k string) string {
			for _, v := range env {
				if strings.HasPrefix(v, k+"=") {
					return strings.TrimPrefix(v, k+"=")
				}
			}
			return ""
		}
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		resolved, _, err := config.Resolve(config.ResolveOptions{Cwd: cwd, Home: value("HOME"), XDGRoot: value("XDG_CONFIG_HOME"), NamedFile: o.ConfigFile})
		if err != nil {
			return nil, err
		}
		b, _ := contract.Canonical(resolved.Settings)
		json.Unmarshal(b, &settings)
		checks = append(checks, map[string]any{"name": "settings", "ok": true, "detail": "Selected settings are valid; no external dependency was executed", "recommended_action": a})
	}
	env := o.Env
	if env == nil {
		env = os.Environ()
	}
	retired, err := config.RetiredWarnings(func(k string) bool {
		for _, v := range env {
			if strings.HasPrefix(v, k+"=") {
				return true
			}
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	for _, w := range retired {
		warnings = append(warnings, w.Code)
	}
	checks = append(checks, map[string]any{"name": "external dependencies", "ok": false, "detail": "Vendor authentication and live pane state: not checked", "recommended_action": a})
	result := map[string]any{"checks": checks, "recommended_action": a, "settings": settings, "warnings": warnings}
	if r.Probe && s != nil {
		p, err := probes(ctx, o, s, m)
		if err != nil {
			return nil, err
		}
		result["probes"] = p
	}
	return result, nil
}

func checkDependency(path, expected string) (bool, string) {
	if path == "" || expected == "" {
		return false, "Executable identity: not recorded"
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, "Saved executable is unavailable"
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return false, "Saved executable is not an executable regular file"
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (1<<30)+1))
	if err != nil || n > 1<<30 {
		return false, "Executable hash could not be checked"
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		return false, "Executable bytes differ from the saved binding"
	}
	return true, "Saved executable hash matches; no process was started"
}
