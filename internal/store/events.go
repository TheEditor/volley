//go:build darwin || linux

package store

import (
	"bytes"
	"os"

	"github.com/TheEditor/volley/internal/contract"
	"golang.org/x/sys/unix"
)

func eventBytes(tx Transaction) ([]byte, error) {
	m, err := parseRecord("manifest", tx.Next)
	if err != nil {
		return nil, err
	}
	return contract.Canonical(map[string]any{"record_version": 1, "run_id": m["run_id"], "transaction_id": tx.ID, "sequence": tx.PreviousRevision + 1, "at": tx.At, "kind": tx.Kind, "revision": tx.PreviousRevision + 1, "status": m["status"], "phase": m["phase"]})
}
func (s *Store) eventPrefix(history []Transaction) ([][]byte, int, error) {
	b, _, err := s.read("state/events.jsonl", RecordLimit)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	lines, end := completeLines(b)
	if len(lines) > len(history) {
		return nil, 0, fail("STATE_INVALID", "state/events.jsonl", "Event extends beyond committed history")
	}
	for i, line := range lines {
		if _, err := parseRecord("event", line); err != nil {
			return nil, 0, fail("STATE_INVALID", "state/events.jsonl", "Malformed complete event")
		}
		expected, err := eventBytes(history[i])
		if err != nil {
			return nil, 0, err
		}
		parsed, _ := parseRecord("event", line)
		canonical, _ := contract.Canonical(parsed)
		if !bytes.Equal(canonical, expected) {
			return nil, 0, fail("STATE_INVALID", "state/events.jsonl", "Event differs from committed transaction")
		}
	}
	return lines, end, nil
}
func (s *Store) projectEvents() error {
	history, err := s.History()
	if err != nil {
		return err
	}
	lines, end, err := s.eventPrefix(history)
	if err != nil {
		return err
	}
	f, err := s.open("state/events.jsonl", unix.O_RDWR|unix.O_CREAT, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	// Only an incomplete final suffix can be removed, under controller ownership.
	if err := f.Truncate(int64(end)); err != nil {
		return err
	}
	if _, err := f.Seek(int64(end), 0); err != nil {
		return err
	}
	for _, tx := range history[len(lines):] {
		b, err := eventBytes(tx)
		if err != nil {
			return err
		}
		if err := s.site("events.append.before"); err != nil {
			return err
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
		if err := s.site("events.append.after"); err != nil {
			return err
		}
	}
	if err := s.syncFile(f, "events.file-fsync"); err != nil {
		return err
	}
	return s.syncParent("state/events.jsonl", "events.dir-fsync")
}
func (s *Store) setAuditPending(pending bool) error {
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return err
	}
	if m["audit_pending"] == pending {
		return nil
	}
	m["audit_pending"] = pending
	m["event_sequence"] = m.Revision()
	tx, err := s.NewTransaction("audit", m, nil, nil)
	if err != nil {
		return err
	}
	return s.commitTransaction(tx, false)
}
func (s *Store) ProjectEvents() error {
	if err := s.checkOwner(); err != nil {
		return err
	}
	if err := s.projectEvents(); err != nil {
		// The original failure is retained. If this update also fails, readers still
		// reject a complete-evidence claim by checking the projection against history.
		_ = s.setAuditPending(true)
		return err
	}
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return err
	}
	if m["audit_pending"] == true {
		if err := s.setAuditPending(false); err != nil {
			return err
		}
		if err := s.projectEvents(); err != nil {
			_ = s.setAuditPending(true)
			return err
		}
	}
	return nil
}

// Events exposes the validated committed prefix; a final partial line is hidden.
func (s *Store) Events() ([]map[string]any, error) {
	history, err := s.History()
	if err != nil {
		return nil, err
	}
	lines, _, err := s.eventPrefix(history)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		m, err := parseRecord("event", line)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, nil
}
func (s *Store) EvidenceComplete() (bool, error) {
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return false, err
	}
	events, err := s.Events()
	if err != nil {
		return false, err
	}
	return m["audit_pending"] == false && uint64(len(events)) == m.Revision(), nil
}

// Index projection failure is a warning. It cannot change execution authority.
func RegisterIndex(write func() error) string {
	if err := write(); err != nil {
		return "INDEX_UNAVAILABLE"
	}
	return ""
}
