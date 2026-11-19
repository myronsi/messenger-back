package jobs

import (
	"context"
	"log/slog"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type CallHooks interface {
	MembersRemoved(ctx context.Context, chatID int64, userIDs []int64)
	ChatsDeleted(ctx context.Context)
}

type ChatRemovedNotifier interface {
	Removed(ctx context.Context, chatID int64, userIDs []int64)
}

type UnreadCounters interface {
	Forget(ctx context.Context, chatID int64, userIDs ...int64) error
	Drop(ctx context.Context, userID int64) error
}

type MemberCache interface {
	Invalidate(ctx context.Context, chatIDs ...int64) error
}

type AccountCleanup struct {
	Calls      CallHooks
	Notifier   ChatRemovedNotifier
	Unread     UnreadCounters
	Events     Emitter
	Members    MemberCache
	DeleteFile func(ctx context.Context, key string) error
	Log        *slog.Logger
}

func (c AccountCleanup) AfterDeletion(ctx context.Context, userID int64, d postgres.DeletedAccount) {
	log := c.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if c.Calls != nil {
		for _, chatID := range d.LeftGroups {
			c.Calls.MembersRemoved(ctx, chatID, []int64{userID})
		}
		if len(d.DeletedChats) > 0 {
			c.Calls.ChatsDeleted(ctx)
		}
	}
	changed := make([]int64, 0, len(d.DeletedChats)+len(d.LeftGroups))
	for chatID, others := range d.DeletedChats {
		changed = append(changed, chatID)
		c.Notifier.Removed(ctx, chatID, others)
		if err := c.Unread.Forget(ctx, chatID, others...); err != nil {
			log.WarnContext(ctx, "forget unread", "error", err)
		}
		if err := c.Events.Emit(ctx, events.Event{Type: events.ChatDeleted, ChatID: chatID, ActorID: userID}); err != nil {
			log.WarnContext(ctx, "emit chat.deleted", "error", err)
		}
	}
	for _, chatID := range d.LeftGroups {
		changed = append(changed, chatID)
		if err := c.Events.Emit(ctx, events.Event{Type: events.ChatMemberRemoved, ChatID: chatID, UserID: userID, ActorID: userID}); err != nil {
			log.WarnContext(ctx, "emit chat.member_removed", "error", err)
		}
	}
	if err := c.Members.Invalidate(ctx, changed...); err != nil {
		log.WarnContext(ctx, "invalidate members", "error", err)
	}
	if err := c.Unread.Drop(ctx, userID); err != nil {
		log.WarnContext(ctx, "drop unread counters", "error", err)
	}
	for _, key := range d.AttachmentKeys {
		if err := c.DeleteFile(ctx, key); err != nil {
			log.WarnContext(ctx, "delete file of a deleted chat", "error", err)
		}
	}
}
