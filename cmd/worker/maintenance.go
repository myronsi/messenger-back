package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/myronsi/messenger-back/internal/calls"
	"github.com/myronsi/messenger-back/internal/jobs"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/search"
	"github.com/myronsi/messenger-back/internal/stickers"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	maintenanceEvery   = time.Hour
	deletionSweepEvery = 5 * time.Minute
	deletionLostAfter  = 15 * time.Minute
	// sessionRetention is how long ended sessions and the refresh rotation history are kept. After that a
	// replayed old refresh token is simply unknown instead of being recognised as reuse.
	sessionRetention = 30 * 24 * time.Hour
)

// maintain removes expired recovery capabilities, ended sessions and old refresh history until ctx ends.
func maintain(ctx context.Context, log *slog.Logger, store *postgres.Store, every time.Duration) {
	run := func() {
		if n, err := store.PurgeRecovery(ctx); err != nil {
			log.ErrorContext(ctx, "purging expired recovery capabilities failed", "err", err)
		} else if n > 0 {
			log.InfoContext(ctx, "purged expired recovery capabilities", "count", n)
		}
		sessions, rotated, err := store.Sessions().PurgeEnded(ctx, sessionRetention)
		if err != nil {
			log.ErrorContext(ctx, "purging ended sessions failed", "err", err)
		} else if sessions+rotated > 0 {
			log.InfoContext(ctx, "purged ended sessions", "sessions", sessions, "rotated_tokens", rotated)
		}
		purged, err := store.Calls().Purge(ctx, calls.Retention)
		if err != nil {
			log.ErrorContext(ctx, "purging old calls failed", "err", err)
		} else if purged.Calls+purged.StartRequests+purged.Envelopes+purged.Identities > 0 {
			log.InfoContext(ctx, "purged call data", "calls", purged.Calls, "start_requests", purged.StartRequests,
				"envelopes", purged.Envelopes, "identities", purged.Identities)
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

func resendChatDeletions(ctx context.Context, log *slog.Logger, store jobs.DeletionStore, emit jobs.Emitter, every, olderThan time.Duration) {
	run := func() {
		n, err := jobs.ResendChatDeletions(ctx, store, emit, olderThan, 500)
		if err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "resending chat deletions failed", "err", err)
		}
		if n > 0 {
			log.WarnContext(ctx, "resent lost chat deletions", "count", n)
		}
	}
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

func seedStickers(ctx context.Context, log *slog.Logger, store *postgres.Store, storage media.Storage) {
	deps := stickers.SeedDeps{
		Stickers: store.Stickers(), Attachments: store.Attachments(),
		Put: func(ctx context.Context, key string, data []byte, contentType string) error {
			return storage.Put(ctx, key, bytes.NewReader(data), int64(len(data)), contentType)
		},
	}
	for {
		err := stickers.Seed(ctx, deps, stickers.Builtin())
		if err == nil {
			log.InfoContext(ctx, "built-in sticker packs are up to date")
			return
		}
		log.WarnContext(ctx, "seeding built-in sticker packs failed", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func unhideStickerPack(ctx context.Context, log *slog.Logger, store *postgres.Store, arg string) error {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("unhide-sticker-pack: %q is not a pack id", arg)
	}
	changed, err := stickers.New(store.Stickers()).Unhide(ctx, id)
	if err != nil {
		return err
	}
	log.InfoContext(ctx, "sticker pack unhidden", "pack_id", id, "changed", changed)
	return nil
}
