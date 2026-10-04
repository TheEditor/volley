//go:build darwin || linux

package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestASTATE05Forbidden(t *testing.T) {
	paths := []string{"state/manifest.json", "state/owner.lock", "state/transactions/undeclared.json", "state/unknown", "volley.config.toml", "gashki.config.toml", "rounds/r00.critique.md", "BRIEF.md", "CONSTRAINTS.md", "HUMAN.md"}
	reserved := []string{"r0.critique.md", "r01.critique-retry.md", "r9999.response.md", "r01.answer-response.md", "r01.spec.md", "r01.human.md", "r01.questions.md", "second-opinion.md", "closing-any.ext"}
	for _, name := range reserved {
		paths = append(paths, "rounds/"+name)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			s := fixture(t)
			for _, file := range []string{"volley.config.toml", "gashki.config.toml", "rounds/r00.critique.md", "BRIEF.md", "CONSTRAINTS.md", "HUMAN.md"} {
				put(t, s, file, "trusted")
			}
			inventory, err := s.Inspect(context.Background(), Protection{Actor: "planner", AllowedHistory: []string{"*"}})
			if err != nil {
				t.Fatal(err)
			}
			put(t, s, path, "tampered")
			observation, err := s.Compare(context.Background(), inventory, nil)
			if err != nil {
				t.Fatal(err)
			}
			if observation == nil || observation.Actor != "planner" {
				t.Fatalf("missing mutation for %s", path)
			}
			found := false
			for _, p := range observation.Paths {
				if p == path {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing path: %+v", observation)
			}
		})
	}
	t.Run("directory-symlink", func(t *testing.T) {
		s := fixture(t)
		inventory, err := s.Snapshot(context.Background(), "planner")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(s.Path, "state/logs"), filepath.Join(s.Path, "old-logs")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(s.Path, "old-logs"), filepath.Join(s.Path, "state/logs")); err != nil {
			t.Fatal(err)
		}
		m, err := s.Compare(context.Background(), inventory, nil)
		if err != nil || m == nil {
			t.Fatalf("%+v %v", m, err)
		}
	})
	t.Run("reserved-basename-only", func(t *testing.T) {
		for _, name := range []string{"r01.revision.md", "x-r01.spec.md", "r01Xspec.md", "r.spec.md", "closing", "second-opinion.md.extra"} {
			if ReservedRoundName(name) {
				t.Fatal(name)
			}
		}
	})
}
func TestASTATE05Allowed(t *testing.T) {
	t.Run("exact-controller-deltas", func(t *testing.T) {
		s := fixture(t)
		put(t, s, "state/logs/active.log", "before")
		inventory, err := s.Snapshot(context.Background(), "planner")
		if err != nil {
			t.Fatal(err)
		}
		paths := []string{"state/logs/active.log", "state/control/receipt.json", "rounds/r01.critique.md"}
		var changes []AuthorizedChange
		for _, path := range paths {
			put(t, s, path, "controller-owned")
			after, err := s.Observe(path)
			if err != nil {
				t.Fatal(err)
			}
			after.Path = filepath.Join(s.Path, path)
			changes = append(changes, AuthorizedChange{path, inventory.Files[path], after, "registered-controller-output"})
		}
		m, err := s.Compare(context.Background(), inventory, changes)
		if err != nil || m != nil {
			t.Fatalf("%+v %v", m, err)
		}
		put(t, s, "state/other", "forbidden")
		m, err = s.Compare(context.Background(), inventory, changes)
		if err != nil || m == nil {
			t.Fatalf("whole state exempt: %+v %v", m, err)
		}
	})
	t.Run("committed-inbox-extension", func(t *testing.T) {
		s := fixture(t)
		inventory, err := s.Snapshot(context.Background(), "planner")
		if err != nil {
			t.Fatal(err)
		}
		r := inputFixture(t, s, "during-turn")
		if _, err := s.SubmitInput(context.Background(), r, time.Second); err != nil {
			t.Fatal(err)
		}
		m, err := s.Compare(context.Background(), inventory, nil)
		if err != nil || m != nil {
			t.Fatalf("%+v %v", m, err)
		}
		put(t, s, "state/inputs/unreferenced.json", "{}")
		m, err = s.Compare(context.Background(), inventory, nil)
		if err != nil || m == nil {
			t.Fatalf("unreferenced receipt accepted: %+v %v", m, err)
		}
	})
	t.Run("changed-inbox-prefix", func(t *testing.T) {
		s := fixture(t)
		r := inputFixture(t, s, "before-turn")
		entry, err := s.SubmitInput(context.Background(), r, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		inventory, err := s.Snapshot(context.Background(), "planner")
		if err != nil {
			t.Fatal(err)
		}
		put(t, s, entry.ReceiptPath, "changed")
		m, err := s.Compare(context.Background(), inventory, nil)
		if err == nil || m == nil {
			t.Fatalf("changed receipt accepted: %+v %v", m, err)
		}
	})
	t.Run("unreserved-history", func(t *testing.T) {
		s := fixture(t)
		inventory, err := s.Inspect(context.Background(), Protection{Actor: "planner", AllowedHistory: []string{"rounds/r01.revision.md"}})
		if err != nil {
			t.Fatal(err)
		}
		put(t, s, "rounds/r01.revision.md", "new history")
		m, err := s.Compare(context.Background(), inventory, nil)
		if err != nil || m != nil {
			t.Fatalf("%+v %v", m, err)
		}
		next, err := s.Snapshot(context.Background(), "planner")
		if err != nil {
			t.Fatal(err)
		}
		put(t, s, "rounds/r01.revision.md", "changed old history")
		m, err = s.Compare(context.Background(), next, nil)
		if err != nil || m == nil {
			t.Fatalf("%+v %v", m, err)
		}
	})
	t.Run("critic-spec-and-answer-append", func(t *testing.T) {
		s := fixture(t)
		for _, path := range []string{"SPEC.md", "QUESTIONS.md", "HUMAN.md"} {
			put(t, s, path, "before")
		}
		critic, err := s.Snapshot(context.Background(), "critic")
		if err != nil {
			t.Fatal(err)
		}
		put(t, s, "SPEC.md", "critic change")
		m, err := s.Compare(context.Background(), critic, nil)
		if err != nil || m == nil {
			t.Fatalf("%+v %v", m, err)
		}
		answer, err := s.Inspect(context.Background(), Protection{Actor: "planner", AnswerTurn: true})
		if err != nil {
			t.Fatal(err)
		}
		put(t, s, "HUMAN.md", "before\nanswer")
		m, err = s.Compare(context.Background(), answer, nil)
		if err != nil || m != nil {
			t.Fatalf("%+v %v", m, err)
		}
		put(t, s, "HUMAN.md", "overwritten")
		m, err = s.Compare(context.Background(), answer, nil)
		if err != nil || m == nil || !strings.Contains(strings.Join(m.Paths, " "), "HUMAN.md") {
			t.Fatalf("%+v %v", m, err)
		}
	})
}

func TestExactInboxPrefixCannotBeRewritten(t *testing.T) {
	s := fixture(t)
	r := inputFixture(t, s, "first")
	if _, err := s.SubmitInput(context.Background(), r, time.Second); err != nil {
		t.Fatal(err)
	}
	inventory, err := s.Snapshot(context.Background(), "planner")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Path, "state/inputs/inbox.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte(" "), b...), 0600); err != nil {
		t.Fatal(err)
	}
	r.Key = "second"
	if _, err := s.SubmitInput(context.Background(), r, time.Second); err != nil {
		t.Fatal(err)
	}
	m, err := s.Compare(context.Background(), inventory, nil)
	if m == nil || err == nil {
		t.Fatalf("rewritten prefix accepted: %+v %v", m, err)
	}
}

func TestInvalidProtectedBytesRetainActorEvidence(t *testing.T) {
	s := fixture(t)
	inventory, err := s.Snapshot(context.Background(), "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Path, "state/manifest.json"), []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := s.Compare(context.Background(), inventory, nil)
	if err != nil || m == nil || m.Actor != "planner" {
		t.Fatalf("%+v %v", m, err)
	}
}
