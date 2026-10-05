//go:build darwin || linux

package human

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/TheEditor/volley/internal/store"
)

type Generation struct {
	RecordVersion      int                   `json:"record_version"`
	ID                 string                `json:"id"`
	RunID              string                `json:"run_id"`
	OriginatingTurn    string                `json:"originating_turn"`
	NextRound          int                   `json:"next_round"`
	QuestionText       string                `json:"question_text"`
	QuestionHash       string                `json:"question_hash"`
	QuestionObserved   store.FileObservation `json:"question_observed"`
	HumanBaseline      store.FileObservation `json:"human_baseline"`
	HumanCanonicalPath string                `json:"human_canonical_path"`
	PriorDirective     string                `json:"prior_directive"`
}

func decode(b []byte, target any) error {
	if _, err := input.Record(bytes.NewReader(b), store.RecordLimit); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
func questionPath(id string) string { return "state/questions/" + id + ".json" }
func obsRecord(o store.FileObservation) map[string]any {
	return map[string]any{"path": o.Path, "sha256": o.Hash, "bytes": o.Bytes, "type": o.Kind, "device": o.Device, "inode": o.Inode}
}

// OpenQuestion commits the exact generation after a completed planner turn.
// The outer engine calls it only at a settled safe boundary.
func OpenQuestion(ctx context.Context, s *store.Store, originatingTurn string, nextRound int, c Clock) (*Generation, error) {
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return nil, err
	}
	if len(m.Object("current_turn")) != 0 {
		return nil, failure("TURN_UNCERTAIN", "Question cannot open during an unfinished turn")
	}
	if existing := m.Object("question"); len(existing) != 0 && existing["answered"] != true {
		return LoadGeneration(s, fmt.Sprint(existing["id"]))
	}
	questions, qobs, err := StableRead(ctx, s, "QUESTIONS.md", c)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(questions)) == 0 {
		return nil, nil
	}
	prior, hobs, err := StableRead(ctx, s, "HUMAN.md", c)
	if err != nil {
		return nil, err
	}
	_, check, err := s.ReadObserved("QUESTIONS.md", Limit)
	if err != nil || check != qobs {
		return nil, failure("ANSWER_CONFLICT", "Questions changed before generation commit")
	}
	id, err := store.NewID()
	if err != nil {
		return nil, err
	}
	g := Generation{1, id, m.String("run_id"), originatingTurn, nextRound, string(questions), contract.HashBytes(questions), qobs, hobs, filepath.Join(s.Path, "HUMAN.md"), string(prior)}
	b, err := contract.Canonical(g)
	if err != nil {
		return nil, err
	}
	hash, err := s.StagePrivateText(questionPath(id), b)
	if err != nil {
		return nil, err
	}
	m["question"] = map[string]any{"id": id, "sha256": g.QuestionHash, "text": g.QuestionText, "observed": obsRecord(hobs), "answered": false, "record_path": questionPath(id), "record_hash": hash, "originating_turn": originatingTurn, "next_round": nextRound}
	m["status"] = "awaiting_answer"
	m["phase"] = "await_answer"
	tx, err := s.NewTransaction("control", m, nil, nil)
	if err != nil {
		return nil, err
	}
	if err = s.CommitTransaction(tx); err != nil {
		return nil, err
	}
	return &g, nil
}
func LoadGeneration(s *store.Store, id string) (*Generation, error) {
	m, err := CheckCurrentGeneration(s, id)
	if err != nil {
		return nil, err
	}
	q := m.Object("question")
	if q["record_path"] != questionPath(id) {
		return nil, failure("STATE_INVALID", "Question record binding differs")
	}
	b, err := s.ReadText(questionPath(id))
	if err != nil {
		return nil, err
	}
	if q["record_hash"] != contract.HashBytes(b) {
		return nil, failure("STATE_INVALID", "Question record hash differs")
	}
	var g Generation
	if err = decode(b, &g); err != nil {
		return nil, err
	}
	if g.ID != id || g.RunID != m.String("run_id") || g.QuestionHash != q["sha256"] || contract.HashBytes([]byte(g.QuestionText)) != g.QuestionHash || g.HumanCanonicalPath != filepath.Join(s.Path, "HUMAN.md") {
		return nil, failure("STATE_INVALID", "Question identity differs")
	}
	return &g, nil
}

