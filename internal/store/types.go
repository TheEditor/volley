// Package store owns durable snapshots, records, locks and integrity checks.
package store

import (
	"context"
	"encoding/json"
)

type FileObservation struct {
	Path   string `json:"path"`
	Hash   string `json:"hash"`
	Kind   string `json:"kind"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Bytes  int64  `json:"bytes"`
}
type Inventory struct {
	Actor           string
	AnswerTurn      bool
	HumanBefore     []byte
	AllowedHistory  []string
	RunID           string
	Files           map[string]FileObservation
	JournalSequence uint64
	JournalHash     string
	JournalBytes    int
}
type AuthorizedChange struct {
	Path          string
	Before, After FileObservation
	Kind          string
}
type Mutation struct {
	Actor         string
	Paths         []string
	Before, After map[string]FileObservation
}
type Comparator interface {
	Snapshot(context.Context, string) (Inventory, error)
	Compare(context.Context, Inventory, []AuthorizedChange) (*Mutation, error)
}
type Artifact struct {
	StagedPath string           `json:"staged_path"`
	TargetPath string           `json:"target_path"`
	Hash       string           `json:"hash"`
	Replace    bool             `json:"replace"`
	Before     *FileObservation `json:"before"`
}
type ReceiptRef struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}
type Transaction struct {
	RecordVersion    int             `json:"record_version"`
	ID               string          `json:"id"`
	Kind             string          `json:"kind"`
	PreviousRevision uint64          `json:"previous_revision"`
	PreviousHash     string          `json:"previous_hash"`
	Next             json.RawMessage `json:"next"`
	Artifacts        []Artifact      `json:"artifacts"`
	Receipt          *ReceiptRef     `json:"receipt"`
	At               string          `json:"at"`
}
type Records interface {
	Load(context.Context, string) (json.RawMessage, error)
	Commit(context.Context, string, Transaction) error
	SaveReceipt(context.Context, string, string, json.RawMessage) error
}
