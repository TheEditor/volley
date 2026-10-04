//go:build darwin || linux

package store

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/TheEditor/volley/internal/contract"
	"golang.org/x/sys/unix"
)

func (s *Store) names(path string) ([]string, error) {
	p, leaf, err := s.parent(path)
	if err != nil {
		return nil, err
	}
	defer p.Close()
	fd, err := unix.Openat(int(p.Fd()), leaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	names, err := f.Readdirnames(-1)
	sort.Strings(names)
	return names, err
}
func transactionName(name string) (uint64, string, bool) {
	if !strings.HasSuffix(name, ".json") {
		return 0, "", false
	}
	parts := strings.SplitN(strings.TrimSuffix(name, ".json"), "-", 2)
	if len(parts) != 2 || !validID(parts[1]) {
		return 0, "", false
	}
	n, err := strconv.ParseUint(parts[0], 10, 64)
	return n, parts[1], err == nil && n > 0 && name == fmt.Sprintf("%08d-%s.json", n, parts[1])
}

// History follows checkpoint hashes backwards. Uncommitted records cannot select
// a different history, even when they claim the same revision.
func (s *Store) History() ([]Transaction, error) {
	current, hash, err := s.LoadSnapshot()
	if err != nil {
		return nil, err
	}
	var reverse []Transaction
	for current.Revision() > 0 {
		tx, err := s.readTransaction(current.Revision(), current.String("last_transaction_id"))
		if err != nil {
			return nil, err
		}
		if contract.HashBytes(tx.Next) != hash {
			return nil, fail("STATE_INVALID", txPath(current.Revision(), tx.ID), "Checkpoint hash differs")
		}
		if err := s.verifyArtifacts(tx); err != nil {
			return nil, err
		}
		reverse = append(reverse, tx)
		if tx.PreviousRevision == 0 {
			if tx.PreviousHash != "" || tx.Kind != "initialize" || tx.Receipt != nil {
				return nil, fail("STATE_INVALID", "state/transactions", "Invalid initial transaction")
			}
			break
		}
		names, err := s.names("state/transactions")
		if err != nil {
			return nil, err
		}
		var previous Snapshot
		found := false
		for _, name := range names {
			n, id, ok := transactionName(name)
			if !ok || n != tx.PreviousRevision {
				continue
			}
			older, err := s.readTransaction(n, id)
			if err != nil {
				return nil, err
			}
			if contract.HashBytes(older.Next) != tx.PreviousHash {
				continue
			}
			if found {
				return nil, fail("STATE_INVALID", "state/transactions", "Ambiguous checkpoint history")
			}
			m, err := parseRecord("manifest", older.Next)
			if err != nil {
				return nil, err
			}
			previous = Snapshot(m)
			if err := s.bindSnapshot(previous); err != nil {
				return nil, err
			}
			found = true
		}
		if !found || previous.String("run_id") != current.String("run_id") {
			return nil, fail("STATE_INVALID", "state/transactions", "Missing previous checkpoint")
		}
		if err := s.validateReceipt(tx.Receipt, previous); err != nil {
			return nil, err
		}
		if tx.Kind == "result" && tx.Receipt == nil {
			return nil, fail("STATE_INVALID", "state/transactions", "Result lacks receipt")
		}
		current = previous
		hash = tx.PreviousHash
	}
	history := make([]Transaction, len(reverse))
	for i := range reverse {
		history[len(reverse)-i-1] = reverse[i]
	}
	return history, nil
}

type Recovery struct {
	Snapshot   Snapshot
	Unfinished bool
	Receipt    *ReceiptRef
}

// Recover repairs storage only. An active intent remains unfinished. A saved
// receipt is returned to the engine, which must decide the result transition.
func (s *Store) Recover() (Recovery, error) {
	if err := s.checkOwner(); err != nil {
		return Recovery{}, err
	}
	m, _, err := s.LoadSnapshot()
	if err != nil && !os.IsNotExist(err) {
		return Recovery{}, err
	}
	revision := uint64(0)
	if err == nil {
		revision = m.Revision()
		if _, err := s.History(); err != nil {
			return Recovery{}, err
		}
	}
	names, err := s.names("state/transactions")
	if err != nil {
		return Recovery{}, err
	}
	var pending []Transaction
	for _, name := range names {
		n, id, ok := transactionName(name)
		if !ok || n != revision+1 {
			continue
		}
		tx, err := s.readTransaction(n, id)
		if err != nil {
			return Recovery{}, err
		}
		pending = append(pending, tx)
	}
	if len(pending) > 1 {
		return Recovery{}, fail("STATE_INVALID", "state/transactions", "Multiple next transactions retained")
	}
	if len(pending) == 1 {
		if err := s.CommitTransaction(pending[0]); err != nil {
			return Recovery{}, err
		}
	} else if revision > 0 {
		if err := s.ProjectEvents(); err != nil {
			return Recovery{}, err
		}
	} else {
		return Recovery{}, nil
	}
	m, _, err = s.LoadSnapshot()
	if err != nil {
		return Recovery{}, err
	}
	r := Recovery{Snapshot: m, Unfinished: len(m.Object("current_turn")) > 0}
	if r.Unfinished {
		turn := m.Object("current_turn")
		id, _ := turn["id"].(string)
		if !validID(id) {
			return r, fail("STATE_INVALID", "state/manifest.json", "Invalid active turn identity")
		}
		path := "state/turns/" + id + "/receipt.json"
		// The directory can be absent when death preceded receipt preparation.
		st, err := os.Lstat(s.Path + "/state/turns/" + id)
		if os.IsNotExist(err) {
			return r, nil
		}
		if err != nil || !st.IsDir() {
			return r, fail("STATE_INVALID", path, "Invalid turn directory")
		}
		b, _, err := s.read(path, RecordLimit)
		if os.IsNotExist(err) {
			return r, nil
		}
		if err != nil {
			return r, err
		}
		ref := ReceiptRef{path, contract.HashBytes(b)}
		if err := s.validateReceipt(&ref, m); err != nil {
			return r, err
		}
		f, err := s.open(path, unix.O_RDONLY, 0)
		if err != nil {
			return r, err
		}
		err = s.syncFile(f, "recovered-receipt.file-fsync")
		f.Close()
		if err != nil {
			return r, err
		}
		if err := s.syncParent(path, "recovered-receipt.dir-fsync"); err != nil {
			return r, err
		}
		r.Receipt = &ref
	}
	return r, nil
}

// ReadyIntent is the final storage gate before an engine can perform an action.
// A successful retry cannot prove delivery; backend recovery owns that decision.
func (s *Store) ReadyIntent(id string) error {
	if err := s.checkOwner(); err != nil {
		return err
	}
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return err
	}
	if len(m.Object("current_turn")) == 0 {
		return fail("STATE_INVALID", "state/manifest.json", "No matching committed active intent")
	}
	history, err := s.History()
	if err != nil {
		return err
	}
	found := false
	for _, tx := range history {
		if tx.ID != id || tx.Kind != "intent" {
			continue
		}
		next, err := parseRecord("manifest", tx.Next)
		if err != nil {
			return err
		}
		found = Snapshot(next).Object("current_turn")["id"] == m.Object("current_turn")["id"]
	}
	if !found {
		return fail("STATE_INVALID", "state/transactions", "Action requires the committed active intent")
	}
	f, err := s.open("state/manifest.json", unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := s.syncFile(f, "intent-gate.file-fsync"); err != nil {
		return err
	}
	return s.syncParent("state/manifest.json", "intent-gate.dir-fsync")
}
func completeLines(b []byte) ([][]byte, int) {
	end := bytes.LastIndexByte(b, '\n') + 1
	if end == 0 {
		return nil, 0
	}
	return bytes.Split(b[:end-1], []byte{'\n'}), end
}
