package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// contextWithSignals ends on SIGINT or SIGTERM; the next run continues where this one stopped.
func contextWithSignals() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
