// Package events defines the domain events that drive work outside the request: search indexing, chat
// cleanup and later push notifications. They are appended to Redis streams (events:messages, events:chats,
// events:users) after the change is stored, and consumed by the worker (cmd/worker) in consumer groups.
//
// An event is a small, flat record of what happened and to which ids; consumers load whatever else they need
// from the stores. That keeps entries small and makes handlers naturally idempotent.
package events

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/myronsi/messenger-back/internal/store/redis"
)

// Event types.
const (
	MessageCreated    = "message.created"
	MessageEdited     = "message.edited"
	MessageDeleted    = "message.deleted"
	MessageHidden     = "message.hidden" // deleted for one user
	ReactionChanged   = "reaction.changed"
	ChatMemberAdded   = "chat.member_added"
	ChatMemberRemoved = "chat.member_removed"
	ChatDeleted       = "chat.deleted"
	UserUpdated       = "user.updated"
	UserDeleted       = "user.deleted"
)

// Event is one domain event. Zero ids are absent.
type Event struct {
	Type      string
	ChatID    int64
	MessageID int64
	// UserID is whom the event is about: the member added or removed, the user who hid a message, the user
	// who changed or deleted the account.
	UserID int64
	// ActorID is who caused it (sender, editor, the admin who removed a member).
	ActorID int64
	At      time.Time
}

// Stream returns the stream an event type belongs to.
func Stream(eventType string) (string, error) {
	switch eventType {
	case MessageCreated, MessageEdited, MessageDeleted, MessageHidden, ReactionChanged:
		return redis.StreamMessages, nil
	case ChatMemberAdded, ChatMemberRemoved, ChatDeleted:
		return redis.StreamChats, nil
	case UserUpdated, UserDeleted:
		return redis.StreamUsers, nil
	}
	return "", fmt.Errorf("events: unknown type %q", eventType)
}

// Fields encodes the event as stream entry fields.
func (e Event) Fields() map[string]any {
	f := map[string]any{"type": e.Type, "at": e.At.UnixMilli()}
	for k, v := range map[string]int64{"chat_id": e.ChatID, "message_id": e.MessageID, "user_id": e.UserID, "actor_id": e.ActorID} {
		if v != 0 {
			f[k] = v
		}
	}
	return f
}

// ErrMalformed is returned for an entry that is not an event of this package.
var ErrMalformed = errors.New("events: malformed entry")

// Parse decodes stream entry fields.
func Parse(fields map[string]string) (Event, error) {
	e := Event{Type: fields["type"]}
	if _, err := Stream(e.Type); err != nil {
		return Event{}, ErrMalformed
	}
	num := func(k string) (int64, error) {
		v, ok := fields[k]
		if !ok {
			return 0, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, ErrMalformed
		}
		return n, nil
	}
	var err error
	if e.ChatID, err = num("chat_id"); err != nil {
		return Event{}, err
	}
	if e.MessageID, err = num("message_id"); err != nil {
		return Event{}, err
	}
	if e.UserID, err = num("user_id"); err != nil {
		return Event{}, err
	}
	if e.ActorID, err = num("actor_id"); err != nil {
		return Event{}, err
	}
	at, err := num("at")
	if err != nil {
		return Event{}, err
	}
	e.At = time.UnixMilli(at).UTC()
	return e, nil
}

// Appender appends to a stream.
type Appender interface {
	Add(ctx context.Context, stream string, fields map[string]any) (string, error)
}

// Log appends events to their streams.
type Log struct {
	app Appender
	now func() time.Time
}

// NewLog returns the event log.
func NewLog(app Appender) *Log { return &Log{app: app, now: time.Now} }

// Emit appends the event. It sets At when it is zero. Callers emit after the change is stored and must
// not undo the change when Emit fails: the consumers' reconciliation jobs repair what an event lost.
func (l *Log) Emit(ctx context.Context, e Event) error {
	stream, err := Stream(e.Type)
	if err != nil {
		return err
	}
	if e.At.IsZero() {
		e.At = l.now()
	}
	_, err = l.app.Add(ctx, stream, e.Fields())
	return err
}
