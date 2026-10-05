//go:build darwin || linux

package store

import (
	"fmt"
	"os"
	"strings"
)

// A mutable spec has a separate inode from its immutable transaction source.
// The transaction binds the copy inode before publication, including recovery.
func (s *Store) promoteSeedCopy(a Artifact, i int) error {
	c := *a.CopyCandidate
	if a.TargetPath != "SPEC.md" || a.Replace || c.Kind != "file" || c.Hash != a.Hash || !strings.HasPrefix(c.Path, "state/control/seed-copy-") || !strings.HasSuffix(c.Path, "-SPEC.md") {
		return fail("STATE_INVALID", a.TargetPath, "Invalid seed copy binding")
	}
	expected := c
	expected.Path = a.TargetPath
	target, err := s.Observe(a.TargetPath)
	if os.IsNotExist(err) {
		current, e := s.Observe(c.Path)
		if e != nil {
			return e
		}
		if current != c {
			return fail("OUTPUT_CONFLICT", c.Path, "Seed copy changed before publication")
		}
		if e = s.link(c.Path, a.TargetPath, fmt.Sprintf("artifact-%d.link", i)); e != nil {
			return e
		}
		target, err = s.Observe(a.TargetPath)
	}
	if err != nil {
		return err
	}
	if target != expected {
		return fail("OUTPUT_CONFLICT", a.TargetPath, "Seed target is not the bound copy")
	}
	if err = s.syncParent(a.TargetPath, fmt.Sprintf("artifact-%d.dir-fsync", i)); err != nil {
		return err
	}
	if current, e := s.Observe(c.Path); e == nil {
		if current != c {
			return fail("OUTPUT_CONFLICT", c.Path, "Seed copy identity changed")
		}
		if e = s.remove(c.Path); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	return s.syncParent(c.Path, fmt.Sprintf("artifact-%d.copy-dir-fsync", i))
}
