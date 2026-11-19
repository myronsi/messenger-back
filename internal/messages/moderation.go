package messages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

const (
	ReportContextSide   = 5
	MaxRemovalReasonLen = 1000
)

type ReportWindow struct {
	Target scylla.Message
	Before []scylla.Message
	After  []scylla.Message
}

func (s *Service) ReportWindow(ctx context.Context, userID, chatID, messageID int64) (ReportWindow, error) {
	p, err := s.History(ctx, userID, scylla.PageQuery{ChatID: chatID, Around: messageID, Limit: 4*ReportContextSide + 1})
	if err != nil {
		return ReportWindow{}, err
	}
	idx := -1
	for i, m := range p.Messages {
		if m.ID == messageID {
			idx = i
			break
		}
	}
	if idx < 0 || p.Messages[idx].Deleted {
		return ReportWindow{}, ErrNotFound
	}
	w := ReportWindow{Target: p.Messages[idx]}
	for i := idx - 1; i >= 0 && len(w.Before) < ReportContextSide; i-- {
		if copyable(p.Messages[i]) {
			w.Before = append([]scylla.Message{p.Messages[i]}, w.Before...)
		}
	}
	for i := idx + 1; i < len(p.Messages) && len(w.After) < ReportContextSide; i++ {
		if copyable(p.Messages[i]) {
			w.After = append(w.After, p.Messages[i])
		}
	}
	return w, nil
}

func copyable(m scylla.Message) bool {
	return !m.Deleted && m.Type != scylla.TypeSystem
}

func (s *Service) RemoveByModeration(ctx context.Context, chatID, messageID int64, reason scylla.ModerationRemoval) (scylla.Message, bool, error) {
	reason.Code = strings.TrimSpace(reason.Code)
	reason.Text = strings.TrimSpace(reason.Text)
	if reason.Code == "" || !validText(reason.Code, 64) {
		return scylla.Message{}, false, invalid("reason code is required")
	}
	if !utf8.ValidString(reason.Text) || utf8.RuneCountInString(reason.Text) > MaxRemovalReasonLen {
		return scylla.Message{}, false, invalid("reason text is at most 1000 characters")
	}
	m, err := s.message(ctx, chatID, messageID)
	if err != nil {
		return scylla.Message{}, false, err
	}
	if m.Type == scylla.TypeSystem {
		return scylla.Message{}, false, invalid("system messages cannot be removed")
	}
	if m.Moderation != nil {
		return m, false, nil
	}
	if err := s.d.Messages.Remove(ctx, chatID, messageID, reason); errors.Is(err, scylla.ErrNotFound) {
		return scylla.Message{}, false, ErrNotFound
	} else if err != nil {
		return scylla.Message{}, false, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	removed, err := s.message(ctx, chatID, messageID)
	if err != nil {
		return scylla.Message{}, false, err
	}
	if members, err := s.Members(ctx, chatID); err == nil {
		s.d.Notifier.MessageRemoved(ctx, removed, members)
	}
	s.emit(ctx, events.Event{Type: events.MessageDeleted, ChatID: chatID, MessageID: messageID})
	return removed, true, s.unlink(ctx, m)
}
