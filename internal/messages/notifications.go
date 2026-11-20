package messages

import (
	"context"
	"fmt"
	"slices"

	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func (s *Service) queueNotification(ctx context.Context, m scylla.Message, members []int64) error {
	if s.d.QueueNotification == nil || m.SenderID == nil || m.Type == scylla.TypeSystem {
		return nil
	}
	recipients := without(members, *m.SenderID)
	if len(recipients) == 0 {
		return nil
	}
	return s.d.QueueNotification(ctx, m.ChatID, m.ID, recipients)
}

func (s *Service) notificationReady(ctx context.Context, m scylla.Message) {
	if s.d.NotificationReady == nil || m.SenderID == nil || m.Type == scylla.TypeSystem {
		return
	}
	if err := s.d.NotificationReady(context.WithoutCancel(ctx), m.ID); err != nil {
		s.d.Log.WarnContext(ctx, "notification ready", "error", err, "message_id", m.ID)
	}
}

func (s *Service) requestHolders(ctx context.Context, chatID int64) ([]int64, error) {
	holders, err := s.d.Store.Chats().RequestHolders(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return holders, nil
}

func (s *Service) repliedToRequest(ctx context.Context, chatID, senderID int64, holders []int64) {
	if !slices.Contains(holders, senderID) {
		return
	}
	accepted, err := s.d.Store.Chats().AcceptRequest(context.WithoutCancel(ctx), senderID, chatID)
	if err != nil {
		s.d.Log.WarnContext(ctx, "accept chat request", "error", err, "chat_id", chatID)
		return
	}
	if hook := s.requestAccepted.Load(); accepted && hook != nil {
		(*hook)(ctx, chatID, senderID)
	}
}

func (s *Service) holdsRequest(ctx context.Context, chatID, userID int64) bool {
	p, err := s.d.Store.Chats().Participant(ctx, chatID, userID)
	return err != nil || p.IsRequest
}

func withoutAll(ids, drop []int64) []int64 {
	if len(drop) == 0 {
		return ids
	}
	out := make([]int64, 0, len(ids))
	for _, x := range ids {
		if !slices.Contains(drop, x) {
			out = append(out, x)
		}
	}
	return out
}
