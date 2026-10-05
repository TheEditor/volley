//go:build darwin || linux

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
	"golang.org/x/sys/unix"
)

type Entry struct {
	RunID        string `json:"run_id"`
	Workspace    string `json:"workspace"`
	CreatedAt    string `json:"created_at"`
	Registration uint64 `json:"registration"`
}
type index struct {
	Version  int     `json:"version"`
	Revision uint64  `json:"revision"`
	Entries  []Entry `json:"entries"`
}

const indexLimit = 8 << 20

func readIndex(dir *os.File) (index, []byte, error) {
	v := index{Version: 1, Entries: []Entry{}}
	fd, err := unix.Openat(int(dir.Fd()), "index.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return v, nil, nil
	}
	if err != nil {
		return v, nil, err
	}
	f := os.NewFile(uintptr(fd), "index.json")
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return v, nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return v, nil, failure("STATE_INVALID", "Index must be a private regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, indexLimit+1))
	if err != nil {
		return v, nil, err
	}
	if len(b) > indexLimit {
		return v, nil, failure("STATE_INVALID", "Index exceeds its size limit")
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err = d.Decode(&v); err != nil {
		return v, nil, failure("STATE_INVALID", "Malformed run index")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return v, nil, failure("STATE_INVALID", "Trailing index data")
	}
	if v.Version != 1 {
		return v, nil, failure("STATE_INVALID", "Unsupported run index version")
	}
	seen := map[string]bool{}
	for _, e := range v.Entries {
		if !IsID(e.RunID) || !filepath.IsAbs(e.Workspace) || e.Registration == 0 || e.Registration > v.Revision || seen[e.RunID] {
			return v, nil, failure("STATE_INVALID", "Invalid index entry")
		}
		if _, err = time.Parse(time.RFC3339Nano, e.CreatedAt); err != nil {
			return v, nil, failure("STATE_INVALID", "Invalid creation time")
		}
		seen[e.RunID] = true
	}
	return v, b, nil
}
func (o Options) openIndex(create bool) (*os.File, error) {
	if o.IndexDir == "" || !filepath.IsAbs(o.IndexDir) {
		return nil, failure("INVALID_CONFIG", "An absolute state directory is required")
	}
	if create {
		if err := os.MkdirAll(o.IndexDir, 0700); err != nil {
			return nil, err
		}
	}
	fd, err := unix.Open(o.IndexDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), o.IndexDir)
	st, err := f.Stat()
	if err != nil || st.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, failure("STATE_INVALID", "Index directory must be private")
	}
	return f, nil
}
func (o Options) loadIndex() (index, error) {
	f, err := o.openIndex(false)
	if os.IsNotExist(err) {
		return index{Version: 1, Entries: []Entry{}}, nil
	}
	if err != nil {
		return index{}, err
	}
	defer f.Close()
	v, _, err := readIndex(f)
	return v, err
}
func (o Options) editIndex(ctx context.Context, edit func(*index) error) error {
	dir, err := o.openIndex(true)
	if err != nil {
		return err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), "index.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), "index.lock")
	defer lock.Close()
	st, err := lock.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return failure("STATE_INVALID", "Invalid index lock")
	}
	end := time.Now().Add(time.Second)
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return err
		}
		if time.Now().After(end) {
			return failure("LOCKED", "Run index is busy")
		}
		if err = pause(ctx); err != nil {
			return err
		}
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	id, err := store.NewID()
	if err != nil {
		return err
	}
	name := "index-" + id + ".tmp"
	v, before, err := readIndex(dir)
	if err != nil {
		return err
	}
	if err = edit(&v); err != nil {
		return err
	}
	b, err := contract.Canonical(v)
	if err != nil {
		return err
	}
	if len(b) > indexLimit {
		return failure("STATE_INVALID", "Run index is full")
	}
	fd, err = unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	tmp := os.NewFile(uintptr(fd), name)
	defer unix.Unlinkat(int(dir.Fd()), name, 0)
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	_, current, err := readIndex(dir)
	if err != nil {
		return err
	}
	if contract.HashBytes(current) != contract.HashBytes(before) {
		return failure("STATE_INVALID", "Run index changed outside its lock")
	}
	old, err := dir.Stat()
	if err != nil {
		return err
	}
	now, err := os.Lstat(o.IndexDir)
	if err != nil || !os.SameFile(old, now) {
		return failure("STATE_INVALID", "Index directory identity changed")
	}
	var check unix.Stat_t
	if err = unix.Fstatat(int(dir.Fd()), "index.lock", &check, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if uint64(check.Ino) != inode(st) {
		return failure("STATE_INVALID", "Index lock identity changed")
	}
	if err = unix.Renameat(int(dir.Fd()), name, int(dir.Fd()), "index.json"); err != nil {
		return err
	}
	return dir.Sync()
}
func inode(st os.FileInfo) uint64 {
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(stat.Ino)
}
func (o Options) Register(ctx context.Context, m store.Snapshot) error {
	return o.editIndex(ctx, func(v *index) error {
		for _, e := range v.Entries {
			if e.RunID == m.String("run_id") {
				if e.Workspace != m.String("canonical_workspace") {
					return failure("STATE_INVALID", "Run ID is registered for another workspace")
				}
				return nil
			}
		}
		v.Revision++
		v.Entries = append(v.Entries, Entry{m.String("run_id"), m.String("canonical_workspace"), m.String("created_at"), v.Revision})
		return nil
	})
}

type listCursor struct {
	Scope     string `json:"scope"`
	Upper     uint64 `json:"upper"`
	CreatedAt string `json:"created_at"`
	RunID     string `json:"run_id"`
	Fields    string `json:"fields"`
	Filter    string `json:"filter"`
}

