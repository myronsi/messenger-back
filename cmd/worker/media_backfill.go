package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/media"
)

func mediaBackfill(ctx context.Context, log *slog.Logger, svc *media.Service, args []string) error {
	fs := flag.NewFlagSet("media-backfill", flag.ContinueOnError)
	kindName := fs.String("kind", "", "video, audio, gif, audio-tags, image, stickers, emoji or avatars")
	limit := fs.Int("limit", 1000, "attachments to handle at most")
	pause := fs.Duration("pause", 200*time.Millisecond, "pause between attachments")
	if err := fs.Parse(args); err != nil {
		return err
	}
	kind, ok := media.BackfillKinds[*kindName]
	if !ok {
		return fmt.Errorf("media-backfill: unknown --kind %q", *kindName)
	}
	after := uuid.Nil
	seen, changed := 0, 0
	for seen < *limit {
		page, err := svc.BackfillPage(ctx, kind, after, min(200, *limit-seen))
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, a := range page {
			after = a.ID
			seen++
			var did bool
			switch {
			case kind.Refresh:
				did, err = svc.Refresh(ctx, a)
			case kind.Job == media.JobGIF && a.MimeType != "image/gif":
				continue
			default:
				did, err = svc.EnqueueJob(ctx, a, kind.Job)
			}
			if err != nil && !errors.Is(err, media.ErrNotFound) {
				log.WarnContext(ctx, "media backfill", "attachment_id", a.ID.String(), "error", err)
			}
			if did {
				changed++
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(*pause):
			}
		}
	}
	log.InfoContext(ctx, "media backfill finished", "kind", *kindName, "seen", seen, "changed", changed)
	return nil
}
