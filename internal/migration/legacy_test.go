//go:build darwin || linux

package migration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
)

func put(t *testing.T, root, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func code(err error) string {
	var e *contract.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		v := info.Mode().String()
		if d.Type()&os.ModeSymlink != 0 {
			target, e := os.Readlink(path)
			if e != nil {
				return e
			}
			v += target
		} else if !d.IsDir() {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			v += contract.HashBytes(b)
		}
		m[rel] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestAMIG04LegacyMarkers(t *testing.T) {
	for _, name := range []string{"roles", "persistent", "backend", "run", "provenance.md", "session.planner"} {
		for _, value := range []string{"", "partial"} {
			t.Run(name+value, func(t *testing.T) {
				root := t.TempDir()
				put(t, root, "state/"+name, value)
				before := tree(t, root)
				if err := CheckStart(root); code(err) != "LEGACY_BOUNDARY_UNVERIFIED" {
					t.Fatal(err)
				}
				report, err := Report(root)
				if err != nil || report["classification"] != "legacy" {
					t.Fatal(report, err)
				}
				emitProof(t, root, nil)
				if !reflect.DeepEqual(before, tree(t, root)) {
					t.Fatal("Legacy files changed")
				}
			})
		}
	}
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			target := t.TempDir()
			put(t, target, "private", "private data")
			os.Mkdir(filepath.Join(root, "state"), 0700)
			path := filepath.Join(root, "state/run")
			if kind == "symlink" {
				os.Symlink(filepath.Join(target, "private"), path)
			} else {
				os.Mkdir(path, 0700)
			}
			before := tree(t, root)
			if code(CheckStart(root)) != "LEGACY_BOUNDARY_UNVERIFIED" {
				t.Fatal("Unsafe marker not blocked")
			}
			r, err := Report(root)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "private data") {
				t.Fatal("Symlink target read")
			}
			emitProof(t, root, nil)
			if !reflect.DeepEqual(before, tree(t, root)) {
				t.Fatal("Marker changed")
			}
		})
	}
	for _, manifest := range []string{"{", "{}", "{\"record_version\":2}"} {
		t.Run(manifest, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "state/run", "old")
			put(t, root, "state/manifest.json", manifest)
			before := tree(t, root)
			expected := "STATE_INVALID"
			if strings.Contains(manifest, ":2") {
				expected = "STATE_VERSION_UNSUPPORTED"
			}
			if code(CheckStart(root)) != expected {
				t.Fatal(CheckStart(root))
			}
			emitProof(t, root, nil)
			if !reflect.DeepEqual(before, tree(t, root)) {
				t.Fatal("Broken native state changed")
			}
		})
	}
}
func TestAMIG02LegacyReport(t *testing.T) {
	root := t.TempDir()
	put(t, root, "state/run", "old-run")
	put(t, root, "state/session.planner", "old-session")
	put(t, root, "rounds/r01.critique.md", "APPROVE\n")
	put(t, root, "SPEC.md", "old spec")
	put(t, root, "QUESTIONS.md", "old question")
	before := tree(t, root)
	a, err := Report(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Report(root)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("Report changed", err)
	}
	if err = contract.Validate("data-workspace-legacy-report", a); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(a)
	if !strings.Contains(string(encoded), "Historical verdict: APPROVE; not a native approval") {
		t.Fatal(a)
	}
	emitProof(t, root, nil)
	if !reflect.DeepEqual(before, tree(t, root)) {
		t.Fatal("Report wrote files")
	}
}
func TestAMIG04RoundsOnly(t *testing.T) {
	for _, name := range []string{"r09.critique.md", "r01.spec.md", "r01.critique.md", "second-opinion.md", "session."} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "rounds/"+name, "APPROVE\n")
			if err := CheckStart(root); err != nil {
				t.Fatal(err)
			}
			err := CheckOutputs(root, true, 2, true, false)
			collision := name == "r01.critique.md" || name == "second-opinion.md"
			if collision != (code(err) == "OUTPUT_CONFLICT") {
				t.Fatal(name, err)
			}
			emitProof(t, root, map[string]any{"collision_error": code(err)})
			r, e := Report(root)
			if e != nil || r["classification"] != "fresh" {
				t.Fatal(r, e)
			}
		})
	}
}

