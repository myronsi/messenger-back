package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/search"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	maintenanceEvery = time.Hour
	// sessionRetention is how long ended sessions and the refresh rotation history are kept. After that a
	// replayed old refresh token is simply unknown instead of being recognised as reuse.
	sessionRetention = 30 * 24 * time.Hour
)

// maintain removes ended sessions, old refresh history and stale recovery tokens until ctx ends.
func maintain(ctx context.Context, log *slog.Logger, store *postgres.Store, every time.Duration) {
	run := func() {
		sessions, rotated, err := store.Sessions().PurgeEnded(ctx, sessionRetention)
		if err != nil {
			log.ErrorContext(ctx, "purging ended sessions failed", "err", err)
		} else if sessions+rotated > 0 {
			log.InfoContext(ctx, "purged ended sessions", "sessions", sessions, "rotated_tokens", rotated)
		}
		if n, err := store.RecoveryTokens().DeleteStale(ctx); err != nil {
			log.ErrorContext(ctx, "purging recovery tokens failed", "err", err)
		} else if n > 0 {
			log.InfoContext(ctx, "purged recovery tokens", "count", n)
		}
	}
	run()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// collectUploads deletes uploads nothing uses (after media.UnusedAfter) until ctx ends.
func collectUploads(ctx context.Context, log *slog.Logger, svc *media.Service, every time.Duration) {
	run := func() {
		for {
			n, err := svc.Collect(ctx, 500)
			if err != nil {
				log.ErrorContext(ctx, "collecting unused uploads failed", "err", err)
				return
			}
			if n > 0 {
				log.InfoContext(ctx, "deleted unused uploads", "count", n)
			}
			if n < 500 {
				return
			}
		}
	}
	run()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// reconcileSearch re-indexes the messages of the last window every period, to repair what the event stream
// missed (Redis was briefly away, entries trimmed before they were indexed).
func reconcileSearch(ctx context.Context, log *slog.Logger, in *search.Indexer, chats search.Chats, every, window time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			start := time.Now()
			if err := in.Reconcile(ctx, chats, window); err != nil && ctx.Err() == nil {
				log.ErrorContext(ctx, "search reconciliation failed", "err", err)
			} else {
				log.InfoContext(ctx, "search reconciled", "window", window, "took", time.Since(start))
			}
		}
	}
}

// ensureIndex sets up the search index, retrying until Elasticsearch answers; false when ctx ended first.
func ensureIndex(ctx context.Context, log *slog.Logger, ix *search.Index) bool {
	for wait := time.Second; ctx.Err() == nil; wait = min(2*wait, time.Minute) {
		err := ix.Ensure(ctx)
		if err == nil {
			return true
		}
		log.WarnContext(ctx, "search index not ready", "err", err)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	return false
}
