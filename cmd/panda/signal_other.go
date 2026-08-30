//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// shutdownContext returns the context that ends the process and a cancel that
// triggers a controlled shutdown. On unix the semantics are exactly the
// historical ones: SIGINT or SIGTERM cancel the context. cancel is also the
// handle the guard package hands to a panicking resident goroutine, so the
// NotifyContext result is wrapped in an ordinary WithCancel — stop() alone
// only deregisters the signal handler and would never cancel the context.
func shutdownContext() (context.Context, context.CancelFunc) {
	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(baseCtx)
	return ctx, func() {
		stop()
		cancel()
	}
}
