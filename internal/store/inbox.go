//go:build darwin || linux

package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/contract"
	"golang.org/x/sys/unix"
)

type InputReceipt struct {
	RecordVersion int     `json:"record_version"`
	RunID         string  `json:"run_id"`
	Kind          string  `json:"kind"`
	Key           string  `json:"key"`
	GenerationID  *string `json:"generation_id"`
	Text          string  `json:"text"`
	CreatedAt     string  `json:"created_at"`
}
type InboxEntry struct {
	RecordVersion int     `json:"record_version"`
	Sequence      uint64  `json:"sequence"`
	PreviousHash  string  `json:"previous_hash"`
	RunID         string  `json:"run_id"`
	Kind          string  `json:"kind"`
	Key           string  `json:"key"`
	GenerationID  *string `json:"generation_id"`
	ReceiptPath   string  `json:"receipt_path"`
	ReceiptHash   string  `json:"receipt_hash"`
}
type Inbox struct {
	Entries     []InboxEntry
	Hashes      []string
	Receipts    map[string]InputReceipt
	PrefixBytes int
}

func inputPath(kind, key string) string {
	return "state/inputs/" + kind + "-" + contract.HashBytes([]byte(key)) + ".json"
}
func generationEqual(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func (s *Store) checkLock(path string, f *os.File) error {
	observed, err := s.lstat(path)
	saved, e := f.Stat()
	if err != nil || e != nil || !os.SameFile(observed, saved) {
		return fail("STATE_INVALID", path, "Lock identity changed")
	}
	return s.CheckIdentity()
}
func (s *Store) readInbox(runID string) (Inbox, error) {
	result := Inbox{Receipts: make(map[string]InputReceipt)}
	b, _, err := s.read("state/inputs/inbox.jsonl", RecordLimit)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	lines, end := completeLines(b)
	result.PrefixBytes = end
	head := ""
	keys := make(map[string]bool)
	for i, line := range lines {
		var entry InboxEntry
		if err := decodeRecord("inbox-entry", line, &entry); err != nil {
			return result, fail("STATE_INVALID", "state/inputs/inbox.jsonl", "Malformed committed inbox entry")
		}
		if entry.Sequence != uint64(i+1) || entry.PreviousHash != head || entry.RunID != runID || entry.ReceiptPath != inputPath(entry.Kind, entry.Key) || keys[entry.ReceiptPath] {
			return result, fail("STATE_INVALID", "state/inputs/inbox.jsonl", "Inbox chain or receipt binding differs")
		}
		rb, _, err := s.read(entry.ReceiptPath, RecordLimit)
		if err != nil {
			return result, err
		}
		if contract.HashBytes(rb) != entry.ReceiptHash {
			return result, fail("STATE_INVALID", entry.ReceiptPath, "Input receipt hash differs")
		}
		var receipt InputReceipt
		if err := decodeRecord("input-receipt", rb, &receipt); err != nil {
			return result, err
		}
		if receipt.RunID != entry.RunID || receipt.Kind != entry.Kind || receipt.Key != entry.Key || !generationEqual(receipt.GenerationID, entry.GenerationID) {
			return result, fail("STATE_INVALID", entry.ReceiptPath, "Input receipt journal binding differs")
		}
		canonical, err := contract.Canonical(entry)
		if err != nil {
			return result, err
		}
		head = contract.HashBytes(canonical)
		keys[entry.ReceiptPath] = true
		result.Entries = append(result.Entries, entry)
		result.Hashes = append(result.Hashes, head)
		result.Receipts[entry.ReceiptPath] = receipt
	}
	return result, nil
}
func (s *Store) inboxLocked(ctx context.Context, wait time.Duration) (*os.File, error) {
	return s.lock(ctx, "state/inputs/inbox.lock", wait)
}

// ReadInbox holds the short lock. Pending receipts never appear as submissions.
func (s *Store) ReadInbox(ctx context.Context, runID string, wait time.Duration) (Inbox, error) {
	lock, err := s.inboxLocked(ctx, wait)
	if err != nil {
		return Inbox{}, err
	}
	defer lock.Close()
	inbox, err := s.readInbox(runID)
	if err != nil {
		return inbox, err
	}
	return inbox, s.checkLock("state/inputs/inbox.lock", lock)
}
func (s *Store) SubmitInput(ctx context.Context, receipt InputReceipt, wait time.Duration) (InboxEntry, error) {
	var zero InboxEntry
	if !validID(receipt.RunID) || receipt.Key == "" || len(receipt.Key) > 1024 || !utf8.ValidString(receipt.Key) || !utf8.ValidString(receipt.Text) || len(receipt.Text) > TextLimit {
		return zero, fail("INVALID_INPUT", "", "Invalid input identity, key or text")
	}
	if receipt.Kind == "answer" && (receipt.GenerationID == nil || !validID(*receipt.GenerationID)) || receipt.Kind != "answer" && receipt.GenerationID != nil {
		return zero, fail("INVALID_INPUT", "", "Invalid question generation binding")
	}
	receipt.RecordVersion = 1
	if receipt.CreatedAt == "" {
		receipt.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	encoded, err := contract.Canonical(receipt)
	if err != nil {
		return zero, err
	}
	if _, err := parseRecord("input-receipt", encoded); err != nil {
		return zero, err
	}
	lock, err := s.inboxLocked(ctx, wait)
	if err != nil {
		return zero, err
	}
	defer lock.Close()
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return zero, err
	}
	if m.String("run_id") != receipt.RunID {
		return zero, fail("STATE_INVALID", "state/inputs", "Input run identity differs")
	}
	inbox, err := s.readInbox(receipt.RunID)
	if err != nil {
		return zero, err
	}
	path := inputPath(receipt.Kind, receipt.Key)
	if b, _, err := s.read(path, RecordLimit); err == nil {
		var prior InputReceipt
		if err := decodeRecord("input-receipt", b, &prior); err != nil {
			return zero, err
		}
		if prior.RunID != receipt.RunID || prior.Kind != receipt.Kind || prior.Key != receipt.Key || prior.Text != receipt.Text || !generationEqual(prior.GenerationID, receipt.GenerationID) {
			return zero, fail("IDEMPOTENCY_CONFLICT", path, "Input key has different content")
		}
		// The first created-at value is retained for an identical retry.
		receipt = prior
		encoded = b
	} else if !os.IsNotExist(err) {
		return zero, err
	}
	if err := s.immutable(path, encoded, 0600, "input-receipt"); err != nil {
		return zero, err
	}
	// The complete prefix is synced again before a recovered acknowledgment.
	journal, err := s.open("state/inputs/inbox.jsonl", unix.O_RDWR|unix.O_CREAT, 0600)
	if err != nil {
		return zero, err
	}
	defer journal.Close()
	if err := journal.Truncate(int64(inbox.PrefixBytes)); err != nil {
		return zero, err
	}
	if _, err := journal.Seek(int64(inbox.PrefixBytes), 0); err != nil {
		return zero, err
	}
	var entry InboxEntry
	for _, candidate := range inbox.Entries {
		if candidate.ReceiptPath == path {
			entry = candidate
			break
		}
	}
	if entry.Sequence == 0 {
		head := ""
		if len(inbox.Hashes) > 0 {
			head = inbox.Hashes[len(inbox.Hashes)-1]
		}
		entry = InboxEntry{1, uint64(len(inbox.Entries) + 1), head, receipt.RunID, receipt.Kind, receipt.Key, receipt.GenerationID, path, contract.HashBytes(encoded)}
		b, err := contract.Canonical(entry)
		if err != nil {
			return zero, err
		}
		if err := s.site("inbox.append.before"); err != nil {
			return zero, err
		}
		if _, err := journal.Write(append(b, '\n')); err != nil {
			return zero, err
		}
		if err := s.site("inbox.append.after"); err != nil {
			return zero, err
		}
	}
	if err := s.syncFile(journal, "inbox.file-fsync"); err != nil {
		return zero, err
	}
	if err := s.syncParent("state/inputs/inbox.jsonl", "inbox.dir-fsync"); err != nil {
		return zero, err
	}
	if err := s.settleInputPending(path, encoded); err != nil {
		return zero, err
	}
	if err := s.checkLock("state/inputs/inbox.lock", lock); err != nil {
		return zero, err
	}
	return entry, nil
}
func (s *Store) settleInputPending(path string, canonical []byte) error {
	names, err := s.names("state/inputs")
	if err != nil {
		return err
	}
	prefix := strings.TrimPrefix(path, "state/inputs/") + ".pending-"
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		b, _, err := s.read("state/inputs/"+name, RecordLimit)
		if err != nil {
			return err
		}
		if string(b) != string(canonical) {
			return fail("STATE_INVALID", "state/inputs/"+name, "Interrupted input differs; evidence retained")
		}
		if err := s.remove("state/inputs/" + name); err != nil {
			return err
		}
	}
	return s.syncParent(path, "input-settle.dir-fsync")
}
func (i Inbox) Head() (uint64, string) {
	if len(i.Hashes) == 0 {
		return 0, ""
	}
	return uint64(len(i.Hashes)), i.Hashes[len(i.Hashes)-1]
}
func (i Inbox) String() string { n, h := i.Head(); return fmt.Sprintf("%d:%s", n, h) }