type fileEvidence struct{ hash string }

func (f fileEvidence) Check(ctx context.Context, s *store.Store, m store.Snapshot, x Resolution) (CheckedEvidence, error) {
	e := CheckedEvidence{}
	if err := s.CheckIdentity(); err != nil {
		return e, err
	}
	e.OwnershipChecked = true
	b, o, err := s.ReadRaw("evidence.json")
	if err != nil {
		return e, err
	}
	var v struct{ Complete, NoDelivery, NoStart bool }
	if err = json.Unmarshal(b, &v); err != nil {
		return e, err
	}
	e.IndependentCompletion = v.Complete
	e.NoDelivery = v.NoDelivery
	e.NoProcessStart = v.NoStart
	e.Evidence = []store.FileObservation{o}
	artifact, err := s.Observe("SPEC.md")
	if err != nil {
		return e, err
	}
	e.ArtifactsChecked = artifact.Hash == f.hash
	e.Artifacts = []store.FileObservation{artifact}
	return e, nil
}

type fileSettlement struct{}

func (fileSettlement) Check(ctx context.Context, s *store.Store, m store.Snapshot, e CheckedEvidence) error {
	b, err := s.ReadText("settled.txt")
	if err != nil {
		return err
	}
	if string(b) != "settled" {
		return failure("TURN_UNCERTAIN", "Session is not settled")
	}
	return nil
}
func TestAMIG03ResolutionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, choice, evidence, settlement, source, want string
		changed                                          bool
	}{
		{name: "human completion", choice: "accept_completed", evidence: `{}`, settlement: "settled", source: "human_attested"},
		{name: "backend completion", choice: "accept_completed", evidence: `{"Complete":true}`, settlement: "settled", source: "backend_confirmed"},
		{name: "unsettled completion", choice: "accept_completed", evidence: `{}`, settlement: "active", want: "TURN_UNCERTAIN"},
		{name: "changed artifact", choice: "accept_completed", evidence: `{}`, settlement: "settled", want: "ARTIFACT_CHANGED", changed: true},
		{name: "proved no start", choice: "confirm_not_executed", evidence: `{"NoDelivery":true,"NoStart":true}`, settlement: "settled", source: "none"},
		{name: "assertion only", choice: "confirm_not_executed", evidence: `{}`, settlement: "settled", want: "TURN_UNCERTAIN"},
		{name: "delivery possible", choice: "confirm_not_executed", evidence: `{"NoStart":true}`, settlement: "settled", want: "TURN_UNCERTAIN"},
		{name: "process possible", choice: "confirm_not_executed", evidence: `{"NoDelivery":true}`, settlement: "settled", want: "TURN_UNCERTAIN"},
		{name: "abandon", choice: "abandon", source: "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "SPEC.md", "checked spec")
			hash := contract.HashBytes([]byte("checked spec"))
			put(t, root, "evidence.json", tc.evidence)
			put(t, root, "settled.txt", tc.settlement)
			if tc.changed {
				put(t, root, "SPEC.md", "changed")
			}
			s, err := store.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			before := tree(t, root)
			_, source, err := CheckResolution(context.Background(), s, nil, Resolution{Resolution: tc.choice, Note: "human assertion"}, ResolutionOptions{Evidence: fileEvidence{hash}, Sessions: fileSettlement{}})
			if code(err) != tc.want || source != tc.source {
				t.Fatal(source, err)
			}
			emitProof(t, root, nil)
			if !reflect.DeepEqual(before, tree(t, root)) {
				t.Fatal("Policy changed evidence")
			}
		})
	}
}

func emitProof(t *testing.T, root string, output any) {
	t.Helper()
	record := map[string]any{"case": t.Name(), "tier": "A", "fixture": "F-FILES/F-PURE", "artifact_hashes": tree(t, root), "observed_output": output}
	b, err := contract.Canonical(record)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("LEGACY_EVIDENCE", string(b))
}
