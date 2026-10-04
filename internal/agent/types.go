// Package agent owns repository-local direct provider operations.
package agent

import "github.com/TheEditor/volley/internal/review"

type Provider string

const (
	Claude Provider = "claude"
	Codex  Provider = "codex"
)

type Session struct {
	IntendedID      string
	ObservedID      string
	ConfiguredModel string
	ObservedModel   string
	Reason          string
}
type Adapter interface{ review.TurnAdapter }
