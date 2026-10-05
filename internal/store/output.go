//go:build darwin || linux

package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ReplaceForRestoration moves one private prepared inode to SPEC.md. The
// caller's committed control intent binds both observations. Recovery accepts
// only the recorded candidate inode, not merely matching approved bytes.
func (s *Store) ReplaceForRestoration(candidate, before FileObservation) (FileObservation, error) {
	if err := s.checkOwner(); err != nil {
		return FileObservation{}, err
	}
	if before.Path != "SPEC.md" || !strings.HasPrefix(candidate.Path, "state/control/restoration-candidate-") || candidate.Kind != "file" || before.Kind != "file" {
		return FileObservation{}, fail("STATE_INVALID", "SPEC.md", "Invalid restoration observations")
	}
	_, current, err := s.ReadObserved("SPEC.md", TextLimit)
	if err != nil {
		return FileObservation{}, err
	}
	installed := candidate
	installed.Path = "SPEC.md"
	if current == installed {
		if err = s.syncParent(candidate.Path, "restoration.source-dir-fsync"); err != nil {
			return FileObservation{}, err
		}
		if err = s.syncParent("SPEC.md", "restoration.target-dir-fsync"); err != nil {
			return FileObservation{}, err
		}
		return current, nil
	}
	if current != before {
		return FileObservation{}, fail("ARTIFACT_CHANGED", "SPEC.md", "Restoration target changed")
	}
	_, staged, err := s.ReadObserved(candidate.Path, TextLimit)
	if err != nil || staged != candidate {
		return FileObservation{}, fail("ARTIFACT_CHANGED", candidate.Path, "Restoration candidate changed")
	}
	if err = s.site("restoration.replace.before"); err != nil {
		return FileObservation{}, err
	}
	sourceDir, sourceName, err := s.parent(candidate.Path)
	if err != nil {
		return FileObservation{}, err
	}
	defer sourceDir.Close()
	targetDir, targetName, err := s.parent("SPEC.md")
	if err != nil {
		return FileObservation{}, err
	}
	defer targetDir.Close()
	_, current, err = s.ReadObserved("SPEC.md", TextLimit)
	if err != nil || current != before {
		return FileObservation{}, fail("ARTIFACT_CHANGED", "SPEC.md", "Restoration target changed before replacement")
	}
	_, staged, err = s.ReadObserved(candidate.Path, TextLimit)
	if err != nil || staged != candidate {
		return FileObservation{}, fail("ARTIFACT_CHANGED", candidate.Path, "Restoration candidate changed before replacement")
	}
	if err = unix.Renameat(int(sourceDir.Fd()), sourceName, int(targetDir.Fd()), targetName); err != nil {
		return FileObservation{}, err
	}
	if err = s.site("restoration.replace.after"); err != nil {
		return FileObservation{}, err
	}
	if err = s.syncParent(candidate.Path, "restoration.source-dir-fsync"); err != nil {
		return FileObservation{}, err
	}
	if err = s.syncParent("SPEC.md", "restoration.target-dir-fsync"); err != nil {
		return FileObservation{}, err
	}
	_, current, err = s.ReadObserved("SPEC.md", TextLimit)
	if err != nil || current != installed {
		return FileObservation{}, fail("ARTIFACT_CHANGED", "SPEC.md", "Restored inode differs")
	}
	return current, nil
}

// RootObservation uses the retained workspace descriptor, not a path lookup.
func (s *Store) RootObservation() (FileObservation, error) {
	if err := s.CheckIdentity(); err != nil {
		return FileObservation{}, err
	}
	return observationOf(s.Path, nil, s.identity), nil
}

func (s *Store) PrepareTurn(id string) error {
	if !validID(id) {
		return fail("INVALID_INPUT", id, "Invalid turn identity")
	}
	if err := s.checkOwner(); err != nil {
		return err
	}
	return s.mkdir("state/turns/"+id, 0700)
}

