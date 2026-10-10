// Package jobs holds the event handlers the worker runs in its consumer groups. Every handler is
// idempotent: entries are delivered at least once.
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

// Group names.
const (
	GroupChatCleanup = "chat-cleanup"
)

// ChatDeleter removes the messages of a chat (scylla.Messages).
type ChatDeleter interface {
	DeleteChat(ctx context.Context, chatID int64) error
}

// UnreadForgetter drops a chat from users' unread counters (redis.Unread).
type UnreadForgetter interface {
	Forget(ctx context.Context, chatID int64, userIDs ...int64) error
}

// ChatCleanup handles events:chats:
//
//   - chat.deleted removes the chat's messages from ScyllaDB, and once more after the grace period, for
//     writes that were in flight when the chat was deleted;
//   - chat.member_removed drops the chat from the user's unread counters.
func ChatCleanup(messages ChatDeleter, unread UnreadForgetter, grace time.Duration, log *slog.Logger) redis.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	now := time.Now
	return func(ctx context.Context, entry redis.Entry) error {
		e, err := events.Parse(entry.Fields)
		if errors.Is(err, events.ErrMalformed) {
			log.WarnContext(ctx, "skipping malformed event", "entry", entry.ID)
			return nil // retrying cannot help
		}
		switch e.Type {
		case events.ChatDeleted:
			if e.ChatID == 0 {
				return nil
			}
			if err := messages.DeleteChat(ctx, e.ChatID); err != nil {
				return err
			}
			if wait := e.At.Add(grace).Sub(now()); wait > 0 {
				return redis.RetryLater{After: wait}
			}
			log.InfoContext(ctx, "chat messages deleted", "chat_id", e.ChatID)
		case events.ChatMemberRemoved:
			if e.ChatID != 0 && e.UserID != 0 {
				return unread.Forget(ctx, e.ChatID, e.UserID)
			}
		}
		return nil
	}
}
