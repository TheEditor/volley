//go:build darwin || linux

package engine

import (
	"context"
	"github.com/TheEditor/volley/internal/store"
	"time"
)

type stopControl struct{ Receipt store.ReceiptRef }

func (s stopControl) Error() string { return "Cooperative controller stop" }

// The monitor reads only the durable inbox. Cancellation goes through the
// current executor, which owns its process handles; it never trusts saved PIDs.
func watchStop(parent context.Context, path, runID string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s, err := store.Open(path)
		if err != nil {
			return
		}
		defer s.Close()
		for {
			inbox, err := s.SavedInbox(runID)
			if err == nil {
				for _, entry := range inbox.Entries {
					if entry.Kind == "control" && inbox.Receipts[entry.ReceiptPath].Text == "stop" {
						cancel(stopControl{store.ReceiptRef{Path: entry.ReceiptPath, Hash: entry.ReceiptHash}})
						return
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(25 * time.Millisecond):
			}
		}
	}()
	return ctx, func() { cancel(nil); <-done }
}
