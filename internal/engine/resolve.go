//go:build darwin || linux

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/TheEditor/volley/internal/agent"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/migration"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

type NoStartProof struct {
	RecordVersion int        `json:"record_version"`
	TurnID        string     `json:"turn_id"`
	RequestHash   string     `json:"request_hash"`
	ResultHash    string     `json:"result_hash"`
	Guard         GuardProof `json:"guard"`
}

type directResolution struct {
	owner *Owner
	proof checkedTurn
}

func (d *directResolution) Check(ctx context.Context, s *store.Store, m store.Snapshot, x migration.Resolution) (migration.CheckedEvidence, error) {
	e := migration.CheckedEvidence{}
	state, err := LoadState(s, m)
	if err != nil {
		return e, err
	}
	d.owner.State = state
	p, err := agent.DecodePreparation(s, m)
	if err != nil {
		return e, err
	}
	d.owner.Prepared = p
	if err = p.CheckResume(agent.CaptureIdentity(d.owner.Options.Env), nil, p.Record.Executables); err != nil {
		return e, err
	}
	// A completed planner may change SPEC. Every other frozen input still binds.
	bound := state
	bound.Bindings = map[string]store.FileObservation{}
	for path, obs := range state.Bindings {
		if x.Resolution == "accept_completed" && path == "SPEC.md" && m.Object("current_turn")["role"] == "planner" {
			continue
		}
		bound.Bindings[path] = obs
	}
	if err = checkBindings(s, bound); err != nil {
		return e, err
	}
	e.OwnershipChecked = true
	id := fmt.Sprint(m.Object("current_turn")["id"])
	if x.Resolution == "accept_completed" {
		d.proof, err = d.owner.checkTurn(ctx, m)
		if err != nil {
			return e, err
		}
		e.ArtifactsChecked = true
		e.IndependentCompletion = true
	} else {
		path := "state/turns/" + id + "/direct-result.json"
		b, err := s.ReadText(path)
		if err != nil {
			return e, failure("TURN_UNCERTAIN", "No independent direct result is available", nil)
		}
		var result struct {
			RecordVersion  int                `json:"record_version"`
			RequestHash    string             `json:"request_hash"`
			Outcome        review.TurnOutcome `json:"outcome"`
			Process        process.Result     `json:"process"`
			EvidenceHashes map[string]string  `json:"evidence_hashes"`
			ErrorCode      string             `json:"error_code"`
		}
		if err = decode(b, &result); err != nil {
			return e, err
		}
		prepared, err := s.ReadText("state/turns/" + id + "/direct-prepared.json")
		if err != nil {
			return e, err
		}
		var binding struct {
			RequestHash string              `json:"request_hash"`
			Prepared    review.PreparedTurn `json:"prepared"`
		}
		if err = json.Unmarshal(prepared, &binding); err != nil {
			return e, err
		}
		q := binding.Prepared.Request
		qb, _ := contract.Canonical(q)
		if result.RecordVersion != 1 || result.RequestHash != binding.RequestHash || result.RequestHash != contract.HashBytes(qb) || q.RunID != m.String("run_id") || q.TurnID != id || q.PromptHash != m.Object("current_turn")["prompt_hash"] {
			return e, failure("STATE_INVALID", "Direct result binding differs", nil)
		}
		if len(result.EvidenceHashes) == 0 {
			return e, failure("TURN_UNCERTAIN", "Direct result has no evidence hashes", nil)
		}
		for path, hash := range result.EvidenceHashes {
			_, obs, err := s.ReadRaw(path)
			if err != nil || obs.Hash != hash {
				return e, failure("ARTIFACT_CHANGED", "Direct evidence differs", nil)
			}
			e.Evidence = append(e.Evidence, obs)
		}
		// An absent start record alone is insufficient. The saved runner result must prove no start.
		ref := m.Object("no_start")
		proofPath := "state/turns/" + id + "/no-start-proof.json"
		if ref["path"] != proofPath {
			return e, failure("TURN_UNCERTAIN", "No checkpoint-bound no-start proof is available", nil)
		}
		proofBytes, err := s.ReadText(proofPath)
		if err != nil {
			return e, err
		}
		if contract.HashBytes(proofBytes) != ref["sha256"] {
			return e, failure("STATE_INVALID", "No-start proof changed", nil)
		}
		var proof NoStartProof
		if err = decode(proofBytes, &proof); err != nil {
			return e, err
		}
		requestBytes, err := s.ReadText("state/turns/" + id + "/engine-request.json")
		if err != nil {
			return e, err
		}
		if proof.RecordVersion != 1 || proof.TurnID != id || proof.RequestHash != m.Object("current_turn")["intent_hash"] || contract.HashBytes(requestBytes) != proof.RequestHash || proof.ResultHash != contract.HashBytes(b) || proof.RequestHash != result.RequestHash {
			return e, failure("STATE_INVALID", "No-start proof binding differs", nil)
		}
		changes := append([]store.AuthorizedChange{}, proof.Guard.Changes...)
		obs, err := s.Observe(proofPath)
		if err != nil {
			return e, err
		}
		obs.Path = filepath.Join(s.Path, proofPath)
		changes = append(changes, store.AuthorizedChange{Path: proofPath, Before: proof.Guard.Before.Files[proofPath], After: obs, Kind: "checked no-start proof"})
		checkpoint, err := checkpointChanges(s, proof.Guard.Before)
		if err != nil {
			return e, err
		}
		changes = append(changes, checkpoint...)
		mutation, err := s.Compare(ctx, proof.Guard.Before, changes)
		if err != nil {
			return e, err
		}
		if mutation != nil {
			return e, failure("ARTIFACT_CHANGED", "No-start evidence changed", map[string]any{"paths": mutation.Paths})
		}
		if expected, ok := result.EvidenceHashes["state/turns/"+id+"/process-exit.json"]; !ok || expected == "" {
			return e, failure("TURN_UNCERTAIN", "No process exit evidence is bound", nil)
		}
		if _, err := s.Metadata("state/turns/" + id + "/process-start.json"); !os.IsNotExist(err) {
			return e, failure("TURN_UNCERTAIN", "A process start record contradicts no-start proof", nil)
		}
		e.NoProcessStart = !result.Process.Started && result.Process.Outcome == process.NotStarted
		e.NoDelivery = e.NoProcessStart && result.Outcome.Delivery.Kind == review.NotStarted
	}
	for group, paths := range [][]string{x.ArtifactPaths, x.EvidencePaths} {
		for _, path := range paths {
			_, obs, err := s.ReadRaw(path)
			if err != nil {
				return e, err
			}
			if group == 0 {
				e.Artifacts = append(e.Artifacts, obs)
			} else {
				e.Evidence = append(e.Evidence, obs)
			}
		}
	}
	return e, nil
}
func (d *directResolution) CheckSettlement(ctx context.Context, s *store.Store, m store.Snapshot, e migration.CheckedEvidence) error {
	if e.IndependentCompletion && d.proof.Outcome.Completion.Settled {
		return nil
	}
	if e.NoProcessStart && e.NoDelivery {
		return nil
	}
	return failure("TURN_UNCERTAIN", "Saved execution is not settled", nil)
}

