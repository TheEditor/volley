// Package store owns durable snapshots, records, locks and integrity checks.
package store

import (
	"context"
	"encoding/json"
)

type FileObservation struct {
	Path   string
	Hash   string
	Kind   string
	Device uint64
	Inode  uint64
	Bytes  int64
}
type Inventory struct {
	Files           map[string]FileObservation
	JournalSequence uint64
	JournalHash     string
}
type AuthorizedChange struct {
	Path   string
	Before FileObservation
	After  FileObservation
	Kind   string
}
type Mutation struct {
	Actor  string
	Paths  []string
	Before map[string]FileObservation
	After  map[string]FileObservation
}
type Comparator interface {
	Snapshot(context.Context, string) (Inventory, error)
	Compare(context.Context, Inventory, []AuthorizedChange) (*Mutation, error)
}
type Artifact struct {
	StagedPath string
	TargetPath string
	Hash       string
}
type Transaction struct {
	ID               string
	PreviousRevision uint64
	PreviousHash     string
	Next             json.RawMessage
	Artifacts        []Artifact
}
type Records interface {
	Load(context.Context, string) (json.RawMessage, error)
	Commit(context.Context, string, Transaction) error
	SaveReceipt(context.Context, string, string, json.RawMessage) error
}
