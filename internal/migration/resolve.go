//go:build darwin || linux

package migration

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/TheEditor/volley/internal/store"
)

type Resolution struct {
	Resolution    string   `json:"resolution"`
	ArtifactPaths []string `json:"artifact_paths"`
	EvidencePaths []string `json:"evidence_paths"`
	Note          string   `json:"note"`
}
type CheckedEvidence struct {
	ArtifactsChecked, OwnershipChecked, IndependentCompletion bool
	NoDelivery, NoProcessStart                                bool
	Artifacts, Evidence                                       []store.FileObservation
}

// Implementations must check records and bindings. User text grants no authority.
type TurnEvidence interface {
	Check(context.Context, *store.Store, store.Snapshot, Resolution) (CheckedEvidence, error)
}
type SessionSettlement interface {
	Check(context.Context, *store.Store, store.Snapshot, CheckedEvidence) error
}
type ResolutionOptions struct {
	Evidence TurnEvidence
	Sessions SessionSettlement
	Apply    func(context.Context, *store.Store, store.Snapshot, Resolution, CheckedEvidence, store.ReceiptRef) error
}

func ParseResolution(r io.Reader) (Resolution, error) {
	var x Resolution
	m, err := input.Object(r)
	if err != nil {
		return x, failure("INVALID_INPUT", err.Error())
	}
	if err = contract.Validate("resolution", m); err != nil {
		return x, failure("INVALID_INPUT", err.Error())
	}
	if len(m) != 4 {
		return x, failure("INVALID_INPUT", "Resolution requires exactly four fields")
	}
	var ok bool
	x.Resolution, ok = m["resolution"].(string)
	if !ok {
		return x, failure("INVALID_INPUT", "Resolution must be a string")
	}
	switch x.Resolution {
	case "accept_completed", "confirm_not_executed", "abandon":
	default:
		return x, failure("INVALID_INPUT", "Unknown resolution")
	}
	x.Note, ok = m["note"].(string)
	if !ok || strings.ContainsRune(x.Note, 0) {
		return x, failure("INVALID_INPUT", "Note must be text without NUL")
	}
	for name, target := range map[string]*[]string{"artifact_paths": &x.ArtifactPaths, "evidence_paths": &x.EvidencePaths} {
		values, ok := m[name].([]any)
		if !ok {
			return x, failure("INVALID_INPUT", name+" must be an array")
		}
		*target = []string{}
		seen := map[string]bool{}
		for _, v := range values {
			path, ok := v.(string)
			if !ok || path == "" || strings.ContainsRune(path, 0) || seen[path] {
				return x, failure("INVALID_INPUT", "Paths must be unique nonempty strings without NUL")
			}
			seen[path] = true
			*target = append(*target, path)
		}
	}
	return x, nil
}
func CheckResolution(ctx context.Context, s *store.Store, m store.Snapshot, x Resolution, o ResolutionOptions) (CheckedEvidence, string, error) {
	var e CheckedEvidence
	if x.Resolution == "abandon" {
		return e, "none", nil
	}
	if o.Evidence == nil || o.Sessions == nil {
		return e, "", failure("TURN_UNCERTAIN", "No checked backend evidence is available")
	}
	e, err := o.Evidence.Check(ctx, s, m, x)
	if err != nil {
		return e, "", err
	}
	if !e.OwnershipChecked {
		return e, "", failure("TURN_UNCERTAIN", "Turn ownership is not checked")
	}
	if err = o.Sessions.Check(ctx, s, m, e); err != nil {
		return e, "", err
	}
	if x.Resolution == "confirm_not_executed" {
		if !e.NoDelivery || !e.NoProcessStart {
			return e, "", failure("TURN_UNCERTAIN", "Independent evidence must prove no delivery and no process start")
		}
		return e, "none", nil
	}
	if !e.ArtifactsChecked {
		return e, "", failure("ARTIFACT_CHANGED", "Completed artifacts are not checked")
	}
	source := "human_attested"
	if e.IndependentCompletion {
		source = "backend_confirmed"
	}
	return e, source, nil
}

// Resolve holds the native owner lock. It does not execute an agent operation.
func Resolve(ctx context.Context, s *store.Store, turn string, x Resolution, o ResolutionOptions) (store.Snapshot, error) {
	if err := s.AcquireOwner(ctx, 0); err != nil {
		return nil, err
	}
	if _, err := s.Recover(); err != nil {
		return nil, err
	}
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return nil, err
	}
	if m.Object("current_turn")["id"] != turn {
		return m, failure("CONFIG_CONFLICT", "Resolution does not name the current turn")
	}
	if m.String("status") == "stopped" {
		return m, failure("CONFIG_CONFLICT", "Stopped run requires a fresh workspace")
	}
	e, source, err := CheckResolution(ctx, s, m, x, o)
	if err != nil {
		return m, err
	}
	if x.Resolution != "abandon" && o.Apply == nil {
		return m, failure("TURN_UNCERTAIN", "Checked resolution cannot be applied by this backend")
	}
	if e.Artifacts == nil {
		e.Artifacts = []store.FileObservation{}
	}
	if e.Evidence == nil {
		e.Evidence = []store.FileObservation{}
	}
	record := map[string]any{"record_version": 1, "run_id": m["run_id"], "turn_id": turn, "resolution": x.Resolution, "artifact_paths": x.ArtifactPaths, "evidence_paths": x.EvidencePaths, "note": x.Note, "completion_source": source, "artifacts": e.Artifacts, "evidence": e.Evidence}
	if err := contract.Validate("resolution-record", record); err != nil {
		return m, failure("STATE_INVALID", err.Error())
	}
	id, err := store.NewID()
	if err != nil {
		return m, err
	}
	b, err := contract.Canonical(record)
	if err != nil {
		return m, err
	}
	path := fmt.Sprintf("state/control/resolution-%s.json", id)
	hash, err := s.StagePrivateText(path, b)
	if err != nil {
		return m, err
	}
	ref := store.ReceiptRef{Path: path, Hash: hash}
	m["resolution"] = map[string]any{"path": path, "sha256": hash, "completion_source": source, "kind": x.Resolution, "turn_id": turn}
	if x.Resolution == "abandon" {
		m["status"] = "stopped"
		tx, err := s.NewTransaction("control", m, nil, nil)
		if err != nil {
			return m, err
		}
		err = s.CommitTransaction(tx)
		return m, err
	}
	if err = o.Apply(ctx, s, m, x, e, ref); err != nil {
		return m, err
	}
	m, _, err = s.LoadSnapshot()
	return m, err
}