type Application struct {
	RecordVersion int                    `json:"record_version"`
	RunID         string                 `json:"run_id"`
	ReceiptPath   string                 `json:"receipt_path"`
	ReceiptHash   string                 `json:"receipt_hash"`
	GenerationID  string                 `json:"generation_id"`
	TurnID        string                 `json:"turn_id"`
	Kind          string                 `json:"kind"`
	Withdrawn     bool                   `json:"withdrawn"`
	QuestionPath  string                 `json:"question_path"`
	QuestionHash  string                 `json:"question_hash"`
	AnswerPath    string                 `json:"answer_path"`
	AnswerHash    string                 `json:"answer_hash"`
	PriorPath     string                 `json:"prior_path"`
	PriorHash     string                 `json:"prior_hash"`
	Source        *store.FileObservation `json:"source"`
}

func applicationPath(hash string) string { return "state/human/applied-" + hash + ".json" }
func point(s *store.Store, name string) error {
	if s.Fault != nil {
		return s.Fault(name)
	}
	return nil
}
func logicalTurn(hash string) string {
	b, err := hex.DecodeString(hash)
	if err != nil || len(b) != 32 {
		return ""
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:16]))
}
func stringsList(m store.Snapshot, key string) []string {
	var result []string
	switch entries := m[key].(type) {
	case []any:
		for _, e := range entries {
			result = append(result, fmt.Sprint(e))
		}
	case []string:
		result = entries
	}
	return result
}