// BeginOutput owns an exclusive raw evidence sink. Partial or invalid bytes
// remain available. A raw sink is not an authoritative completion receipt.
func (s *Store) BeginOutput(path string) (*os.File, error) {
	if err := s.checkOwner(); err != nil {
		return nil, err
	}
	p, err := clean(path)
	if err != nil {
		return nil, err
	}
	if protectedAuthority(p) {
		return nil, fail("STATE_INVALID", p, "Authority cannot be a raw output")
	}
	if err := s.mkdir(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	f, err := s.open(p, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if err := s.syncParent(p, "output.create-dir-fsync"); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s *Store) FinishOutput(path string, f *os.File) error {
	if err := s.syncFile(f, "output.file-fsync"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return s.syncParent(path, "output.dir-fsync")
}

// CheckOutput binds a controller stream to the opened sink and its incremental
// writer hash. A replaced path or an outside write cannot become that stream.
func (s *Store) CheckOutput(path string, f *os.File, expectedHash string) (FileObservation, error) {
	info, err := f.Stat()
	if err != nil {
		return FileObservation{}, err
	}
	current, err := s.lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return FileObservation{}, fail("ARTIFACT_CHANGED", path, "Owned output inode changed")
	}
	_, obs, err := s.ReadRaw(path)
	if err != nil {
		return obs, err
	}
	if obs.Hash != expectedHash || obs.Bytes != info.Size() {
		return obs, fail("ARTIFACT_CHANGED", path, "Owned output differs from the controller stream")
	}
	return obs, nil
}

// ReadText uses the same descriptor-relative, bounded, regular UTF-8 reader
// as owned records. Authority and raw invalid output are never repaired here.
func (s *Store) ReadText(path string) ([]byte, error) {
	b, _, err := s.read(path, TextLimit)
	return b, err
}

// ReadObserved returns one bounded descriptor read with its kernel identity.
// An absent file is an explicit observation. The caller supplies a second
// equal read after its declared poll interval before consuming external input.
func (s *Store) ReadObserved(path string, limit int) ([]byte, FileObservation, error) {
	if limit < 1 || limit > TextLimit {
		return nil, FileObservation{}, fail("INVALID_INPUT", path, "Invalid read limit")
	}
	b, info, err := s.read(path, limit)
	if os.IsNotExist(err) {
		return nil, FileObservation{Path: path, Kind: "absent"}, nil
	}
	if err != nil {
		return nil, FileObservation{}, err
	}
	current, err := s.lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return nil, FileObservation{}, fail("ARTIFACT_CHANGED", path, "File identity changed during read")
	}
	return b, observationOf(path, b, info), nil
}

// RemoveObserved runs only after the archive checkpoint commits. It preserves
// changed or replaced input and keeps removed inodes in private quarantine.
// Exact concurrent submissions use the inbox command path.
func (s *Store) RemoveObserved(expected FileObservation) (bool, error) {
	if expected.Path != "HUMAN.md" && expected.Path != "QUESTIONS.md" {
		return false, fail("STATE_INVALID", expected.Path, "Only legacy input sources can be removed")
	}
	if err := s.checkOwner(); err != nil {
		return false, err
	}
	if err := s.site("input-source.remove.before"); err != nil {
		return false, err
	}
	_, observed, err := s.ReadObserved(expected.Path, 1<<20)
	if err != nil {
		return false, err
	}
	if observed.Kind == "absent" {
		return false, nil
	}
	if observed != expected {
		return false, nil
	}
	// Keep the removed inode in an owned quarantine. A writer with an open
	// descriptor can still append; those bytes must not be lost by unlink.
	id, err := NewID()
	if err != nil {
		return false, err
	}
	retained := "state/human/removed-source-" + id + ".md"
	if err := s.mkdir("state/human", 0700); err != nil {
		return false, err
	}
	if err := s.rename(expected.Path, retained, "input-source.removal-rename"); err != nil {
		return false, err
	}
	_, moved, readErr := s.ReadObserved(retained, 1<<20)
	moved.Path = expected.Path
	if readErr != nil || moved != expected {
		// A path replacement between observation and rename is restored only
		// if no newer source exists. Both inodes remain when restoration loses.
		if err := s.link(retained, expected.Path, "input-source.restore"); err != nil && !os.IsExist(err) {
			return false, err
		}
		_ = s.syncParent(expected.Path, "input-source.restore-dir-fsync")
		_ = s.syncParent(retained, "input-source.retained-dir-fsync")
		return false, fail("ANSWER_CONFLICT", retained, "Changed source remains pending and retained")
	}
	if err := s.site("input-source.remove.after"); err != nil {
		return false, err
	}
	if err := s.syncParent(expected.Path, "input-source.remove-dir-fsync"); err != nil {
		return true, err
	}
	if err := s.syncParent(retained, "input-source.retained-dir-fsync"); err != nil {
		return true, err
	}
	return true, nil
}

// ReadRaw retains invalid protocol bytes as evidence, without interpreting
// them as a state record or an acceptable text artifact.
func (s *Store) ReadRaw(path string) ([]byte, FileObservation, error) {
	f, err := s.open(path, unix.O_RDONLY, 0)
	if err != nil {
		return nil, FileObservation{}, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, FileObservation{}, err
	}
	b, err := io.ReadAll(io.LimitReader(f, TextLimit+1))
	if err != nil {
		return nil, FileObservation{}, err
	}
	after, err := f.Stat()
	if err != nil || len(b) > TextLimit || before.Size() != after.Size() {
		return nil, FileObservation{}, fmt.Errorf("Raw evidence changed or exceeds its limit")
	}
	return b, observationOf(path, b, after), nil
}

func (s *Store) SyncRetained(path string) error {
	if err := s.checkOwner(); err != nil {
		return err
	}
	f, err := s.open(path, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := s.syncFile(f, "output.recovered-file-fsync"); err != nil {
		return err
	}
	return s.syncParent(path, "output.recovered-dir-fsync")
}
