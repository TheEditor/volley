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
