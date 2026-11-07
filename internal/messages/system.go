package messages

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

const (
	SystemGroupCreated  = "group_created"
	SystemMemberAdded   = "member_added"
	SystemMemberJoined  = "member_joined"
	SystemMemberRemoved = "member_removed"
	SystemMemberLeft    = "member_left"
	SystemCall          = "call"
)

type SystemEvent struct {
	Kind       string       `json:"kind"`
	ActorID    *int64       `json:"actor_id,omitempty"`
	ActorName  string       `json:"actor_name,omitempty"`
	TargetID   *int64       `json:"target_id,omitempty"`
	TargetName string       `json:"target_name,omitempty"`
	Call       *CallSummary `json:"call,omitempty"`
}

type CallSummary struct {
	CallID          int64  `json:"call_id,string"`
	Kind            string `json:"kind"`
	Outcome         string `json:"outcome"`
	DurationSeconds *int   `json:"duration_seconds"`
}

func ParseSystemEvent(m scylla.Message) (SystemEvent, bool) {
	if m.Type != scylla.TypeSystem || m.Content == nil {
		return SystemEvent{}, false
	}
	var e SystemEvent
	if err := json.Unmarshal([]byte(*m.Content), &e); err != nil || e.Kind == "" {
		return SystemEvent{}, false
	}
	return e, true
}

func (s *Service) SendSystem(ctx context.Context, chatID int64, e SystemEvent) (scylla.Message, error) {
	id, err := s.SystemMessageID()
	if err != nil {
		return scylla.Message{}, err
	}
	return s.SendSystemAs(ctx, chatID, id, e)
}

func (s *Service) SystemMessageID() (int64, error) {
	id, err := s.d.IDs.Next()
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return id, nil
}

func (s *Service) SendSystemAs(ctx context.Context, chatID, id int64, e SystemEvent) (scylla.Message, error) {
	ctx = context.WithoutCancel(ctx)
	body, err := json.Marshal(e)
	if err != nil {
		return scylla.Message{}, err
	}
	members, err := s.Members(ctx, chatID)
	if err != nil {
		return scylla.Message{}, err
	}
	content := string(body)
	m := scylla.Message{ChatID: chatID, ID: id, Type: scylla.TypeSystem, Content: &content, CreatedAt: ids.Time(id)}
	if err := s.d.Messages.Insert(ctx, m); err != nil {
		return scylla.Message{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := s.d.Store.Chats().TouchActivity(ctx, chatID, m.ID, m.CreatedAt); err != nil {
		s.d.Log.WarnContext(ctx, "chat activity", "error", err, "chat_id", chatID)
	}
	s.d.Notifier.MessageCreated(ctx, m, members, "")
	s.emit(ctx, events.Event{Type: events.MessageCreated, ChatID: chatID, MessageID: m.ID, At: m.CreatedAt})
	return m, nil
}
