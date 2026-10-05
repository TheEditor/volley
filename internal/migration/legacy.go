//go:build darwin || linux

// Package migration protects legacy files and validates explicit resolutions.
package migration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/TheEditor/volley/internal/ops"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

func failure(code, message string) *contract.Error {
	r, _ := contract.Load()
	return r.Error(code, message)
}

type Classification struct {
	Kind    string
	Markers []store.FileObservation
}

func Classify(s *store.Store) (Classification, error) {
	c := Classification{Kind: "fresh", Markers: []store.FileObservation{}}
	state, err := s.Metadata("state")
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil || state.Kind != "directory" {
		return c, failure("STATE_INVALID", "State path is unsafe or unreadable")
	}
	_, err = s.Metadata("state/manifest.json")
	if err == nil {
		b, _, err := s.ReadRaw("state/manifest.json")
		if err != nil {
			return c, failure("STATE_INVALID", "Native manifest is unsafe or unreadable")
		}
		v, err := input.Record(bytes.NewReader(b), store.RecordLimit)
		if err != nil {
			return c, failure("STATE_INVALID", "Native manifest is malformed")
		}
		m, ok := v.(map[string]any)
		if !ok {
			return c, failure("STATE_INVALID", "Native manifest must be an object")
		}
		if version, ok := m["record_version"].(json.Number); ok {
			if _, err := version.Int64(); err == nil && version.String() != "1" {
				return c, failure("STATE_VERSION_UNSUPPORTED", "Native record version is not supported")
			}
		}
		if _, _, err = s.LoadSnapshot(); err != nil {
			return c, failure("STATE_INVALID", "Native manifest is invalid; preserve it and use read-only inspection")
		}
		c.Kind = "native"
		return c, nil
	}
	if !os.IsNotExist(err) {
		return c, failure("STATE_INVALID", "State path is unsafe or unreadable")
	}
	names, err := s.Names("state")
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, failure("STATE_INVALID", "State directory is unsafe or unreadable")
	}
	for _, name := range names {
		marker := name == "roles" || name == "persistent" || name == "backend" || name == "run" || name == "provenance.md" || strings.HasPrefix(name, "session.") && len(name) > len("session.")
		if !marker {
			continue
		}
		obs, err := s.Metadata("state/" + name)
		if err != nil {
			return c, err
		}
		if obs.Kind == "file" {
			if _, checked, e := s.ReadRaw(obs.Path); e == nil {
				obs = checked
			}
		}
		c.Markers = append(c.Markers, obs)
	}
	if len(c.Markers) > 0 {
		c.Kind = "legacy"
	}
	return c, nil
}
func Recipe(path string) []string {
	return []string{
		"Settle old execution and preserve all old files. This report does not query or clean up a pane.",
		"Copy required brief, constraints and decided human requirements to a fresh workspace. Do not copy old state, sessions or question generations.",
		ops.Command("volley", "run", "NEW_WORKSPACE", "--seed", filepath.Join(path, "SPEC.md"), "--config", "NEW_SETTINGS.toml"),
		"The new review starts at critic round 1 without old session memory. Native import and legacy session reuse are deferred.",
	}
}
func CheckStart(path string) error {
	s, err := store.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer s.Close()
	c, err := Classify(s)
	if err != nil {
		var e *contract.Error
		if errors.As(err, &e) {
			e.Path = &s.Path
			command := ops.Command("volley", "doctor", s.Path)
			e.Remediation = &command
			e.Evidence = map[string]any{"recipe": Recipe(s.Path), "preserve": "Keep broken state. Restore a verified workspace copy or use a fresh workspace without old state or sessions."}
		}
		return err
	}
	if c.Kind == "legacy" {
		e := failure("LEGACY_BOUNDARY_UNVERIFIED", "Legacy workspace cannot start a native review")
		e.Path = &s.Path
		report := ops.Command("volley", "workspace", "legacy-report", s.Path)
		e.Remediation = &report
		e.Evidence = map[string]any{"classification": "legacy", "recipe": Recipe(s.Path)}
		return e
	}
	return nil
}

