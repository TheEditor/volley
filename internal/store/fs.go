//go:build darwin || linux

package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/contract"
	"golang.org/x/sys/unix"
)

const TextLimit = 32 << 20
const RecordLimit = 32 << 20

type Error struct{ Code, Path, Message string }

func (e *Error) Error() string              { return fmt.Sprintf("%s: %s: %s", e.Code, e.Path, e.Message) }
func fail(code, path, message string) error { return &Error{code, path, message} }
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])), nil
}
func validID(id string) bool {
	if len(id) != 26 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}

// Fault is a local harness seam. Production has no environment fault trigger.
type Fault func(string) error
type Store struct {
	Path          string
	root          *os.File
	identity      os.FileInfo
	owner         *os.File
	ownerIdentity os.FileInfo
	Fault         Fault
}

func Open(path string) (*Store, error) {
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	physical, err = filepath.Abs(physical)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(physical, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), physical)
	st, err := root.Stat()
	if err != nil {
		root.Close()
		return nil, err
	}
	return &Store{Path: physical, root: root, identity: st}, nil
}
func (s *Store) Close() error {
	if s.owner != nil {
		_ = s.owner.Close()
		s.owner = nil
	}
	return s.root.Close()
}
func (s *Store) CheckIdentity() error {
	st, err := os.Lstat(s.Path)
	if err != nil || !st.IsDir() || !os.SameFile(s.identity, st) {
		return fail("STATE_INVALID", s.Path, "Workspace identity changed")
	}
	return nil
}
func (s *Store) checkOwner() error {
	if s.owner == nil {
		return fail("LOCKED", s.Path, "Controller ownership is required")
	}
	if err := s.CheckIdentity(); err != nil {
		return err
	}
	st, err := s.lstat("state/owner.lock")
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(s.ownerIdentity, st) {
		return fail("STATE_INVALID", "state/owner.lock", "Owner lock identity changed")
	}
	return nil
}
func clean(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", fail("STATE_INVALID", path, "Invalid owned path")
	}
	p := filepath.Clean(path)
	if p == "." || p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
		return "", fail("STATE_INVALID", path, "Owned path escapes workspace")
	}
	return p, nil
}

