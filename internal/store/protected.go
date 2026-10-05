//go:build darwin || linux

package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"golang.org/x/sys/unix"
)

type Protection struct {
	Actor          string
	AnswerTurn     bool
	AllowedHistory []string
}

func ReservedRoundName(name string) bool {
	if name == "second-opinion.md" || strings.HasPrefix(name, "closing-") {
		return true
	}
	if !strings.HasPrefix(name, "r") {
		return false
	}
	i := 1
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 1 {
		return false
	}
	switch name[i:] {
	case ".critique.md", ".critique-retry.md", ".response.md", ".answer-response.md", ".spec.md", ".human.md", ".questions.md":
		return true
	}
	return false
}
func (s *Store) Observe(path string) (FileObservation, error) {
	info, err := s.lstat(path)
	if err != nil {
		return FileObservation{}, err
	}
	if info.IsDir() {
		return observationOf(path, nil, info), nil
	}
	b, info, err := s.read(path, TextLimit)
	if err != nil {
		return FileObservation{}, err
	}
	// A second descriptor read detects concurrent changes without using mtimes as
	// authority. Persistent modification is checked against the retained inventory.
	again, other, err := s.read(path, TextLimit)
	if err != nil || !os.SameFile(info, other) || !bytes.Equal(b, again) {
		return FileObservation{}, fail("ARTIFACT_CHANGED", path, "File changed during observation")
	}
	return observationOf(path, b, info), nil
}
func (s *Store) protectedFiles(p Protection) (map[string]FileObservation, error) {
	files := make(map[string]FileObservation)
	root := observationOf(".", nil, s.identity)
	root.Path = s.Path
	files["."] = root
	var walk func(string) error
	walk = func(path string) error {
		// Lstat captures an unsafe leaf as evidence. All reads and directory walks use
		// the descriptor-relative no-follow API, so an unsafe parent is never followed.
		st, err := os.Lstat(filepath.Join(s.Path, path))
		if os.IsNotExist(err) {
			files[path] = FileObservation{Path: filepath.Join(s.Path, path), Kind: "absent"}
			return nil
		}
		if err != nil {
			return err
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			o := observationOf(path, nil, st)
			o.Kind = "special"
			if st.Mode()&os.ModeSymlink != 0 {
				o.Kind = "symlink"
			}
			o.Path = filepath.Join(s.Path, path)
			files[path] = o
			return nil
		}
		o, err := s.Observe(path)
		if err != nil {
			o = observationOf(path, nil, st)
			o.Kind = "unreadable"
			o.Path = filepath.Join(s.Path, path)
			files[path] = o
			return nil
		}
		o.Path = filepath.Join(s.Path, path)
		files[path] = o
		if st.IsDir() {
			names, err := s.names(path)
			if err != nil {
				return err
			}
			for _, name := range names {
				if err := walk(path + "/" + name); err != nil {
					return err
				}
			}
		}
		return nil
	}
	paths := []string{"state", "rounds", "volley.config.toml", "gashki.config.toml", "BRIEF.md", "CONSTRAINTS.md"}
	if p.Actor == "critic" || p.AnswerTurn {
		paths = append(paths, "SPEC.md", "QUESTIONS.md")
	}
	if !p.AnswerTurn {
		paths = append(paths, "HUMAN.md")
	}
	for _, path := range paths {
		if err := walk(path); err != nil {
			return nil, err
		}
	}
	return files, nil
}
func (s *Store) normalizeInbox(inbox Inbox) error {
	f, err := s.open("state/inputs/inbox.jsonl", unix.O_RDWR, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() != int64(inbox.PrefixBytes) {
		if err := f.Truncate(int64(inbox.PrefixBytes)); err != nil {
			return err
		}
	}
	if err := s.syncFile(f, "inbox-prefix.file-fsync"); err != nil {
		return err
	}
	if err := s.syncParent("state/inputs/inbox.jsonl", "inbox-prefix.dir-fsync"); err != nil {
		return err
	}
	for path := range inbox.Receipts {
		b, _, err := s.read(path, RecordLimit)
		if err != nil {
			return err
		}
		if err := s.settleInputPending(path, b); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) Snapshot(ctx context.Context, actor string) (Inventory, error) {
	return s.Inspect(ctx, Protection{Actor: actor})
}
func (s *Store) Inspect(ctx context.Context, p Protection) (Inventory, error) {
	lock, err := s.inboxLocked(ctx, time.Second)
	if err != nil {
		return Inventory{}, err
	}
	defer lock.Close()
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return Inventory{}, err
	}
	inbox, err := s.readInbox(m.String("run_id"))
	if err != nil {
		return Inventory{}, err
	}
	if err := s.normalizeInbox(inbox); err != nil {
		return Inventory{}, err
	}
	files, err := s.protectedFiles(p)
	if err != nil {
		return Inventory{}, err
	}
	for path, observation := range files {
		if observation.Kind == "unreadable" || observation.Kind == "symlink" || observation.Kind == "special" {
			return Inventory{}, fail("STATE_INVALID", path, "Protected inventory contains an unsafe path")
		}
	}
	n, h := inbox.Head()
	result := Inventory{Files: files, JournalSequence: n, JournalHash: h, JournalBytes: inbox.PrefixBytes, Actor: p.Actor, AnswerTurn: p.AnswerTurn, AllowedHistory: append([]string(nil), p.AllowedHistory...), RunID: m.String("run_id")}
	if p.AnswerTurn {
		b, _, err := s.read("HUMAN.md", TextLimit)
		if err != nil && !os.IsNotExist(err) {
			return Inventory{}, err
		}
		result.HumanBefore = b
	}
	return result, s.checkLock("state/inputs/inbox.lock", lock)
}
func mutation(actor string, before, after map[string]FileObservation, paths []string) *Mutation {
	sort.Strings(paths)
	m := &Mutation{Actor: actor, Paths: paths, Before: make(map[string]FileObservation), After: make(map[string]FileObservation)}
	for _, path := range paths {
		m.Before[path] = before[path]
		m.After[path] = after[path]
	}
	return m
}

// Compare uses the retained inventory, never a changed manifest. Journal
// consistency cannot authenticate a writer who can forge receipt and journal.
func (s *Store) Compare(ctx context.Context, expected Inventory, changes []AuthorizedChange) (*Mutation, error) {
	lock, err := s.inboxLocked(ctx, time.Second)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	inbox, err := s.readInbox(expected.RunID)
	if err != nil {
		return mutation(expected.Actor, expected.Files, nil, []string{"state/inputs/inbox.jsonl"}), err
	}
	if uint64(len(inbox.Hashes)) < expected.JournalSequence || expected.JournalSequence > 0 && inbox.Hashes[expected.JournalSequence-1] != expected.JournalHash {
		return mutation(expected.Actor, expected.Files, nil, []string{"state/inputs/inbox.jsonl"}), fail("STATE_INVALID", "state/inputs/inbox.jsonl", "Committed prefix changed")
	}
	if expected.JournalBytes > 0 {
		b, _, err := s.read("state/inputs/inbox.jsonl", RecordLimit)
		if err != nil || len(b) < expected.JournalBytes || contract.HashBytes(b[:expected.JournalBytes]) != expected.Files["state/inputs/inbox.jsonl"].Hash {
			return mutation(expected.Actor, expected.Files, nil, []string{"state/inputs/inbox.jsonl"}), fail("STATE_INVALID", "state/inputs/inbox.jsonl", "Exact committed prefix bytes changed")
		}
	}
	if err := s.normalizeInbox(inbox); err != nil {
		return nil, err
	}
	after, err := s.protectedFiles(Protection{Actor: expected.Actor, AnswerTurn: expected.AnswerTurn, AllowedHistory: expected.AllowedHistory})
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool)
	for _, change := range changes {
		before := expected.Files[change.Path]
		observed := after[change.Path]
		if before != change.Before || observed != change.After || change.Kind == "" {
			return nil, fail("STATE_INVALID", change.Path, "Authorized delta does not match its exact observations")
		}
		allowed[change.Path] = true
	}
	for _, entry := range inbox.Entries[expected.JournalSequence:] {
		allowed[entry.ReceiptPath] = true
	}
	if uint64(len(inbox.Entries)) > expected.JournalSequence {
		allowed["state/inputs/inbox.jsonl"] = true
	}
	var changed []string
	for path, before := range expected.Files {
		if before != after[path] && !allowed[path] {
			changed = append(changed, path)
		}
	}
	for path := range after {
		if _, known := expected.Files[path]; known || allowed[path] {
			continue
		}
		accept := false
		if strings.HasPrefix(path, "rounds/") && !ReservedRoundName(filepath.Base(path)) && after[path].Kind == "file" {
			for _, name := range expected.AllowedHistory {
				if name == "*" || name == path {
					accept = true
					break
				}
			}
		}
		if !accept {
			changed = append(changed, path)
		}
	}
	if expected.AnswerTurn {
		b, _, err := s.read("HUMAN.md", TextLimit)
		if err != nil && (!os.IsNotExist(err) || len(expected.HumanBefore) > 0) || err == nil && !bytes.HasPrefix(b, expected.HumanBefore) {
			changed = append(changed, "HUMAN.md")
		}
	}
	if err := s.checkLock("state/inputs/inbox.lock", lock); err != nil {
		return nil, err
	}
	if len(changed) > 0 {
		return mutation(expected.Actor, expected.Files, after, changed), nil
	}
	return nil, nil
}