func CheckOutputs(path string, seed bool, cap int, advisory, closing bool) error {
	s, err := store.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer s.Close()
	c, err := Classify(s)
	if err != nil {
		return err
	}
	if c.Kind != "fresh" {
		return nil
	}
	if o, err := s.Metadata("SPEC.md"); err == nil && o.Kind == "file" {
		seed = true
	}
	names, err := s.Names("rounds")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return failure("OUTPUT_CONFLICT", "Round output directory is unsafe")
	}
	for _, name := range names {
		planned := name == "second-opinion.md" && advisory
		if closing && cap > 1 {
			switch name {
			case "closing-approved.spec.md", "closing.spec.md", "closing-response.md", "closing-rejected.spec.md":
				planned = true
			}
		}
		if strings.HasPrefix(name, "r") {
			i := 1
			for i < len(name) && name[i] >= '0' && name[i] <= '9' {
				i++
			}
			var round int
			if i > 1 {
				fmt.Sscan(name[1:i], &round)
			}
			if round > 0 && round <= cap {
				switch name[i:] {
				case ".critique.md", ".critique-retry.md", ".verdict-missing.md":
					planned = true
				case ".spec.md", ".response.md":
					planned = round > 1 || !seed
				}
			}
		}
		if planned {
			return failure("OUTPUT_CONFLICT", "Existing untrusted history conflicts with a planned native output: rounds/"+name)
		}
	}
	return nil
}
func record(o store.FileObservation) map[string]any {
	return map[string]any{"path": o.Path, "sha256": o.Hash, "bytes": o.Bytes, "type": o.Kind, "device": o.Device, "inode": o.Inode}
}
func Report(path string) (map[string]any, error) {
	s, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	c, err := Classify(s)
	if err != nil {
		return nil, err
	}
	markers := []any{}
	for _, o := range c.Markers {
		markers = append(markers, record(o))
	}
	facts := []any{}
	unknown := []string{"Live process and pane ownership are not checked", "Old verdicts have no native artifact-bound approval receipt", "Native import and legacy session reuse are deferred"}
	paths := []string{"BRIEF.md", "SPEC.md", "CONSTRAINTS.md", "QUESTIONS.md", "HUMAN.md"}
	for _, o := range c.Markers {
		paths = append(paths, o.Path)
	}
	if names, err := s.Names("rounds"); err == nil {
		for _, name := range names {
			paths = append(paths, "rounds/"+name)
		}
	} else if !os.IsNotExist(err) {
		unknown = append(unknown, "Round directory is unsafe or unreadable")
	}
	sort.Strings(paths)
	for _, path := range paths {
		obs, err := s.Metadata(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if obs.Kind != "file" {
			unknown = append(unknown, "Unsafe or non-regular artifact: "+path)
			continue
		}
		b, o, err := s.ReadRaw(path)
		if err != nil {
			unknown = append(unknown, "Source bytes and hash could not be checked: "+path)
			continue
		}
		value := "Retained historical artifact; no native authority"
		if utf8.Valid(b) {
			switch {
			case path == "state/roles":
				v := strings.TrimSpace(string(b))
				if v == "planner=claude" || v == "planner=codex" {
					value = v
				} else {
					value = "Unknown or partial role record"
				}
			case path == "state/persistent":
				v := strings.TrimSpace(string(b))
				if v == "0" || v == "1" {
					value = "Legacy persistent=" + v
				} else {
					value = "Unknown or partial persistence record"
				}
			case path == "state/backend":
				v := strings.TrimSpace(string(b))
				if v == "cli" || v == "gashki" {
					value = "Legacy backend=" + v
				} else {
					value = "Unknown or partial backend record"
				}
			case path == "state/run" || strings.HasPrefix(path, "state/session."):
				value = strings.TrimSpace(string(b))
				if len(value) > 4096 {
					value = "Oversized legacy identity; not adopted"
				}
			case strings.Contains(filepath.Base(path), "critique"):
				v := review.ParseVerdict(string(b)).Value
				if v == "MISSING" && strings.TrimSpace(string(b)) == "APPROVE" {
					v = "APPROVE"
				}
				value = "Historical verdict: " + v + "; not a native approval"
			case path == "QUESTIONS.md":
				if len(bytes.TrimSpace(b)) > 0 {
					value = "Historical question text remains; no native generation"
				} else {
					value = "Empty historical question file"
				}
			}
		} else {
			value = "Non-text historical bytes; not interpreted"
		}
		facts = append(facts, map[string]any{"name": path, "value": value, "sha256": o.Hash})
	}
	return map[string]any{"workspace": s.Path, "classification": c.Kind, "markers": markers, "facts": facts, "unknown": unknown, "active_execution": "unknown; no old process or pane was queried", "recipe": Recipe(s.Path)}, nil
}
