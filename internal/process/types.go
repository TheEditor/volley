// Package process owns argument-vector processes and verified settlement.
package process

import (
	"context"
	"io"
	"os"
	"time"
)

type Identity struct {
	PID      int    `json:"pid"`
	PGID     int    `json:"pgid"`
	Start    string `json:"start"`
	Verified bool   `json:"verified"`
}
type Request struct {
	Path    string
	Args    []string
	Cwd     string
	Env     []string
	Timeout time.Duration
	// ElapsedBefore is the saved, already consumed part of the same turn budget.
	ElapsedBefore time.Duration
	Stdout        io.Writer
	Stderr        io.Writer
	// Input is an explicit owned tool payload. Agent calls leave it empty.
	// It never reads the controller's input descriptor.
	Input []byte
	// UserTTY is an explicit editor-only terminal descriptor. Agent requests
	// leave it nil. It is never the controller answer input descriptor.
	UserTTY *os.File
	OnStart func(Identity) error
}
type Outcome string

const (
	Exited          Outcome = "exited"
	Interrupted     Outcome = "interrupted"
	NotStarted      Outcome = "not-started"
	Uncertain       Outcome = "uncertain"
	DeadlineExpired Outcome = "deadline-expired"
)

type Result struct {
	Outcome          Outcome       `json:"outcome"`
	Started          bool          `json:"started"`
	Identity         Identity      `json:"identity"`
	Exit             int           `json:"exit"`
	Signal           string        `json:"signal"`
	TimedOut         bool          `json:"timed_out"`
	Interrupted      bool          `json:"interrupted"`
	Settled          bool          `json:"settled"`
	Duration         time.Duration `json:"duration"`
	BudgetConsumed   time.Duration `json:"budget_consumed"`
	Signals          []string      `json:"signals"`
	ObservedMembers  []Identity    `json:"observed_members"`
	RemainingMembers int           `json:"remaining_members"`
}
type Runner interface {
	Run(context.Context, Request) (Result, error)
}

// ValidateIdentifier is for vendor model and effort values. Paths and ordinary
// argv entries can contain whitespace; all values reject NUL in the runner.
func ValidateIdentifier(value string) error { return validateIdentifier(value) }
