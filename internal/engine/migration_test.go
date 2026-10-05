//go:build darwin || linux

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/migration"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/store"
)

func TestAMIG02FreshLegacySeed(t *testing.T) {
	old := t.TempDir()
	os.Mkdir(filepath.Join(old, "state"), 0700)
	os.WriteFile(filepath.Join(old, "state/session.planner"), []byte("old-session"), 0600)
	source := filepath.Join(old, "SPEC.md")
	original := []byte("# Old preserved spec\nRequirements\n")
	os.WriteFile(source, original, 0600)
	f := newFixture(t, "claude", false, true, AgentPlan{Critiques: []string{"VERDICT: REVISE\n", "VERDICT: APPROVE\n"}})
	migrationProof(t, f)
	os.Remove(filepath.Join(f.Workspace, "SPEC.md"))
	f.Request.Seed = source
	os.WriteFile(filepath.Join(f.Workspace, "CONSTRAINTS.md"), []byte("Keep explicit requirements\n"), 0600)
	data, err := f.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if data["status"] != "approved" || len(f.calls()) != 3 {
		t.Fatal(data, f.calls())
	}
	b, _ := os.ReadFile(source)
	if contract.HashBytes(b) != contract.HashBytes(original) {
		t.Fatal("Legacy seed source changed")
	}
	s, err := store.Open(f.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.History(); err != nil {
		t.Fatal("Mutable seed corrupted history", err)
	}
	state, err := LoadState(s, f.manifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.Persistent {
		t.Fatal("Old session adopted")
	}
	if !strings.Contains(f.calls()[0], "critic critique") || number(data["round"]) != 2 {
		t.Fatal(state.LastTurn)
	}
	t.Log("A-MIG-02 source hash", contract.HashBytes(original), "new spec hash", data["spec_hash"], "calls", f.calls())
}
func TestAMIG03NativeAcceptAndAbandon(t *testing.T) {
	for _, choice := range []string{"accept_completed", "abandon"} {
		t.Run(choice, func(t *testing.T) {
			f := newFixture(t, "claude", false, true, AgentPlan{Critiques: []string{"VERDICT: APPROVE\n"}})
			migrationProof(t, f)
			stop := "engine.receipt.saved"
			if choice == "abandon" {
				stop = "engine.intent.committed"
			}
			f.Options.Hook = func(name string, s *store.Store) error {
				if name == stop {
					return errors.New("owned interruption")
				}
				return nil
			}
			_, err := f.run(t)
			if err == nil {
				t.Fatal("Interruption did not occur")
			}
			m := f.manifest(t)
			id := m.Object("current_turn")["id"].(string)
			before, _ := os.ReadFile(filepath.Join(f.Workspace, "SPEC.md"))
			calls := len(f.calls())
			data, err := Resolve(context.Background(), f.Workspace, id, migration.Resolution{Resolution: choice, ArtifactPaths: []string{"SPEC.md"}, EvidencePaths: []string{}, Note: "checked resolution"}, f.Options)
			if err != nil {
				t.Fatal(err)
			}
			status := "ready"
			if choice == "abandon" {
				status = "stopped"
			}
			if data["status"] != status || len(f.calls()) != calls {
				t.Fatal(data, f.calls())
			}
			after, _ := os.ReadFile(filepath.Join(f.Workspace, "SPEC.md"))
			if contract.HashBytes(before) != contract.HashBytes(after) {
				t.Fatal("Resolution changed spec")
			}
			resolved := f.manifest(t).Object("resolution")
			source := "backend_confirmed"
			if choice == "abandon" {
				source = "none"
			}
			if resolved["completion_source"] != source {
				t.Fatal(resolved)
			}
			if choice == "accept_completed" {
				f.Options.Hook = nil
				f.Request.Resume = true
				if _, err = f.run(t); err != nil {
					t.Fatal(err)
				}
				if len(f.calls()) != calls {
					t.Fatal("Accepted turn repeated")
				}
			}
			t.Log("A-MIG-03", choice, "resolution", resolved, "calls", f.calls(), "spec hash", contract.HashBytes(after))
		})
	}
}

type failStartRunner struct{}

func (failStartRunner) Run(ctx context.Context, q process.Request) (process.Result, error) {
	agentCall := len(q.Args) > 1
	for _, arg := range q.Args {
		if arg == "--version" {
			agentCall = false
		}
	}
	if agentCall {
		if err := os.Chmod(q.Path, 0600); err != nil {
			return process.Result{}, err
		}
		defer os.Chmod(q.Path, 0700)
	}
	return (process.UnixRunner{}).Run(ctx, q)
}
func TestAMIG03NativeNoStart(t *testing.T) {
	f := newFixture(t, "claude", false, true, AgentPlan{})
	migrationProof(t, f)
	f.Options.Runner = failStartRunner{}
	_, err := f.run(t)
	if err == nil {
		t.Fatal("Failed start was accepted")
	}
	m := f.manifest(t)
	id := m.Object("current_turn")["id"].(string)
	if len(f.calls()) != 0 {
		t.Fatal("Process started")
	}
	data, err := Resolve(context.Background(), f.Workspace, id, migration.Resolution{Resolution: "confirm_not_executed", ArtifactPaths: []string{}, EvidencePaths: []string{}, Note: "proved no process start"}, f.Options)
	if err != nil {
		t.Fatal(err)
	}
	if data["status"] != "ready" || data["turn"] != nil {
		t.Fatal(data)
	}
	f.Options.Runner = process.UnixRunner{}
	f.Request.Resume = true
	data, err = f.run(t)
	if err != nil || data["status"] != "approved" || len(f.calls()) != 1 {
		t.Fatal(data, err, f.calls())
	}
	t.Log("A-MIG-03 no-start record", m.Object("no_start"), "calls after checked resume", f.calls())
}

type fakeTurnEvidence struct {
	hash  string
	steps *[]string
}

func (f fakeTurnEvidence) Check(ctx context.Context, s *store.Store, m store.Snapshot, x migration.Resolution) (migration.CheckedEvidence, error) {
	*f.steps = append(*f.steps, "artifacts and ownership")
	if err := s.CheckIdentity(); err != nil {
		return migration.CheckedEvidence{}, err
	}
	obs, err := s.Observe("SPEC.md")
	if err != nil {
		return migration.CheckedEvidence{}, err
	}
	return migration.CheckedEvidence{OwnershipChecked: true, ArtifactsChecked: obs.Hash == f.hash, Artifacts: []store.FileObservation{obs}}, nil
}

type fakeSessionSettlement struct{ steps *[]string }

func (f fakeSessionSettlement) Check(ctx context.Context, s *store.Store, m store.Snapshot, e migration.CheckedEvidence) error {
	b, err := s.ReadText("settlement.txt")
	if err != nil {
		return err
	}
	if string(b) != "settled" {
		return errors.New("fake session is active")
	}
	*f.steps = append(*f.steps, "session settlement")
	return nil
}
func TestAMIG03HumanAttestedRecord(t *testing.T) {
	f := newFixture(t, "claude", false, true, AgentPlan{})
	migrationProof(t, f)
	os.WriteFile(filepath.Join(f.Workspace, "settlement.txt"), []byte("settled"), 0600)
	f.Options.Hook = func(name string, s *store.Store) error {
		if name == "engine.intent.committed" {
			return errors.New("owned interruption")
		}
		return nil
	}
	if _, err := f.run(t); err == nil {
		t.Fatal("No interruption")
	}
	m := f.manifest(t)
	id := m.Object("current_turn")["id"].(string)
	hash := m.String("spec_hash")
	s, err := store.Open(f.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	steps := []string{}
	applied := false
	options := migration.ResolutionOptions{Evidence: fakeTurnEvidence{hash, &steps}, Sessions: fakeSessionSettlement{&steps}, Apply: func(ctx context.Context, s *store.Store, m store.Snapshot, x migration.Resolution, e migration.CheckedEvidence, ref store.ReceiptRef) error {
		if len(steps) != 2 || !e.ArtifactsChecked || ref.Hash == "" {
			t.Fatal("Advance preceded checks", steps, e)
		}
		applied = true
		m["current_turn"] = nil
		m["status"] = "ready"
		tx, err := s.NewTransaction("control", m, nil, nil)
		if err != nil {
			return err
		}
		return s.CommitTransaction(tx)
	}}
	m, err = migration.Resolve(context.Background(), s, id, migration.Resolution{Resolution: "accept_completed", ArtifactPaths: []string{"SPEC.md"}, EvidencePaths: []string{"settlement.txt"}, Note: "Human completion claim with checked artifacts and settled fake session"}, options)
	if err != nil || !applied || len(f.calls()) != 0 {
		t.Fatal(err, applied, f.calls())
	}
	ref := m.Object("resolution")
	b, err := s.ReadText(ref["path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if ref["completion_source"] != "human_attested" || !strings.Contains(string(b), `"completion_source":"human_attested"`) {
		t.Fatal(ref, string(b))
	}
	t.Log("A-MIG-03 human-attested record hash", contract.HashBytes(b), "check order", steps, "calls", f.calls())
}

func migrationProof(t *testing.T, f *fixture) {
	t.Helper()
	t.Cleanup(func() {
		f.evidence(t)
		b, err := os.ReadFile(filepath.Join(f.Root, "evidence.json"))
		if err != nil {
			t.Error(err)
			return
		}
		t.Log("MIGRATION_EVIDENCE", string(b))
	})
}
