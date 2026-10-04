//go:build darwin || linux

package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

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

// ReadText uses the same descriptor-relative, bounded, regular UTF-8 reader
// as owned records. Authority and raw invalid output are never repaired here.
func (s *Store) ReadText(path string) ([]byte, error) {
	b, _, err := s.read(path, TextLimit)
	return b, err
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