// CommitApplication selects a committed inbox entry and archives its exact
// inputs before one checkpoint records the fixed logical planner turn.
func CommitApplication(ctx context.Context, s *store.Store, entry store.InboxEntry) (Application, error) {
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return Application{}, err
	}
	inbox, err := s.ReadInbox(ctx, m.String("run_id"), 0)
	if err != nil {
		return Application{}, err
	}
	var receipt store.InputReceipt
	matched := false
	for _, candidate := range inbox.Entries {
		if reflect.DeepEqual(candidate, entry) {
			receipt = inbox.Receipts[candidate.ReceiptPath]
			matched = true
			break
		}
	}
	if !matched {
		return Application{}, failure("ANSWER_CONFLICT", "Input has no matching journal commit")
	}
	// Resume a committed application without choosing a new turn or source.
	for _, id := range append(stringsList(m, "answers"), stringsList(m, "steering")...) {
		if id == entry.ReceiptHash {
			return LoadApplication(s, entry.ReceiptHash)
		}
	}
	if len(m.Object("current_turn")) != 0 {
		return Application{}, failure("TURN_UNCERTAIN", "Input waits for the current turn to settle")
	}
	if entry.Kind != "answer" && entry.Kind != "steer" {
		return Application{}, failure("INVALID_INPUT", "Input is not an answer or directive")
	}
	var g *Generation
	if entry.Kind == "answer" {
		if entry.GenerationID == nil {
			return Application{}, failure("ANSWER_CONFLICT", "Answer lacks a question")
		}
		g, err = LoadGeneration(s, *entry.GenerationID)
		if err != nil {
			return Application{}, err
		}
		for _, candidate := range inbox.Entries {
			if candidate.Kind == "answer" && candidate.GenerationID != nil && *candidate.GenerationID == g.ID {
				if candidate.ReceiptPath != entry.ReceiptPath {
					return Application{}, failure("ANSWER_CONFLICT", "An earlier committed answer wins")
				}
				break
			}
		}
	}
	base := "state/human/archive/" + entry.ReceiptHash
	a := Application{RecordVersion: 1, RunID: m.String("run_id"), ReceiptPath: entry.ReceiptPath, ReceiptHash: entry.ReceiptHash, Kind: entry.Kind, TurnID: logicalTurn(entry.ReceiptHash), Withdrawn: receipt.Withdrawn, AnswerPath: base + ".answer.md", PriorPath: base + ".prior.md", Source: receipt.Source}
	question := ""
	prior := receipt.PriorDirective
	if g != nil {
		a.GenerationID = g.ID
		a.QuestionPath = base + ".questions.md"
		question = g.QuestionText
		prior = g.PriorDirective
	}
	for _, artifact := range []struct {
		path, text string
		hash       *string
	}{{a.QuestionPath, question, &a.QuestionHash}, {a.AnswerPath, receipt.Text, &a.AnswerHash}, {a.PriorPath, prior, &a.PriorHash}} {
		if artifact.path == "" {
			continue
		}
		label := "prior"
		if artifact.path == a.QuestionPath {
			label = "question"
		}
		if artifact.path == a.AnswerPath {
			label = "answer"
		}
		if err := point(s, "human.archive-"+label+".before"); err != nil {
			return Application{}, err
		}
		hash, err := s.StagePrivateText(artifact.path, []byte(artifact.text))
		if err != nil {
			return Application{}, err
		}
		*artifact.hash = hash
		if err := point(s, "human.archive-"+label+".after"); err != nil {
			return Application{}, err
		}
	}
	b, err := contract.Canonical(a)
	if err != nil {
		return Application{}, err
	}
	hash, err := s.StagePrivateText(applicationPath(entry.ReceiptHash), b)
	if err != nil {
		return Application{}, err
	}
	key := "steering"
	if g != nil {
		key = "answers"
		m.Object("question")["answered"] = true
	}
	m[key] = append(stringsList(m, key), entry.ReceiptHash)
	applications, _ := m["applications"].([]any)
	applications = append(applications, map[string]any{"receipt_hash": entry.ReceiptHash, "record_path": applicationPath(entry.ReceiptHash), "record_hash": hash, "turn_id": a.TurnID, "planner_delivered": false, "critic_delivered": false})
	m["applications"] = applications
	// Steering stays pending while a question remains unanswered.
	if g != nil || len(m.Object("question")) == 0 || m.Object("question")["answered"] == true {
		m["status"] = "ready"
		m["phase"] = "apply_directive"
	}
	tx, err := s.NewTransaction("control", m, nil, nil)
	if err != nil {
		return Application{}, err
	}
	if err := point(s, "human.application.commit.before"); err != nil {
		return Application{}, err
	}
	if err = s.CommitTransaction(tx); err != nil {
		return Application{}, err
	}
	if err := point(s, "human.application.commit.after"); err != nil {
		return Application{}, err
	}
	return a, nil
}
func LoadApplication(s *store.Store, hash string) (Application, error) {
	if logicalTurn(hash) == "" {
		return Application{}, failure("STATE_INVALID", "Invalid input receipt hash")
	}
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return Application{}, err
	}
	var declaration map[string]any
	entries, _ := m["applications"].([]any)
	for _, entry := range entries {
		candidate, _ := entry.(map[string]any)
		if candidate["receipt_hash"] == hash {
			if declaration != nil {
				return Application{}, failure("STATE_INVALID", "Ambiguous input application")
			}
			declaration = candidate
		}
	}
	if declaration == nil || declaration["record_path"] != applicationPath(hash) {
		return Application{}, failure("STATE_INVALID", "Input application is not committed")
	}
	b, err := s.ReadText(applicationPath(hash))
	if err != nil {
		return Application{}, err
	}
	if declaration["record_hash"] != contract.HashBytes(b) {
		return Application{}, failure("STATE_INVALID", "Application record changed")
	}
	var a Application
	if err = decode(b, &a); err != nil {
		return Application{}, err
	}
	if a.ReceiptHash != hash || a.RunID != m.String("run_id") || a.TurnID != logicalTurn(hash) || declaration["turn_id"] != a.TurnID {
		return Application{}, failure("STATE_INVALID", "Application binding differs")
	}
	inbox, err := s.ReadInbox(context.Background(), a.RunID, 0)
	if err != nil {
		return Application{}, err
	}
	bound := false
	for _, entry := range inbox.Entries {
		if entry.ReceiptPath != a.ReceiptPath || entry.ReceiptHash != a.ReceiptHash {
			continue
		}
		r := inbox.Receipts[entry.ReceiptPath]
		generation := ""
		if r.GenerationID != nil {
			generation = *r.GenerationID
		}
		if r.Kind != a.Kind || r.Withdrawn != a.Withdrawn || generation != a.GenerationID || !reflect.DeepEqual(r.Source, a.Source) || contract.HashBytes([]byte(r.Text)) != a.AnswerHash {
			return Application{}, failure("STATE_INVALID", "Application differs from its committed input")
		}
		bound = true
	}
	if !bound {
		return Application{}, failure("STATE_INVALID", "Application has no committed input")
	}
	for _, role := range []string{"planner", "critic"} {
		if declaration[role+"_delivered"] == true {
			path, _ := declaration[role+"_receipt_path"].(string)
			hash, _ := declaration[role+"_receipt_hash"].(string)
			if err := checkDelivery(s, a, role, store.ReceiptRef{Path: path, Hash: hash}); err != nil {
				return Application{}, err
			}
		}
	}
	for _, pair := range [][2]string{{a.QuestionPath, a.QuestionHash}, {a.AnswerPath, a.AnswerHash}, {a.PriorPath, a.PriorHash}} {
		if pair[0] == "" {
			continue
		}
		text, err := s.ReadText(pair[0])
		if err != nil || contract.HashBytes(text) != pair[1] {
			return Application{}, failure("STATE_INVALID", "Archived input changed")
		}
	}
	return a, nil
}
func RemoveConsumedSource(s *store.Store, a Application) (bool, error) {
	checked, err := LoadApplication(s, a.ReceiptHash)
	if err != nil {
		return false, err
	}
	if !reflect.DeepEqual(checked, a) {
		return false, failure("STATE_INVALID", "Source removal application differs")
	}
	if a.Source == nil || a.Source.Kind == "absent" {
		return false, nil
	}
	return s.RemoveObserved(*a.Source)
}

