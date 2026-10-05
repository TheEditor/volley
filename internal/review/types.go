// Package review owns pure decisions. It does not launch or inspect agents.
package review

import (
	"context"
	"time"
)

type Status string
type Phase string
type OutcomeKind string

const (
	Ready            Status      = "ready"
	Running          Status      = "running"
	AwaitingAnswer   Status      = "awaiting_answer"
	Handover         Status      = "handover"
	Impasse          Status      = "impasse"
	Approved         Status      = "approved"
	Stopped          Status      = "stopped"
	Completed        OutcomeKind = "completed"
	NotStarted       OutcomeKind = "definitely-not-started"
	Uncertain        OutcomeKind = "delivery-uncertain"
	WithCursor       OutcomeKind = "running-with-cursor"
	Failed           OutcomeKind = "failed-with-evidence"
	IdentityConflict OutcomeKind = "identity-conflict"
)

type Snapshot struct {
	Status      Status
	Phase       Phase
	Round       int
	Cap         int
	CurrentTurn string
	Revision    uint64
}
type Observation struct {
	Kind         string
	Turn         TurnOutcome
	ArtifactHash string
	QuestionID   string
	DirectiveIDs []string
}
type Action struct {
	Kind    string
	Purpose string
	Role    string
	Next    Snapshot
}
type TurnRequest struct {
	RunID             string
	TurnID            string
	Purpose           string
	Role              string
	Provider          string
	Round             int
	Attempt           int
	Workspace         string
	Prompt            []byte
	PromptHash        string
	ExpectedArtifacts []string
	SpecBeforeHash    string
	SessionID         string
	Timeout           time.Duration
	ElapsedBefore     time.Duration
}
type PreparedTurn struct {
	Request          TurnRequest
	Argv             []string
	Payload          []byte
	IntendedIdentity string
	CapabilityFacts  map[string]bool
}
type DeliveryReceipt struct {
	Kind        OutcomeKind
	Cursor      string
	PayloadHash string
	PaneUUID    string
	Replayed    bool
}
type CompletionReceipt struct {
	Kind          OutcomeKind
	Exit          int
	Settled       bool
	Cursor        string
	SessionID     string
	EvidencePaths []string
}
type Reply struct {
	Kind       string
	Path       string
	Hash       string
	Reason     string
	Root       string
	Source     string
	Format     string
	Version    string
	SessionID  string
	PromptPath string
}

// Record supplies the schema names for an authoritative turn receipt.
func (r Reply) Record() map[string]any {
	return map[string]any{"kind": r.Kind, "path": r.Path, "sha256": r.Hash, "reason": r.Reason, "root": r.Root, "source": r.Source, "format": r.Format, "version": r.Version, "session_id": r.SessionID, "prompt_path": r.PromptPath}
}

type RetentionDecision struct {
	Kind         string
	Reason       string
	OwnedTargets []string
}
type TurnOutcome struct {
	Kind           OutcomeKind
	Delivery       DeliveryReceipt
	Completion     CompletionReceipt
	Reply          Reply
	Retention      RetentionDecision
	ArtifactHashes map[string]string
	Warnings       []string
}
type Operation func(context.Context) (TurnOutcome, error)
type TurnAdapter interface {
	Prepare(context.Context, TurnRequest) (PreparedTurn, error)
	Perform(context.Context, PreparedTurn) (TurnOutcome, error)
}
type GuardedTurnExecutor interface {
	Execute(context.Context, TurnRequest, Operation) (TurnOutcome, error)
}
type ExternalCallGate interface {
	Check(context.Context, string) error
}
