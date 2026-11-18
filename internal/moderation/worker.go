package moderation

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

const Group = "moderation"

func (s *Service) Handle(ctx context.Context, entry redis.Entry) error {
	e, err := events.Parse(entry.Fields)
	if errors.Is(err, events.ErrMalformed) {
		s.d.Log.WarnContext(ctx, "skipping malformed event", "entry", entry.ID)
		return nil
	}
	if err != nil || e.Type != events.UserReported || e.UserID == 0 {
		return nil
	}
	return s.Recompute(ctx, e.UserID)
}

type Claimer interface {
	Claim(ctx context.Context, id string, ttl time.Duration) (bool, error)
}

func (s *Service) RunDaily(ctx context.Context, log *slog.Logger, every time.Duration, once Claimer) {
	run := func() {
		if once != nil {
			slot := s.d.Now().UTC().Truncate(every).Format(time.RFC3339)
			claimed, err := once.Claim(ctx, slot, every)
			if err != nil || !claimed {
				return
			}
		}
		checked, purged, err := s.Daily(ctx)
		if err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "moderation daily job failed", "err", err)
			return
		}
		if checked+int(purged) > 0 {
			log.InfoContext(ctx, "moderation daily job", "accounts", checked, "purged_reports", purged)
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
