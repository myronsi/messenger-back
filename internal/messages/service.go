// Package messages implements sending, editing, deleting, reacting to and reading messages, with every
// authorization check. It stores through the message repository (ScyllaDB) and tells the realtime layer
// what happened through a Notifier; it knows nothing about WebSockets or JSON.
package messages

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gocql/gocql"
	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

// Limits of the contract.
const (
	MaxContent = 4096 // characters (code points)
	MaxEmoji   = 32
	// unreadCap bounds the recount of unread messages after a read; clients show "999+".
	unreadCap = 999
)

// Errors of the service; the transport maps them to the contract's error codes.
var (
	// ErrNotFound: the chat or message does not exist, or the user is not a member (which is not revealed).
	ErrNotFound = errors.New("not found")
	// ErrForbidden: a member may not do this (someone else's message, missing role).
	ErrForbidden = errors.New("forbidden")
	// ErrBlocked: one user of a direct chat blocked the other.
	ErrBlocked = errors.New("blocked by user")
	// ErrInvalid: the request breaks a rule; the wrapped text says which.
	ErrInvalid = errors.New("invalid request")
	// ErrUnavailable: a store needed for the action is down; the client may retry.
	ErrUnavailable = errors.New("temporarily unavailable")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// IDSource hands out message ids (a Snowflake generator).
type IDSource interface {
	Next() (int64, error)
}

// Members answers who is in a chat, from a cache in front of PostgreSQL.
type Members interface {
	Get(ctx context.Context, chatID int64, load func(context.Context, int64) ([]int64, error)) ([]int64, error)
	IsMember(ctx context.Context, chatID, userID int64, load func(context.Context, int64) ([]int64, error)) (bool, error)
}

// Unread keeps the unread counters.
type Unread interface {
	Increment(ctx context.Context, chatID int64, userIDs ...int64) error
	Set(ctx context.Context, userID, chatID, count int64) error
}

// Dedup remembers the message of a client_temp_id (per user and chat).
type Dedup interface {
	Claim(ctx context.Context, userID, chatID int64, clientTempID string, id int64) (existing int64, claimed bool, err error)
	Release(ctx context.Context, userID, chatID int64, clientTempID string, id int64) error
}

// Store is the PostgreSQL side.
type Store interface {
	Chats() postgres.ChatRepository
	Social() postgres.SocialRepository
	Attachments() postgres.AttachmentRepository
}

// Deps are the collaborators of the service.
type Deps struct {
	Messages scylla.MessageRepository
	Store    Store
	Members  Members
	Unread   Unread
	Dedup    Dedup
	IDs      IDSource
	Notifier Notifier
	// Events records the changes for the worker (search indexing, cleanup). Optional.
	Events EventLog
	// Accepted is called for every new message (the messages-per-second metric). Optional.
	Accepted func()
	Log      *slog.Logger
}

// EventLog appends domain events (events.Log).
type EventLog interface {
	Emit(ctx context.Context, e events.Event) error
}

// Service implements the message use cases.
type Service struct {
	d   Deps
	now func() time.Time
}

// New returns the service.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Notifier == nil {
		d.Notifier = NopNotifier{}
	}
	if d.Accepted == nil {
		d.Accepted = func() {}
	}
	return &Service{d: d, now: time.Now}
}

// emit records an event after the change is stored. A failure does not undo the change; the worker's
// reconciliation repairs what the event would have triggered.
func (s *Service) emit(ctx context.Context, e events.Event) {
	if s.d.Events == nil {
		return
	}
	if err := s.d.Events.Emit(context.WithoutCancel(ctx), e); err != nil {
		s.d.Log.WarnContext(ctx, "emit event", "type", e.Type, "error", err)
	}
}

// loadMembers reads the members of a chat from PostgreSQL, for the cache.
func (s *Service) loadMembers(ctx context.Context, chatID int64) ([]int64, error) {
	ps, err := s.d.Store.Chats().Participants(ctx, chatID)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(ps))
	for i, p := range ps {
		out[i] = p.UserID
	}
	return out, nil
}

// Members returns the members of a chat (cached).
func (s *Service) Members(ctx context.Context, chatID int64) ([]int64, error) {
	m, err := s.d.Members.Get(ctx, chatID, s.loadMembers)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return m, nil
}

