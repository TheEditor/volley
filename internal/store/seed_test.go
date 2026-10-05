//go:build darwin || linux

package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMutableSeedCopyRecovery(t *testing.T) {
	for _, site := range []string{"", "artifact-0.link.after"} {
		t.Run(site, func(t *testing.T) {
			s := fixture(t)
			m, _, _ := s.LoadSnapshot()
			stage := "state/control/seed-proof-SPEC.md"
			candidate := "state/control/seed-copy-proof-SPEC.md"
			b := []byte("original seed")
			hash, err := s.StageText(stage, b)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.StageText(candidate, b); err != nil {
				t.Fatal(err)
			}
			obs, err := s.Observe(candidate)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.NewTransaction("control", m, []Artifact{{StagedPath: stage, TargetPath: "SPEC.md", Hash: hash, CopyCandidate: &obs}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			s.Fault = func(name string) error {
				if name == site {
					return errors.New("owned interruption")
				}
				return nil
			}
			err = s.CommitTransaction(tx)
			if site != "" && err == nil {
				t.Fatal("Fault was not reached")
			}
			if site == "" && err != nil {
				t.Fatal(err)
			}
			s.Fault = nil
			if _, err = s.Recover(); err != nil {
				t.Fatal(err)
			}
			a, _ := os.Stat(filepath.Join(s.Path, stage))
			target, _ := os.Stat(filepath.Join(s.Path, "SPEC.md"))
			if os.SameFile(a, target) {
				t.Fatal("Mutable target shares source inode")
			}
			if err = os.WriteFile(filepath.Join(s.Path, "SPEC.md"), []byte("revised"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = s.History(); err != nil {
				t.Fatal("Revision damaged history", err)
			}
		})
	}
}
