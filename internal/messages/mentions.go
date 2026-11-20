package messages

import (
	"context"

	"github.com/myronsi/messenger-back/internal/mentions"
	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func (s *Service) resolveMentions(ctx context.Context, chatID, sender int64, text string) ([]int64, bool) {
	names, all := mentions.Parse(text)
	if all {
		p, err := s.d.Store.Chats().Participant(ctx, chatID, sender)
		if err != nil || (p.Role != postgres.RoleOwner && p.Role != postgres.RoleAdmin) {
			all = false
		}
	}
	if len(names) == 0 {
		return nil, all
	}
	found, err := s.d.Store.Sharing().MembersByUsername(ctx, chatID, names)
	if err != nil {
		s.d.Log.WarnContext(ctx, "resolve mentions", "error", err, "chat_id", chatID)
		return nil, all
	}
	ids := make([]int64, 0, len(names))
	for _, n := range names {
		if id, ok := found[n]; ok && id != sender {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, all
	}
	return ids, all
}

func (s *Service) recordMentions(ctx context.Context, m scylla.Message, members []int64, replace bool) {
	if m.SenderID == nil {
		return
	}
	targets := m.Mentions
	if m.MentionsAll {
		targets = without(members, *m.SenderID)
	}
	if len(targets) > 0 && s.allow(ctx, *m.SenderID, moderation.Mention) != nil {
		targets = nil
	}
	if len(targets) == 0 && !replace {
		return
	}
	ctx = context.WithoutCancel(ctx)
	var err error
	if replace {
		err = s.d.Store.Sharing().SetUnreadMentions(ctx, m.ChatID, m.ID, targets)
	} else {
		err = s.d.Store.Sharing().AddUnreadMentions(ctx, m.ChatID, m.ID, targets)
	}
	if err != nil {
		s.d.Log.WarnContext(ctx, "record mentions", "error", err, "chat_id", m.ChatID, "message_id", m.ID)
	}
}