// RecordDelivery requires a checked, saved turn receipt that names this input.
// It runs after the result checkpoint settles the turn. The engine owns the
// external delivery and never replaces an uncertain application with a new ID.
func RecordDelivery(s *store.Store, a Application, role string, ref store.ReceiptRef) error {
	if role != "planner" && role != "critic" {
		return failure("INVALID_INPUT", "Unknown directive role")
	}
	checked, err := LoadApplication(s, a.ReceiptHash)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(checked, a) {
		return failure("STATE_INVALID", "Application delivery binding differs")
	}
	if err := checkDelivery(s, a, role, ref); err != nil {
		return err
	}
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return err
	}
	if len(m.Object("current_turn")) != 0 {
		return failure("TURN_UNCERTAIN", "Delivery waits for the result checkpoint")
	}
	entries, _ := m["applications"].([]any)
	for _, entry := range entries {
		declaration := entry.(map[string]any)
		if declaration["receipt_hash"] != a.ReceiptHash {
			continue
		}
		key := role + "_receipt_path"
		hashKey := role + "_receipt_hash"
		if old, ok := declaration[key]; ok && old != "" && (old != ref.Path || declaration[hashKey] != ref.Hash) {
			return failure("STATE_INVALID", "Application already has another role delivery")
		}
		declaration[key] = ref.Path
		declaration[hashKey] = ref.Hash
		declaration[role+"_delivered"] = true
	}
	tx, err := s.NewTransaction("control", m, nil, nil)
	if err != nil {
		return err
	}
	return s.CommitTransaction(tx)
}

func checkDelivery(s *store.Store, a Application, role string, ref store.ReceiptRef) error {
	record, b, err := s.ReadRecord("receipt", ref.Path)
	if err != nil {
		return err
	}
	turn, _ := record["turn_id"].(string)
	if turn == "" || record["run_id"] != a.RunID || ref.Path != "state/turns/"+turn+"/receipt.json" || contract.HashBytes(b) != ref.Hash || record["role"] != role || role == "planner" && turn != a.TurnID {
		return failure("STATE_INVALID", "Directive delivery receipt differs")
	}
	completion, _ := record["completion"].(map[string]any)
	if completion["kind"] != "completed" || completion["settled"] != true {
		return failure("TURN_UNCERTAIN", "Directive delivery is not complete")
	}
	inputs, _ := record["input_receipt_ids"].([]any)
	found := false
	for _, id := range inputs {
		if id == a.ReceiptHash {
			found = true
		}
	}
	if !found {
		return failure("STATE_INVALID", "Turn receipt does not bind the input")
	}
	return nil
}