// IsMember reports whether the user is in the chat (from the members cache).
func (s *Service) IsMember(ctx context.Context, chatID, userID int64) (bool, error) {
	return s.d.Members.IsMember(ctx, chatID, userID, s.loadMembers)
}

// Visible reports whether the viewer can see the message: a member of its chat, the message exists, is not
// deleted for everyone and not hidden for the viewer.
func (s *Service) Visible(ctx context.Context, viewerID, chatID, messageID int64) (bool, error) {
	ok, err := s.IsMember(ctx, chatID, viewerID)
	if err != nil || !ok {
		return false, err
	}
	m, err := s.message(ctx, chatID, messageID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil || m.Deleted {
		return false, err
	}
	hidden, err := s.d.Messages.Hidden(ctx, viewerID, chatID, messageID)
	if err != nil {
		return false, err
	}
	return !hidden, nil
}

// requireMember fails with ErrNotFound unless the user is in the chat.
func (s *Service) requireMember(ctx context.Context, chatID, userID int64) error {
	ok, err := s.d.Members.IsMember(ctx, chatID, userID, s.loadMembers)
	if err != nil {
		// Fail closed: without an answer the action is refused.
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

func (s *Service) message(ctx context.Context, chatID, messageID int64) (scylla.Message, error) {
	m, err := s.d.Messages.Get(ctx, chatID, messageID)
	if errors.Is(err, scylla.ErrNotFound) {
		return scylla.Message{}, ErrNotFound
	}
	if err != nil {
		return scylla.Message{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return m, nil
}

func validText(s string, maxLen int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= maxLen
}

// SendRequest is a new message from a member.
type SendRequest struct {
	ChatID   int64
	SenderID int64
	// ClientTempID makes the send idempotent per sender (1-64 characters).
	ClientTempID string
	Type         string // text, file or voice
	Content      *string
	AttachmentID *uuid.UUID
	ReplyTo      *int64
}

// Sent is the result of Send. Duplicate means the client_temp_id was used before; Message then carries the id
// and time of the first send (and its content once it is stored).
type Sent struct {
	Message   scylla.Message
	Duplicate bool
}

func (s *Service) validateSend(r SendRequest) error {
	if n := utf8.RuneCountInString(r.ClientTempID); n == 0 || n > 64 || !utf8.ValidString(r.ClientTempID) {
		return invalid("client_temp_id must be 1 to 64 characters")
	}
	if r.Content != nil && !validText(*r.Content, MaxContent) {
		return invalid("content must be at most %d characters", MaxContent)
	}
	switch r.Type {
	case scylla.TypeText:
		if r.Content == nil || strings.TrimSpace(*r.Content) == "" {
			return invalid("a text message needs content")
		}
		if r.AttachmentID != nil {
			return invalid("a text message has no attachment")
		}
	case scylla.TypeFile, scylla.TypeVoice:
		if r.AttachmentID == nil {
			return invalid("a %s message needs attachment_id", r.Type)
		}
	default:
		return invalid("type must be text, file or voice")
	}
	return nil
}

// Send stores a message and delivers it to the members of the chat.
func (s *Service) Send(ctx context.Context, r SendRequest) (Sent, error) {
	if err := s.validateSend(r); err != nil {
		return Sent{}, err
	}
	if err := s.requireMember(ctx, r.ChatID, r.SenderID); err != nil {
		return Sent{}, err
	}
	chat, err := s.d.Store.Chats().Get(ctx, r.ChatID)
	if errors.Is(err, postgres.ErrNotFound) {
		return Sent{}, ErrNotFound
	}
	if err != nil {
		return Sent{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	members, err := s.Members(ctx, r.ChatID)
	if err != nil {
		return Sent{}, err
	}
	if chat.Type == postgres.ChatDirect {
		if err := s.checkNotBlocked(ctx, r.SenderID, members); err != nil {
			return Sent{}, err
		}
	}
	var attachment *gocql.UUID
	if r.AttachmentID != nil {
		a, err := s.checkAttachment(ctx, r)
		if err != nil {
			return Sent{}, err
		}
		attachment = &a
	}
	if r.ReplyTo != nil {
		if _, err := s.message(ctx, r.ChatID, *r.ReplyTo); errors.Is(err, ErrNotFound) {
			return Sent{}, invalid("reply_to is not a message of this chat")
		} else if err != nil {
			return Sent{}, err
		}
	}

	id, err := s.d.IDs.Next()
	if err != nil {
		return Sent{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	prev, claimed, err := s.d.Dedup.Claim(ctx, r.SenderID, r.ChatID, r.ClientTempID, id)
	if err != nil {
		return Sent{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !claimed {
		return s.duplicate(ctx, r, prev)
	}

	sender := r.SenderID
	m := scylla.Message{
		ChatID: r.ChatID, ID: id, SenderID: &sender, Type: r.Type, Content: r.Content,
		AttachmentID: attachment, ReplyTo: r.ReplyTo, CreatedAt: ids.Time(id),
	}
	if attachment != nil {
		// Linked before the message is stored, so recipients can download the file as soon as they see it.
		if err := s.d.Store.Attachments().Link(ctx, uuid.UUID(*attachment), r.ChatID, id); err != nil {
			if rerr := s.d.Dedup.Release(context.WithoutCancel(ctx), r.SenderID, r.ChatID, r.ClientTempID, id); rerr != nil {
				s.d.Log.WarnContext(ctx, "release client_temp_id", "error", rerr)
			}
			return Sent{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	if err := s.d.Messages.Insert(ctx, m); err != nil {
		if rerr := s.d.Dedup.Release(context.WithoutCancel(ctx), r.SenderID, r.ChatID, r.ClientTempID, id); rerr != nil {
			s.d.Log.WarnContext(ctx, "release client_temp_id", "error", rerr)
		}
		return Sent{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	s.d.Accepted()

	others := without(members, r.SenderID)
	if err := s.d.Unread.Increment(ctx, r.ChatID, others...); err != nil {
		s.d.Log.WarnContext(ctx, "unread increment", "error", err, "chat_id", r.ChatID)
	}
	s.d.Notifier.MessageCreated(ctx, m, members, r.ClientTempID)
	s.emit(ctx, events.Event{Type: events.MessageCreated, ChatID: r.ChatID, MessageID: id, ActorID: r.SenderID, At: m.CreatedAt})
	return Sent{Message: m}, nil
}

// ErrSendInProgress: a send with this client_temp_id is still being stored (or failed). The client retries.
var ErrSendInProgress = fmt.Errorf("%w: the first send with this client_temp_id is not stored yet", ErrUnavailable)

// duplicate answers a repeated send with the first message, but only once that message is stored: until then
// (the first send is still running, or failed and will release its claim) the retry fails as unavailable, so
// a client never gets an ack for a message that does not exist.
func (s *Service) duplicate(ctx context.Context, r SendRequest, id int64) (Sent, error) {
	m, err := s.d.Messages.Get(ctx, r.ChatID, id)
	if err != nil || m.SenderID == nil || *m.SenderID != r.SenderID {
		return Sent{}, ErrSendInProgress
	}
	return Sent{Message: m, Duplicate: true}, nil
}

func without(ids []int64, id int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}

// requireNotBlocked fails with ErrBlocked when the chat is a direct chat and one side blocked the other.
func (s *Service) requireNotBlocked(ctx context.Context, userID, chatID int64) error {
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if chat.Type != postgres.ChatDirect {
		return nil
	}
	members, err := s.Members(ctx, chatID)
	if err != nil {
		return err
	}
	return s.checkNotBlocked(ctx, userID, members)
}

func (s *Service) checkNotBlocked(ctx context.Context, sender int64, members []int64) error {
	for _, other := range without(members, sender) {
		blocked, err := s.d.Store.Social().BlockedEither(ctx, sender, other)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if blocked {
			return ErrBlocked
		}
	}
	return nil
}

// checkAttachment allows only a message upload of the sender (a v1 attachment only in its own chat); a voice
// message needs a voice or audio upload.
func (s *Service) checkAttachment(ctx context.Context, r SendRequest) (gocql.UUID, error) {
	a, err := s.d.Store.Attachments().Get(ctx, *r.AttachmentID)
	if errors.Is(err, postgres.ErrNotFound) {
		return gocql.UUID{}, invalid("attachment_id is not one of your uploads")
	}
	if err != nil {
		return gocql.UUID{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if a.UploaderID == nil || *a.UploaderID != r.SenderID || a.Purpose != postgres.PurposeMessage || (a.ChatID != nil && *a.ChatID != r.ChatID) {
		return gocql.UUID{}, invalid("attachment_id is not one of your uploads")
	}
	if r.Type == scylla.TypeVoice && !strings.HasPrefix(a.MimeType, "audio/") {
		return gocql.UUID{}, invalid("a voice message needs an audio attachment")
	}
	return gocql.UUID(a.ID), nil
}

// Edit replaces the text of the user's own text message.
func (s *Service) Edit(ctx context.Context, userID, chatID, messageID int64, content string) (scylla.Message, error) {
	if strings.TrimSpace(content) == "" || !validText(content, MaxContent) {
		return scylla.Message{}, invalid("content must be 1 to %d characters", MaxContent)
	}
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return scylla.Message{}, err
	}
	m, err := s.message(ctx, chatID, messageID)
	if err != nil {
		return scylla.Message{}, err
	}
	if m.Deleted {
		return scylla.Message{}, ErrNotFound
	}
	if m.SenderID == nil || *m.SenderID != userID || m.Type != scylla.TypeText {
		return scylla.Message{}, ErrForbidden
	}
	if err := s.requireNotBlocked(ctx, userID, chatID); err != nil {
		return scylla.Message{}, err
	}
	at := s.now().UTC().Truncate(time.Millisecond)
	if err := s.d.Messages.Edit(ctx, chatID, messageID, content, at); errors.Is(err, scylla.ErrNotFound) {
		return scylla.Message{}, ErrNotFound
	} else if err != nil {
		return scylla.Message{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	m.Content, m.EditedAt = &content, &at
	if members, err := s.Members(ctx, chatID); err == nil {
		// Members who deleted the message for themselves do not get its new text.
		s.d.Notifier.MessageEdited(ctx, m, s.withoutHidden(ctx, chatID, messageID, members))
	}
	s.emit(ctx, events.Event{Type: events.MessageEdited, ChatID: chatID, MessageID: messageID, ActorID: userID, At: at})
	return m, nil
}

// withoutHidden leaves out the members for whom the message is hidden.
func (s *Service) withoutHidden(ctx context.Context, chatID, messageID int64, members []int64) []int64 {
	hidden, err := s.d.Messages.HiddenAmong(ctx, chatID, messageID, members)
	if err != nil {
		s.d.Log.WarnContext(ctx, "hidden check", "error", err)
		return nil // fail closed: nobody gets an event that might reveal a hidden message
	}
	out := make([]int64, 0, len(members))
	for _, m := range members {
		if !hidden[m] {
			out = append(out, m)
		}
	}
	return out
}

// Delete scopes.
const (
	ScopeMe       = "me"
	ScopeEveryone = "everyone"
)

// Delete deletes a message for the user only, or for everyone: the sender may, and in a group the owner,
// admins and moderators may delete any message.
func (s *Service) Delete(ctx context.Context, userID, chatID, messageID int64, scope string) error {
	if scope != ScopeMe && scope != ScopeEveryone {
		return invalid("scope must be me or everyone")
	}
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return err
	}
	m, err := s.message(ctx, chatID, messageID)
	if err != nil {
		return err
	}
	if scope == ScopeMe {
		if err := s.d.Messages.Hide(ctx, userID, chatID, messageID, scylla.HiddenDeletedForMe); err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		s.d.Notifier.MessageDeleted(ctx, chatID, messageID, ScopeMe, []int64{userID})
		s.emit(ctx, events.Event{Type: events.MessageHidden, ChatID: chatID, MessageID: messageID, UserID: userID, ActorID: userID})
		return nil
	}
	if m.SenderID == nil || *m.SenderID != userID {
		if err := s.requireModerator(ctx, chatID, userID); err != nil {
			return err
		}
	}
	if err := s.d.Messages.Delete(ctx, chatID, messageID); errors.Is(err, scylla.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if members, err := s.Members(ctx, chatID); err == nil {
		s.d.Notifier.MessageDeleted(ctx, chatID, messageID, ScopeEveryone, members)
	}
	s.emit(ctx, events.Event{Type: events.MessageDeleted, ChatID: chatID, MessageID: messageID, ActorID: userID})
	return nil
}

func (s *Service) requireModerator(ctx context.Context, chatID, userID int64) error {
	p, err := s.d.Store.Chats().Participant(ctx, chatID, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	switch p.Role {
	case postgres.RoleOwner, postgres.RoleAdmin, postgres.RoleModerator:
		chat, err := s.d.Store.Chats().Get(ctx, chatID)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if chat.Type == postgres.ChatGroup {
			return nil
		}
	}
	return ErrForbidden
}

func validEmoji(e string) bool {
	if e == "" || !utf8.ValidString(e) || utf8.RuneCountInString(e) > MaxEmoji {
		return false
	}
	for _, r := range e {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// React adds or removes the user's reaction to a message.
func (s *Service) React(ctx context.Context, userID, chatID, messageID int64, emoji string, add bool) error {
	if !validEmoji(emoji) {
		return invalid("emoji must be 1 to %d characters without spaces", MaxEmoji)
	}
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return err
	}
	m, err := s.message(ctx, chatID, messageID)
	if err != nil {
		return err
	}
	if m.Deleted {
		return ErrNotFound
	}
	if err := s.requireNotBlocked(ctx, userID, chatID); err != nil {
		return err
	}
	at := s.now().UTC()
	if add {
		err = s.d.Messages.AddReaction(ctx, chatID, messageID, userID, emoji, at)
	} else {
		err = s.d.Messages.RemoveReaction(ctx, chatID, messageID, userID, emoji)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if members, err := s.Members(ctx, chatID); err == nil {
		s.d.Notifier.ReactionChanged(ctx, chatID, messageID, scylla.Reaction{Emoji: emoji, UserID: userID, CreatedAt: at}, add, members)
	}
	s.emit(ctx, events.Event{Type: events.ReactionChanged, ChatID: chatID, MessageID: messageID, ActorID: userID, At: at})
	return nil
}

// Read marks the chat as read up to and including the message. Read positions only move forward. The
// reader's other devices always learn about it; the other members only when read receipts are on.
func (s *Service) Read(ctx context.Context, userID, chatID, messageID int64) error {
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return err
	}
	if _, err := s.message(ctx, chatID, messageID); err != nil {
		return err
	}
	advanced, err := s.d.Store.Chats().MarkRead(ctx, chatID, userID, messageID)
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrNotFound // removed from the chat after the cached membership check
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !advanced {
		return nil // an older position (a lagging tab): nothing changes, nobody is told
	}
	if n, err := s.d.Messages.CountAfter(ctx, chatID, userID, messageID, unreadCap); err != nil {
		s.d.Log.WarnContext(ctx, "unread recount", "error", err, "chat_id", chatID)
	} else if err := s.d.Unread.Set(ctx, userID, chatID, int64(n)); err != nil {
		s.d.Log.WarnContext(ctx, "unread set", "error", err, "chat_id", chatID)
	}
	recipients := []int64{userID}
	settings, err := s.d.Store.Social().Privacy(ctx, []int64{userID})
	if err == nil && settings[userID].ReadReceiptsEnabled {
		if members, err := s.Members(ctx, chatID); err == nil {
			recipients = members
		}
	}
	s.d.Notifier.Read(ctx, chatID, userID, messageID, s.now().UTC(), recipients)
	return nil
}

// Resend delivers one of the user's stored messages again, to members it was not delivered to as well.
func (s *Service) Resend(ctx context.Context, userID, chatID, messageID int64) error {
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return err
	}
	m, err := s.message(ctx, chatID, messageID)
	if err != nil {
		return err
	}
	if m.SenderID == nil || *m.SenderID != userID {
		return ErrForbidden
	}
	if m.Deleted {
		return ErrNotFound
	}
	if err := s.requireNotBlocked(ctx, userID, chatID); err != nil {
		return err
	}
	// Delivered again to the sender's devices and to the members it was not delivered to; members who deleted
	// it for themselves keep it deleted.
	unhidden, err := s.d.Messages.UnhideAll(ctx, chatID, messageID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	members, err := s.Members(ctx, chatID)
	if err != nil {
		return err
	}
	recipients := []int64{userID}
	for _, uid := range unhidden {
		if uid != userID && slices.Contains(members, uid) {
			recipients = append(recipients, uid)
		}
	}
	s.d.Notifier.MessageCreated(ctx, m, recipients, "")
	return nil
}

// Typing tells the other members that the user is (not) typing.
func (s *Service) Typing(ctx context.Context, userID, chatID int64, typing bool) error {
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return err
	}
	if err := s.requireNotBlocked(ctx, userID, chatID); err != nil {
		return nil // dropped silently, like every typing indicator that cannot be delivered
	}
	members, err := s.Members(ctx, chatID)
	if err != nil {
		return err
	}
	s.d.Notifier.Typing(ctx, chatID, userID, typing, without(members, userID))
	return nil
}
