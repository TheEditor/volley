// Package process owns argument-vector processes and verified settlement.
package process

import (
	"context"
	"io"
	"time"
)

type Identity struct {
	PID      int
	PGID     int
	Start    string
	Verified bool
}
type Request struct {
	Path    string
	Args    []string
	Cwd     string
	Env     []string
	Timeout time.Duration
	Stdout  io.Writer
	Stderr  io.Writer
	OnStart func(Identity) error
}
type Result struct {
	Started     bool
	Identity    Identity
	Exit        int
	Signal      string
	TimedOut    bool
	Interrupted bool
	Settled     bool
	Duration    time.Duration
}
type Runner interface {
	Run(context.Context, Request) (Result, error)
}
