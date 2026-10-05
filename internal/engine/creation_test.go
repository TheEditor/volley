//go:build darwin || linux

package engine

import (
	"errors"
	"github.com/TheEditor/volley/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func TestFreshSetupRecovery(t *testing.T) {
	for _, boundary := range []string{"engine.creation.committed", "engine.preparation.committed", "artifact-0.link.after"} {
		t.Run(boundary, func(t *testing.T) {
			f := newFixture(t, "claude", false, true, AgentPlan{})
			migrationProof(t, f)
			seed := filepath.Join(f.Root, "seed.md")
			if err := os.Rename(filepath.Join(f.Workspace, "SPEC.md"), seed); err != nil {
				t.Fatal(err)
			}
			f.Request.Seed = seed
			fired := false
			f.Options.Hook = func(name string, s *store.Store) error {
				if name == boundary && !fired {
					fired = true
					return errors.New("owned setup interruption")
				}
				return nil
			}
			f.Options.Fault = func(name string) error {
				if name == boundary && !fired {
					fired = true
					return errors.New("owned seed interruption")
				}
				return nil
			}
			_, err := f.run(t)
			if err == nil || !fired || len(f.calls()) != 0 {
				t.Fatal("setup boundary", err, fired, f.calls())
			}
			id := f.manifest(t).String("run_id")
			f.Options.Hook = nil
			f.Options.Fault = nil
			f.Request.Resume = true
			f.Request.Resolved = nil
			f.Request.Seed = ""
			data, err := f.run(t)
			if err != nil || data["status"] != "approved" || len(f.calls()) != 1 || f.manifest(t).String("run_id") != id {
				t.Fatal(data, err, f.calls())
			}
			t.Log("setup recovery", boundary, "run preserved", id, "owned turns", f.calls())
		})
	}
}
func TestFreshSetupChangedInputRefuses(t *testing.T) {
	for _, source := range []bool{false, true} {
		t.Run(map[bool]string{false: "workspace", true: "seed"}[source], func(t *testing.T) {
			f := newFixture(t, "claude", false, true, AgentPlan{})
			migrationProof(t, f)
			target := filepath.Join(f.Workspace, "SPEC.md")
			if source {
				seed := filepath.Join(f.Root, "seed.md")
				os.Rename(target, seed)
				f.Request.Seed = seed
				target = seed
			}
			f.Options.Hook = func(name string, s *store.Store) error {
				if name == "engine.creation.committed" {
					return errors.New("owned interruption")
				}
				return nil
			}
			if _, err := f.run(t); err == nil {
				t.Fatal("missing interruption")
			}
			os.WriteFile(target, []byte("changed setup input\n"), 0600)
			f.Options.Hook = nil
			f.Request.Resume = true
			f.Request.Resolved = nil
			_, err := f.run(t)
			requireCode(t, err, "ARTIFACT_CHANGED")
			if len(f.calls()) != 0 {
				t.Fatal("changed input launched", f.calls())
			}
		})
	}
}