// parent walks descriptor-relative directories with O_NOFOLLOW at every step.
// A later path replacement cannot redirect an opened descriptor outside root.
func (s *Store) parent(path string) (*os.File, string, error) {
	if err := s.CheckIdentity(); err != nil {
		return nil, "", err
	}
	path, err := clean(path)
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(path, string(filepath.Separator))
	fd, err := unix.Dup(int(s.root.Fd()))
	if err != nil {
		return nil, "", err
	}
	dir := os.NewFile(uintptr(fd), s.Path)
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(int(dir.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		dir.Close()
		if err != nil {
			return nil, "", fail("STATE_INVALID", path, "Owned parent is absent or not a real directory")
		}
		dir = os.NewFile(uintptr(next), part)
	}
	return dir, parts[len(parts)-1], nil
}
func (s *Store) open(path string, flags int, mode uint32) (*os.File, error) {
	parent, name, err := s.parent(path)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(s.Path, path))
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, fail("STATE_INVALID", path, "Owned record is not a regular file")
	}
	return f, nil
}
func (s *Store) lstat(path string) (os.FileInfo, error) {
	parent, name, err := s.parent(path)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	var st unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	// Open only regular files/directories. Other types have no usable identity.
	flags := unix.O_RDONLY
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		flags |= unix.O_DIRECTORY
	} else if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fail("STATE_INVALID", path, "Owned path is a symlink or special file")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	return f.Stat()
}
func (s *Store) read(path string, limit int) ([]byte, os.FileInfo, error) {
	f, err := s.open(path, unix.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, nil, err
	}
	if len(b) > limit || !utf8.Valid(b) {
		return nil, nil, fail("STATE_INVALID", path, "Record exceeds its byte limit or has invalid UTF-8")
	}
	after, err := f.Stat()
	if err != nil || after.Size() != st.Size() {
		return nil, nil, fail("STATE_INVALID", path, "Record changed during read")
	}
	return b, st, nil
}
func (s *Store) site(name string) error {
	if s.Fault != nil {
		return s.Fault(name)
	}
	return nil
}
func (s *Store) syncFile(f *os.File, site string) error {
	if err := s.site(site + ".before"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return s.site(site + ".after")
}
func (s *Store) syncParent(path, site string) error {
	f, _, err := s.parent(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return s.syncFile(f, site)
}
func (s *Store) rename(from, to, site string) error {
	if err := s.site(site + ".before"); err != nil {
		return err
	}
	a, an, err := s.parent(from)
	if err != nil {
		return err
	}
	defer a.Close()
	b, bn, err := s.parent(to)
	if err != nil {
		return err
	}
	defer b.Close()
	if err := unix.Renameat(int(a.Fd()), an, int(b.Fd()), bn); err != nil {
		return err
	}
	return s.site(site + ".after")
}
func (s *Store) link(from, to, site string) error {
	if err := s.site(site + ".before"); err != nil {
		return err
	}
	a, an, err := s.parent(from)
	if err != nil {
		return err
	}
	defer a.Close()
	b, bn, err := s.parent(to)
	if err != nil {
		return err
	}
	defer b.Close()
	if err := unix.Linkat(int(a.Fd()), an, int(b.Fd()), bn, 0); err != nil {
		return err
	}
	return s.site(site + ".after")
}
func (s *Store) remove(path string) error {
	parent, name, err := s.parent(path)
	if err != nil {
		return err
	}
	defer parent.Close()
	return unix.Unlinkat(int(parent.Fd()), name, 0)
}
func (s *Store) mkdir(path string, mode uint32) error {
	// A top-level artifact already has the opened workspace as its parent.
	// Check that identity rather than passing "." to the leaf-path walker.
	if path == "." {
		return s.CheckIdentity()
	}
	parts := strings.Split(path, "/")
	prefix := ""
	for depth, part := range parts {
		if prefix != "" {
			prefix += "/"
		}
		prefix += part
		parent, name, err := s.parent(prefix)
		if err != nil {
			return err
		}
		err = unix.Mkdirat(int(parent.Fd()), name, mode)
		created := err == nil
		parent.Close()
		if err != nil && err != unix.EEXIST {
			return err
		}
		st, err := s.lstat(prefix)
		if err != nil || !st.IsDir() {
			return fail("STATE_INVALID", prefix, "Owned directory is not a real directory")
		}
		if (prefix == "state" || strings.HasPrefix(prefix, "state/")) && st.Mode().Perm()&0077 != 0 {
			return fail("STATE_INVALID", prefix, "Owned directory permissions are not private")
		}
		if created {
			dir, leaf, err := s.parent(prefix)
			if err != nil {
				return err
			}
			fd, err := unix.Openat(int(dir.Fd()), leaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			dir.Close()
			if err != nil {
				return err
			}
			f := os.NewFile(uintptr(fd), prefix)
			err = s.syncFile(f, fmt.Sprintf("mkdir-%d.file-fsync", depth))
			f.Close()
			if err != nil {
				return err
			}
			if err := s.syncParent(prefix, fmt.Sprintf("mkdir-%d.parent-fsync", depth)); err != nil {
				return err
			}
		} else {
			if err := s.syncParent(prefix, fmt.Sprintf("mkdir-%d.existing-parent-fsync", depth)); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) Prepare() error {
	for _, path := range []string{"state", "state/control", "state/transactions", "state/turns", "state/prompts", "state/inputs", "state/human", "state/human/archive", "state/logs", "rounds"} {
		mode := uint32(0700)
		if path == "rounds" {
			mode = 0777
		}
		if err := s.mkdir(path, mode); err != nil {
			return err
		}
	}
	f, err := s.open("state/inputs/inbox.lock", unix.O_RDWR|unix.O_CREAT, 0600)
	if err != nil {
		return err
	}
	return f.Close()
}
func (s *Store) AcquireOwner(ctx context.Context, wait time.Duration) error {
	if s.owner != nil {
		return fail("LOCKED", s.Path, "This store already has an owner")
	}
	f, err := s.lock(ctx, "state/owner.lock", wait)
	if err != nil {
		return err
	}
	s.owner = f
	s.ownerIdentity, err = f.Stat()
	if err != nil {
		f.Close()
		s.owner = nil
	}
	return err
}
func (s *Store) lock(ctx context.Context, path string, wait time.Duration) (*os.File, error) {
	f, err := s.open(path, unix.O_RDWR|unix.O_CREAT, 0600)
	if err != nil {
		return nil, err
	}
	if wait == 0 {
		wait = time.Second
	}
	end := time.Now().Add(wait)
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			f.Close()
			return nil, err
		}
		if time.Now().After(end) {
			f.Close()
			return nil, fail("LOCKED", path, "Wait, then inspect the workspace before retry")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func (s *Store) immutable(path string, b []byte, mode uint32, site string) error {
	if old, _, err := s.read(path, RecordLimit); err == nil {
		if bytes.Equal(old, b) {
			f, err := s.open(path, unix.O_RDONLY, 0)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := s.syncFile(f, site+".existing-fsync"); err != nil {
				return err
			}
			return s.syncParent(path, site+".existing-dir-fsync")
		}
		return fail("OUTPUT_CONFLICT", path, "Immutable record already has other bytes")
	} else if !os.IsNotExist(err) {
		return err
	}
	id, err := NewID()
	if err != nil {
		return err
	}
	pending := path + ".pending-" + id
	f, err := s.open(pending, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := s.syncFile(f, site+".file-fsync"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := s.link(pending, path, site+".publish"); err != nil {
		return err
	}
	if err := s.syncParent(path, site+".dir-fsync"); err != nil {
		return err
	}
	if err := s.remove(pending); err != nil {
		return err
	}
	return s.syncParent(path, site+".pending-remove-dir-fsync")
}
func observationOf(path string, b []byte, info os.FileInfo) FileObservation {
	st := info.Sys().(*syscall.Stat_t)
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	}
	hash := ""
	size := int64(0)
	if kind == "file" {
		hash = contract.HashBytes(b)
		size = info.Size()
	}
	return FileObservation{Path: path, Hash: hash, Kind: kind, Device: uint64(st.Dev), Inode: uint64(st.Ino), Bytes: size}
}
