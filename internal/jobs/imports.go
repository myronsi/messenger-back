package jobs

import (
	"context"
	"errors"
	"log/slog"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

const GroupImportCleanup = "import-cleanup"

type RangeDeleter interface {
	DeleteRange(ctx context.Context, chatID, from, to int64) error
}

type ImportRows interface {
	ForgetMessages(ctx context.Context, chatID, from, to int64) error
	Delete(ctx context.Context, id int64) error
}

func ImportCleanup(messages RangeDeleter, imports ImportRows, log *slog.Logger) redis.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return func(ctx context.Context, entry redis.Entry) error {
		e, err := events.Parse(entry.Fields)
		if errors.Is(err, events.ErrMalformed) {
			log.WarnContext(ctx, "skipping malformed event", "entry", entry.ID)
			return nil
		}
		if e.Type != events.ImportRemoved || e.ChatID == 0 || e.ImportID == 0 || e.MessageID == 0 || e.UpTo < e.MessageID {
			return nil
		}
		if err := messages.DeleteRange(ctx, e.ChatID, e.MessageID, e.UpTo); err != nil {
			return err
		}
		if err := imports.ForgetMessages(ctx, e.ChatID, e.MessageID, e.UpTo); err != nil {
			return err
		}
		if err := imports.Delete(ctx, e.ImportID); err != nil {
			return err
		}
		log.InfoContext(ctx, "imported history removed", "chat_id", e.ChatID, "import_id", e.ImportID)
		return nil
	}
}
