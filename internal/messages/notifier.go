package messages

import (
	"context"
	"time"

	"github.com/myronsi/messenger-back/internal/store/scylla"
)

// Notifier delivers what happened to the users it concerns. Implementations render the events for every
// recipient and must not fail the action: the change is stored, delivery is best effort, and clients catch
// up through the history after a reconnect.
type Notifier interface {
	// MessageCreated: a new (or resent) message; clientTempID is set for the sender's own copy.
	MessageCreated(ctx context.Context, m scylla.Message, recipients []int64, clientTempID string)
	MessageEdited(ctx context.Context, m scylla.Message, recipients []int64)
	// MessageDeleted for everyone (recipients: the members) or for the user only (recipients: the user).
	MessageDeleted(ctx context.Context, chatID, messageID int64, scope string, recipients []int64)
	ReactionChanged(ctx context.Context, chatID, messageID int64, r scylla.Reaction, added bool, recipients []int64)
	Read(ctx context.Context, chatID, userID, messageID int64, at time.Time, recipients []int64)
	Typing(ctx context.Context, chatID, userID int64, typing bool, recipients []int64)
}

// NopNotifier delivers nothing.
type NopNotifier struct{}

var _ Notifier = NopNotifier{}

func (NopNotifier) MessageCreated(context.Context, scylla.Message, []int64, string)               {}
func (NopNotifier) MessageEdited(context.Context, scylla.Message, []int64)                        {}
func (NopNotifier) MessageDeleted(context.Context, int64, int64, string, []int64)                 {}
func (NopNotifier) ReactionChanged(context.Context, int64, int64, scylla.Reaction, bool, []int64) {}
func (NopNotifier) Read(context.Context, int64, int64, int64, time.Time, []int64)                 {}
func (NopNotifier) Typing(context.Context, int64, int64, bool, []int64)                           {}