func selectedFields(text string) ([]string, error) {
	if text == "" {
		return []string{"run_id", "workspace", "created_at", "status", "revision"}, nil
	}
	seen := map[string]bool{"run_id": true, "workspace": true}
	allowed := map[string]bool{"run_id": true, "workspace": true, "created_at": true, "status": true, "revision": true}
	for _, f := range strings.Split(text, ",") {
		if !allowed[f] || seen[f] && f != "run_id" && f != "workspace" {
			return nil, failure("INVALID_INPUT", "Unknown or repeated list field")
		}
		seen[f] = true
	}
	fields := []string{}
	for f := range seen {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	return fields, nil
}
func item(e Entry) map[string]any {
	m := map[string]any{"run_id": e.RunID, "workspace": e.Workspace, "created_at": e.CreatedAt, "status": "unavailable", "revision": 0}
	s, err := store.Open(e.Workspace)
	if err != nil {
		return m
	}
	defer s.Close()
	v, err := read(s)
	if err == nil && s.Path == e.Workspace && v.String("run_id") == e.RunID {
		m["status"] = v["status"]
		m["revision"] = v.Revision()
	}
	return m
}
func (o Options) List(r Request) (map[string]any, error) {
	fields, err := selectedFields(r.Fields)
	if err != nil {
		return nil, err
	}
	v, err := o.loadIndex()
	if err != nil {
		return nil, err
	}
	scope := contract.HashBytes([]byte(o.IndexDir))
	c := listCursor{Scope: scope, Upper: v.Revision, Fields: strings.Join(fields, ","), Filter: r.Filter}
	if r.Cursor != "" {
		if err = decodeCursor(r.Cursor, &c); err != nil {
			return nil, err
		}
		if c.Scope != scope || c.Upper > v.Revision || c.Fields != strings.Join(fields, ",") || c.Filter != r.Filter || c.RunID != "" && !IsID(c.RunID) {
			return nil, failure("INVALID_INPUT", "Cursor is incompatible with this list")
		}
		if c.RunID != "" {
			if _, err = time.Parse(time.RFC3339Nano, c.CreatedAt); err != nil {
				return nil, failure("INVALID_INPUT", "Invalid list position")
			}
		}
	}
	sort.Slice(v.Entries, func(i, j int) bool {
		a, b := v.Entries[i], v.Entries[j]
		at, _ := time.Parse(time.RFC3339Nano, a.CreatedAt)
		bt, _ := time.Parse(time.RFC3339Nano, b.CreatedAt)
		if at.Equal(bt) {
			return a.RunID < b.RunID
		}
		return at.After(bt)
	})
	items := []any{}
	truncated := false
	for _, e := range v.Entries {
		et, _ := time.Parse(time.RFC3339Nano, e.CreatedAt)
		ct, _ := time.Parse(time.RFC3339Nano, c.CreatedAt)
		if e.Registration > c.Upper || c.RunID != "" && (et.After(ct) || et.Equal(ct) && e.RunID <= c.RunID) {
			continue
		}
		full := item(e)
		if r.Filter != "" && full["status"] != r.Filter {
			continue
		}
		if len(items) == r.Limit {
			truncated = true
			break
		}
		m := map[string]any{}
		for _, f := range fields {
			m[f] = full[f]
		}
		items = append(items, m)
		c.CreatedAt = e.CreatedAt
		c.RunID = e.RunID
	}
	return map[string]any{"items": items, "cursor": encodeCursor(c), "pagination": map[string]any{"limit": r.Limit, "returned": len(items)}, "truncated": truncated}, nil
}
func (o Options) Prune(ctx context.Context, r Request) (map[string]any, error) {
	if r.OlderThan <= 0 {
		return nil, failure("INVALID_INPUT", "A positive older-than duration is required")
	}
	if !r.DryRun && !r.Yes {
		return nil, failure("ACK_REQUIRED", "Prune requires --yes or --dry-run")
	}
	if r.DryRun && r.Yes {
		return nil, failure("INVALID_INPUT", "Choose --yes or --dry-run")
	}
	now := time.Now()
	if o.Now != nil {
		now = o.Now()
	}
	effects := []any{}
	removed := []any{}
	eligible := func(e Entry) bool {
		created, err := time.Parse(time.RFC3339Nano, e.CreatedAt)
		if err != nil || now.Sub(created) <= r.OlderThan {
			return false
		}
		s, err := store.Open(e.Workspace)
		if err != nil {
			return false
		}
		defer s.Close()
		m, err := read(s)
		if err != nil || m.String("run_id") != e.RunID || s.Path != e.Workspace || s.ObserveOwner() != "absent" || !terminal(m.String("status")) || m.Object("cleanup")["pending"] == true || len(m.Object("current_turn")) > 0 || len(m.Object("question")) > 0 {
			return false
		}
		return true
	}
	edit := func(v *index) error {
		kept := []Entry{}
		for _, e := range v.Entries {
			if eligible(e) {
				m := item(e)
				effects = append(effects, m)
				if !r.DryRun {
					removed = append(removed, m)
					continue
				}
			}
			kept = append(kept, e)
		}
		if !r.DryRun {
			v.Entries = kept
			v.Revision++
		}
		return nil
	}
	if r.DryRun {
		v, err := o.loadIndex()
		if err != nil {
			return nil, err
		}
		edit(&v)
	} else if err := o.editIndex(ctx, edit); err != nil {
		return nil, err
	}
	return map[string]any{"removed": removed, "effects": effects, "dry_run": r.DryRun}, nil
}
