package messages

import (
	"context"
	"errors"
	"time"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	ImportIdleTimeout = 24 * time.Hour
	ImportExpiryEvery = 10 * time.Minute
	importExpiryBatch = 50
)

func (s *Service) ExpireIdleImports(ctx context.Context, now time.Time) (int, error) {
	repo, _, err := s.imports()
	if err != nil {
		return 0, err
	}
	idle, err := repo.Idle(ctx, now.Add(-ImportIdleTimeout), importExpiryBatch)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, imp := range idle {
		removing, err := repo.MarkRemoving(ctx, imp.ID)
		if errors.Is(err, postgres.ErrNotFound) {
			continue
		}
		if err != nil {
			return removed, err
		}
		var actor int64
		if removing.ImportedBy != nil {
			actor = *removing.ImportedBy
		}
		s.recordChange(ctx, postgres.SyncChange{ChatID: removing.ChatID, Kind: postgres.SyncHistoryImported})
		s.emit(ctx, events.Event{Type: events.ImportRemoved, ChatID: removing.ChatID, ImportID: removing.ID, MessageID: removing.FirstMessageID, UpTo: removing.LastMessageID, ActorID: actor})
		s.importChanged(ctx, removing)
		s.d.Log.InfoContext(ctx, "idle import removed", "import_id", removing.ID, "chat_id", removing.ChatID, "next_index", removing.NextIndex, "message_count", removing.MessageCount)
		removed++
	}
	return removed, nil
}

func (s *Service) RunImportExpiry(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for {
				n, err := s.ExpireIdleImports(ctx, time.Now())
				if err != nil {
					if ctx.Err() == nil {
						s.d.Log.WarnContext(ctx, "expire idle imports", "error", err)
					}
					break
				}
				if n < importExpiryBatch {
					break
				}
			}
		}
	}
}