type directSettlement struct{ d *directResolution }

func (d directSettlement) Check(ctx context.Context, s *store.Store, m store.Snapshot, e migration.CheckedEvidence) error {
	return d.d.CheckSettlement(ctx, s, m, e)
}

func Resolve(ctx context.Context, workspace, turn string, x migration.Resolution, options Options) (map[string]any, error) {
	s, err := store.Open(workspace)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	o := &Owner{Store: s, Options: options}
	d := &directResolution{owner: o}
	settings := migration.ResolutionOptions{Evidence: d, Sessions: directSettlement{d}, Apply: func(ctx context.Context, s *store.Store, m store.Snapshot, x migration.Resolution, e migration.CheckedEvidence, ref store.ReceiptRef) error {
		if x.Resolution == "accept_completed" {
			proof, err := o.checkTurn(ctx, m, ref.Path)
			if err != nil {
				return err
			}
			return o.finishTurn(ctx, m, proof.Request, proof.Outcome, proof.Receipt)
		}
		// The prior intent and all partial evidence remain. A later resume can create
		// a new direct process only after the checked no-start resolution is saved.
		m["current_turn"] = nil
		m["status"] = "ready"
		return saveState(s, m, o.State, "control", nil, nil)
	}}
	m, err := migration.Resolve(ctx, s, turn, x, settings)
	if m == nil {
		return nil, err
	}
	data := Data(m)
	data["resolution"] = m["resolution"]
	return data, err
}
