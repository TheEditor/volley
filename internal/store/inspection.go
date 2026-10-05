//go:build darwin || linux

package store

import (
	"golang.org/x/sys/unix"
	"os"
)

// ObserveOwner tests the existing lock without creating or changing a record.
// This probe grants no controller authority to the Store.
func (s *Store) ObserveOwner() string {
	f, err := s.open("state/owner.lock", unix.O_RDONLY, 0)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "unknown"
	}
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
		return "active"
	}
	if err != nil {
		return "unknown"
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	current, err := s.lstat("state/owner.lock")
	if err != nil || !os.SameFile(info, current) || s.CheckIdentity() != nil {
		return "unknown"
	}
	return "absent"
}

// SavedInbox reads only complete, checked journal entries. It does not repair
// partial writes and does not create or acquire a writer lock.
func (s *Store) SavedInbox(runID string) (Inbox, error) { return s.readInbox(runID) }

// Metadata reports an unsafe leaf without opening it or following its target.
func (s *Store) Metadata(path string) (FileObservation, error) {
	p, leaf, err := s.parent(path)
	if err != nil {
		return FileObservation{}, err
	}
	defer p.Close()
	var st unix.Stat_t
	if err = unix.Fstatat(int(p.Fd()), leaf, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return FileObservation{}, err
	}
	kind := "special"
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		kind = "file"
	case unix.S_IFDIR:
		kind = "directory"
	case unix.S_IFLNK:
		kind = "symlink"
	}
	return FileObservation{Path: path, Kind: kind, Device: uint64(st.Dev), Inode: uint64(st.Ino), Bytes: st.Size}, nil
}
func (s *Store) Names(path string) ([]string, error) { return s.names(path) }
