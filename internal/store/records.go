//go:build darwin || linux

package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"golang.org/x/sys/unix"
)

type Snapshot map[string]any

// ReadRecord is a read-only, no-follow read of a schema-validated owned record.
func (s *Store) ReadRecord(schema, path string) (map[string]any, []byte, error) {
	b, _, err := s.read(path, RecordLimit)
	if err != nil {
		return nil, nil, err
	}
	m, err := parseRecord(schema, b)
	return m, b, err
}

func (m Snapshot) String(key string) string { value, _ := m[key].(string); return value }
func (m Snapshot) Revision() uint64 {
	n, _ := strconv.ParseUint(fmt.Sprint(m["revision"]), 10, 64)
	return n
}
func (m Snapshot) Object(key string) map[string]any {
	value, _ := m[key].(map[string]any)
	return value
}
func parseRecord(name string, b []byte) (map[string]any, error) {
	v, err := input.Record(bytes.NewReader(b), RecordLimit)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Expected an owned record object")
	}
	if err := contract.Validate(name, m); err != nil {
		return nil, err
	}
	return m, nil
}
func decodeRecord(name string, b []byte, target any) error {
	m, err := parseRecord(name, b)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(encoded))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
func (s *Store) LoadSnapshot() (Snapshot, string, error) {
	if err := s.CheckIdentity(); err != nil {
		return nil, "", err
	}
	b, _, err := s.read("state/manifest.json", RecordLimit)
	if err != nil {
		return nil, "", err
	}
	raw, err := parseRecord("manifest", b)
	if err != nil {
		return nil, "", fail("STATE_INVALID", "state/manifest.json", err.Error())
	}
	m := Snapshot(raw)
	if err := s.bindSnapshot(m); err != nil {
		return nil, "", err
	}
	if ref := m.Object("resolution"); len(ref) > 0 {
		path, _ := ref["path"].(string)
		if filepath.Dir(path) != "state/control" || !strings.HasPrefix(filepath.Base(path), "resolution-") {
			return nil, "", fail("STATE_INVALID", path, "Resolution path is invalid")
		}
		record, encoded, e := s.ReadRecord("resolution-record", path)
		if e != nil {
			return nil, "", e
		}
		if contract.HashBytes(encoded) != ref["sha256"] || record["run_id"] != m["run_id"] || record["turn_id"] != ref["turn_id"] || record["completion_source"] != ref["completion_source"] || record["resolution"] != ref["kind"] {
			return nil, "", fail("STATE_INVALID", path, "Resolution record binding differs")
		}
	}
	return m, contract.HashBytes(b), nil
}
func (s *Store) bindSnapshot(m Snapshot) error {
	observed := observationOf(".", nil, s.identity)
	ownership := m.Object("ownership")
	if m.String("canonical_workspace") != s.Path || fmt.Sprint(ownership["device"]) != fmt.Sprint(observed.Device) || fmt.Sprint(ownership["inode"]) != fmt.Sprint(observed.Inode) {
		return fail("STATE_INVALID", "state/manifest.json", "Workspace ownership binding differs")
	}
	if m.Revision() < 1 || !validID(m.String("last_transaction_id")) {
		return fail("STATE_INVALID", "state/manifest.json", "Invalid revision or transaction identity")
	}
	return nil
}
func (s *Store) NewTransaction(kind string, next Snapshot, artifacts []Artifact, receipt *ReceiptRef) (Transaction, error) {
	var revision uint64
	hash := ""
	previous, h, err := s.LoadSnapshot()
	if err != nil && !os.IsNotExist(err) {
		return Transaction{}, err
	}
	if err == nil {
		revision = previous.Revision()
		hash = h
	}
	id, err := NewID()
	if err != nil {
		return Transaction{}, err
	}
	next["revision"] = revision + 1
	next["last_transaction_id"] = id
	b, err := contract.Canonical(next)
	if err != nil {
		return Transaction{}, err
	}
	if artifacts == nil {
		artifacts = []Artifact{}
	}
	return Transaction{1, id, kind, revision, hash, b, artifacts, receipt, time.Now().UTC().Format(time.RFC3339Nano)}, nil
}
func (s *Store) readTransaction(revision uint64, id string) (Transaction, error) {
	var tx Transaction
	b, _, err := s.read(txPath(revision, id), RecordLimit)
	if err != nil {
		return tx, err
	}
	err = decodeRecord("transaction", b, &tx)
	if err != nil {
		return tx, fail("STATE_INVALID", txPath(revision, id), err.Error())
	}
	if tx.ID != id || tx.PreviousRevision+1 != revision {
		return tx, fail("STATE_INVALID", txPath(revision, id), "Transaction name and binding differ")
	}
	return tx, nil
}
func txPath(revision uint64, id string) string {
	return fmt.Sprintf("state/transactions/%08d-%s.json", revision, id)
}
func (s *Store) validateReceipt(ref *ReceiptRef, previous Snapshot) error {
	if ref == nil {
		return nil
	}
	if previous == nil || len(previous.Object("current_turn")) == 0 {
		return fail("STATE_INVALID", ref.Path, "Receipt requires a committed active intent")
	}
	b, _, err := s.read(ref.Path, RecordLimit)
	if err != nil {
		return err
	}
	if contract.HashBytes(b) != ref.Hash {
		return fail("STATE_INVALID", ref.Path, "Receipt hash differs")
	}
	receipt, err := parseRecord("receipt", b)
	if err != nil {
		return fail("STATE_INVALID", ref.Path, err.Error())
	}
	if receipt["run_id"] != previous["run_id"] {
		return fail("STATE_INVALID", ref.Path, "Receipt run binding differs")
	}
	turn := previous.Object("current_turn")
	if len(turn) > 0 {
		if ref.Path != "state/turns/"+fmt.Sprint(turn["id"])+"/receipt.json" {
			return fail("STATE_INVALID", ref.Path, "Receipt path differs from active turn")
		}
		for _, binding := range [][2]string{{"turn_id", "id"}, {"purpose", "purpose"}, {"role", "role"}, {"round", "round"}, {"prompt_hash", "prompt_hash"}} {
			if fmt.Sprint(receipt[binding[0]]) != fmt.Sprint(turn[binding[1]]) {
				return fail("STATE_INVALID", ref.Path, "Receipt intent binding differs: "+binding[0])
			}
		}
		if receipt["spec_before_hash"] != previous["spec_hash"] {
			return fail("STATE_INVALID", ref.Path, "Receipt spec-before binding differs")
		}
	}
	return nil
}
func (s *Store) SaveTurnReceipt(turnID string, record map[string]any) (ReceiptRef, error) {
	if err := s.checkOwner(); err != nil {
		return ReceiptRef{}, err
	}
	if !validID(turnID) {
		return ReceiptRef{}, fail("STATE_INVALID", turnID, "Invalid turn identity")
	}
	if record["turn_id"] != turnID {
		return ReceiptRef{}, fail("STATE_INVALID", turnID, "Receipt turn identity differs")
	}
	if err := contract.Validate("receipt", record); err != nil {
		return ReceiptRef{}, err
	}
	b, err := contract.Canonical(record)
	if err != nil {
		return ReceiptRef{}, err
	}
	path := "state/turns/" + turnID + "/receipt.json"
	if err := s.mkdir(filepath.Dir(path), 0700); err != nil {
		return ReceiptRef{}, err
	}
	// Check intent bindings before the immutable publication.
	previous, _, err := s.LoadSnapshot()
	if err != nil {
		return ReceiptRef{}, err
	}
	turn := previous.Object("current_turn")
	if record["run_id"] != previous["run_id"] || turn["id"] != turnID {
		return ReceiptRef{}, fail("STATE_INVALID", path, "Receipt is not for the committed active turn")
	}
	for _, binding := range [][2]string{{"purpose", "purpose"}, {"role", "role"}, {"round", "round"}, {"prompt_hash", "prompt_hash"}} {
		if fmt.Sprint(record[binding[0]]) != fmt.Sprint(turn[binding[1]]) {
			return ReceiptRef{}, fail("STATE_INVALID", path, "Receipt intent binding differs")
		}
	}
	if record["spec_before_hash"] != previous["spec_hash"] {
		return ReceiptRef{}, fail("STATE_INVALID", path, "Receipt spec-before binding differs")
	}
	if err := s.immutable(path, b, 0600, "receipt"); err != nil {
		return ReceiptRef{}, err
	}
	return ReceiptRef{path, contract.HashBytes(b)}, nil
}
func (s *Store) StageText(path string, b []byte) (string, error) {
	return s.stageText(path, b, 0666)
}
func (s *Store) StagePrivateText(path string, b []byte) (string, error) {
	return s.stageText(path, b, 0600)
}
func (s *Store) stageText(path string, b []byte, mode uint32) (string, error) {
	if err := s.checkOwner(); err != nil {
		return "", err
	}
	if protectedAuthority(path) || strings.HasSuffix(path, "/receipt.json") {
		return "", fail("STATE_INVALID", path, "Text cannot replace a storage authority record")
	}
	if len(b) > TextLimit {
		return "", fail("INVALID_INPUT", path, "Text exceeds 32 MiB")
	}
	if !utf8.Valid(b) {
		return "", fail("INVALID_INPUT", path, "Text has invalid UTF-8")
	}
	if err := s.mkdir(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := s.immutable(path, b, mode, "artifact-stage"); err != nil {
		return "", err
	}
	return contract.HashBytes(b), nil
}
func (s *Store) verifyArtifacts(tx Transaction) error {
	for _, a := range tx.Artifacts {
		b, _, err := s.read(a.StagedPath, TextLimit)
		if err != nil {
			return err
		}
		if contract.HashBytes(b) != a.Hash {
			return fail("STATE_INVALID", a.StagedPath, "Staged artifact hash differs")
		}
		if _, err := clean(a.TargetPath); err != nil {
			return err
		}
		if protectedAuthority(a.TargetPath) {
			return fail("STATE_INVALID", a.TargetPath, "Artifact cannot replace checkpoint authority")
		}
	}
	return nil
}
func (s *Store) promote(tx Transaction) error {
	for i, a := range tx.Artifacts {
		stageBytes, stageInfo, err := s.read(a.StagedPath, TextLimit)
		if err != nil {
			return err
		}
		if contract.HashBytes(stageBytes) != a.Hash {
			return fail("STATE_INVALID", a.StagedPath, "Staged artifact changed")
		}
		if a.CopyCandidate != nil {
			if err := s.promoteSeedCopy(a, i); err != nil {
				return err
			}
			continue
		}
		targetBytes, targetInfo, err := s.read(a.TargetPath, TextLimit)
		if err == nil && (os.SameFile(stageInfo, targetInfo) || a.Replace) && contract.HashBytes(targetBytes) == a.Hash {
			if err := s.syncParent(a.TargetPath, fmt.Sprintf("artifact-%d.recovered-dir-fsync", i)); err != nil {
				return err
			}
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		site := fmt.Sprintf("artifact-%d", i)
		if a.Replace {
			if a.Before == nil || err != nil {
				return fail("OUTPUT_CONFLICT", a.TargetPath, "Replacement lacks original target evidence")
			}
			current := observationOf(a.TargetPath, targetBytes, targetInfo)
			if current != *a.Before {
				return fail("OUTPUT_CONFLICT", a.TargetPath, "Replacement target changed")
			}
			pending := a.TargetPath + ".promotion-" + tx.ID
			candidate, e := s.open(pending, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0666)
			if e == nil {
				if _, e = candidate.Write(stageBytes); e == nil {
					e = s.syncFile(candidate, site+".replacement-file-fsync")
				}
				closeErr := candidate.Close()
				if e != nil {
					return e
				}
				if closeErr != nil {
					return closeErr
				}
			} else if !os.IsExist(e) {
				return e
			} else {
				candidate, err := s.open(pending, unix.O_RDONLY, 0)
				if err != nil {
					return err
				}
				err = s.syncFile(candidate, site+".replacement-recovered-file-fsync")
				candidate.Close()
				if err != nil {
					return err
				}
			}
			pendingBytes, _, e := s.read(pending, TextLimit)
			if e != nil || contract.HashBytes(pendingBytes) != a.Hash {
				return fail("OUTPUT_CONFLICT", pending, "Promotion candidate is not the staged artifact")
			}
			if err := s.rename(pending, a.TargetPath, site+".rename"); err != nil {
				return err
			}
		} else {
			if err == nil {
				return fail("OUTPUT_CONFLICT", a.TargetPath, "Unexpected existing artifact")
			}
			if err := s.link(a.StagedPath, a.TargetPath, site+".link"); err != nil {
				return err
			}
		}
		if err := s.syncParent(a.TargetPath, site+".dir-fsync"); err != nil {
			return err
		}
		promoted, _, err := s.read(a.TargetPath, TextLimit)
		if err != nil || contract.HashBytes(promoted) != a.Hash {
			return fail("STATE_INVALID", a.TargetPath, "Promoted bytes differ")
		}
	}
	return nil
}
func (s *Store) writeManifest(tx Transaction) error {
	pending := "state/manifest.pending-" + tx.ID
	f, err := s.open(pending, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if os.IsExist(err) {
		old, _, e := s.read(pending, RecordLimit)
		if e != nil || !bytes.Equal(old, tx.Next) {
			return fail("STATE_INVALID", pending, "Partial manifest candidate is retained")
		}
		f, err = s.open(pending, unix.O_RDONLY, 0)
	}
	if err != nil {
		return err
	}
	if st, _ := f.Stat(); st.Size() == 0 {
		if _, err := f.Write(tx.Next); err != nil {
			f.Close()
			return err
		}
	}
	if err := s.syncFile(f, "manifest.file-fsync"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := s.checkOwner(); err != nil {
		return err
	}
	if err := s.rename(pending, "state/manifest.json", "manifest.rename"); err != nil {
		return err
	}
	return s.syncParent("state/manifest.json", "manifest.dir-fsync")
}
func (s *Store) CommitTransaction(tx Transaction) error {
	return s.commitTransaction(tx, true)
}
func (s *Store) commitTransaction(tx Transaction, project bool) error {
	if err := s.checkOwner(); err != nil {
		return err
	}
	b, err := contract.Canonical(tx)
	if err != nil {
		return err
	}
	if _, err := parseRecord("transaction", b); err != nil {
		return err
	}
	previous, hash, err := s.LoadSnapshot()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err != nil {
		previous = nil
		hash = ""
	}
	if previous != nil {
		if _, err := s.History(); err != nil {
			return err
		}
	}
	next, err := parseRecord("manifest", tx.Next)
	if err != nil {
		return err
	}
	if err := s.bindSnapshot(Snapshot(next)); err != nil {
		return err
	}
	if previous != nil && previous.Revision() == tx.PreviousRevision+1 && previous.String("last_transaction_id") == tx.ID {
		if hash != contract.HashBytes(tx.Next) {
			return fail("STATE_INVALID", "state/manifest.json", "Committed transaction bytes differ")
		}
		if project {
			return s.ProjectEvents()
		}
		return nil
	}
	if (previous == nil && tx.PreviousRevision != 0) || previous != nil && previous.Revision() != tx.PreviousRevision || hash != tx.PreviousHash {
		return fail("STATE_INVALID", "state/transactions", "Previous checkpoint binding differs")
	}
	if Snapshot(next).Revision() != tx.PreviousRevision+1 || next["last_transaction_id"] != tx.ID {
		return fail("STATE_INVALID", "state/transactions", "Next checkpoint binding differs")
	}
	if previous != nil && next["run_id"] != previous["run_id"] {
		return fail("STATE_INVALID", "state/transactions", "Run identity changed")
	}
	if previous == nil && (tx.Kind != "initialize" || tx.Receipt != nil) || previous != nil && tx.Kind == "initialize" {
		return fail("STATE_INVALID", "state/transactions", "Initialization requires an absent checkpoint and no receipt")
	}
	if tx.Kind == "result" && tx.Receipt == nil {
		return fail("STATE_INVALID", "state/transactions", "Result transition requires a durable receipt")
	}
	if err := s.validateReceipt(tx.Receipt, previous); err != nil {
		return err
	}
	if err := s.verifyArtifacts(tx); err != nil {
		return err
	}
	if err := s.immutable(txPath(tx.PreviousRevision+1, tx.ID), b, 0600, "transaction"); err != nil {
		return err
	}
	if err := s.promote(tx); err != nil {
		return err
	}
	if err := s.writeManifest(tx); err != nil {
		return err
	}
	if project {
		return s.ProjectEvents()
	}
	return nil
}

func protectedAuthority(path string) bool {
	p, err := clean(path)
	if err != nil {
		return true
	}
	return p == "state/manifest.json" || strings.HasPrefix(p, "state/manifest.pending-") || p == "state/events.jsonl" || strings.HasSuffix(p, ".lock") || strings.HasPrefix(p, "state/transactions/") || strings.HasPrefix(p, "state/inputs/")
}
