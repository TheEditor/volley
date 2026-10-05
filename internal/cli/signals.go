package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func controllerContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-ch:
			if sig == syscall.SIGTERM {
				cancel(fmt.Errorf("SIGTERM"))
			} else {
				cancel(fmt.Errorf("SIGINT"))
			}
		case <-ctx.Done():
		}
	}()
	return ctx, func() { signal.Stop(ch); cancel(nil) }
}
