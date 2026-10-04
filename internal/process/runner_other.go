//go:build !darwin && !linux

package process

import (
	"context"
	"fmt"
)

type UnixRunner struct{}

func (UnixRunner) Run(context.Context, Request) (Result, error) {
	return Result{Outcome: NotStarted, Exit: -1}, fmt.Errorf("UNSUPPORTED_PLATFORM: process control requires macOS or Linux")
}
