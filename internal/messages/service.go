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

	"github.com/myronsi/messenger-back/internal/drawing"
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
	// ErrRateLimited: the sender sent too much; the transport rejects the event before the service sees it.
	ErrRateLimited = errors.New("rate limited")
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
	Increment(ctx context.Context, chatID, messageID int64, userIDs ...int64) error
	Set(ctx context.Context, userID, chatID, readUpTo int64, unread []int64) error
}

// Dedup remembers the message of a client_temp_id (per user and chat).
type Dedup interface {
	Claim(ctx context.Context, userID, chatID int64, clientTempID string, id int64) (existing int64, claimed bool, err error)
	Release(ctx context.Context, userID, chatID int64, clientTempID string, id int64) error
	Fail(ctx context.Context, userID, chatID int64, clientTempID string, id int64) error
	Resume(ctx context.Context, userID, chatID int64, clientTempID string, id int64) (bool, error)
}

// Store is the PostgreSQL side.
type Store interface {
	Chats() postgres.ChatRepository
	Social() postgres.SocialRepository
	Attachments() postgres.AttachmentRepository
	Users() postgres.UserRepository
	Stickers() postgres.StickerRepository
	Unsent() postgres.UnsentRepository
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
	// QueueNotification commits intent before the message write; nil disables background delivery.
	QueueNotification func(context.Context, int64, int64, []int64) error
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
	return s.visible(ctx, viewerID, chatID, messageID, nil)
}

// VisibleAttachment also verifies that the stored message references this attachment.
func (s *Service) VisibleAttachment(ctx context.Context, viewerID, chatID, messageID int64, id uuid.UUID) (bool, error) {
	attachment := gocql.UUID(id)
	return s.visible(ctx, viewerID, chatID, messageID, &attachment)
}

func (s *Service) visible(ctx context.Context, viewerID, chatID, messageID int64, attachment *gocql.UUID) (bool, error) {
	ok, err := s.IsMember(ctx, chatID, viewerID)
	if err != nil || !ok {
		return false, err
	}
	m, err := s.message(ctx, chatID, messageID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil || m.Deleted || (attachment != nil && !slices.Contains(m.AttachmentIDs(), *attachment)) {
		return false, err
	}
	floor, err := s.floor(ctx, chatID, viewerID)
	if err != nil || messageID <= floor {
		return false, err
	}
	hidden, err := s.d.Messages.Hidden(ctx, viewerID, chatID, messageID)
	if err != nil {
		return false, err
	}
	return !hidden, nil
}

func (s *Service) floor(ctx context.Context, chatID, userID int64) (int64, error) {
	bound, err := s.d.Store.Chats().ClearedBound(ctx, chatID, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return 0, nil
	}
	return bound, err
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
	AlbumIDs     []uuid.UUID
	ReplyTo      *int64
	Drawings     map[uuid.UUID][]byte
	StickerID    *int64
}

// Sent is the result of Send. Duplicate means the client_temp_id was used before; Message then carries the id
// and time of the first send (and its content once it is stored).
type Sent struct {
	Message   scylla.Message
	Duplicate bool
}

// MaxAlbumItems bounds attachment lookups and the size of one chat message.
const MaxAlbumItems = 10

func (s *Service) validateSend(r SendRequest) error {
	if n := utf8.RuneCountInString(r.ClientTempID); n == 0 || n > 64 || !utf8.ValidString(r.ClientTempID) {
		return invalid("client_temp_id must be 1 to 64 characters")
	}
	if r.Content != nil && !validText(*r.Content, MaxContent) {
		return invalid("content must be at most %d characters", MaxContent)
	}
	if r.AlbumIDs != nil && (r.Type != scylla.TypeFile || r.AttachmentID != nil || len(r.AlbumIDs) < 2 || len(r.AlbumIDs) > MaxAlbumItems) {
		return invalid("attachment_ids must contain 2 to %d items and is only valid for a file message without attachment_id", MaxAlbumItems)
	}
	if len(r.Drawings) > 0 && r.Type != scylla.TypeFile {
		return invalid("only the images of a file message can have drawings")
	}
	if len(r.Drawings) > MaxAlbumItems {
		return invalid("drawings must contain at most %d items", MaxAlbumItems)
	}
	if r.StickerID != nil && r.Type != scylla.TypeSticker {
		return invalid("only a sticker message has sticker_id")
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
		if r.AttachmentID == nil && len(r.AlbumIDs) == 0 {
			return invalid("a %s message needs attachment_id", r.Type)
		}
	case scylla.TypeSticker:
		if r.StickerID == nil {
			return invalid("a sticker message needs sticker_id")
		}
		if r.Content != nil || r.AttachmentID != nil {
			return invalid("a sticker message has no content and no attachment")
		}
	default:
		return invalid("type must be text, file, voice or sticker")
	}
	return nil
}

// send stores a message and delivers it to the members of the chat.
func (s *Service) send(ctx context.Context, r SendRequest) (Sent, error) {
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
	if r.ReplyTo != nil {
		if _, err := s.message(ctx, r.ChatID, *r.ReplyTo); errors.Is(err, ErrNotFound) {
			return Sent{}, invalid("reply_to is not a message of this chat")
		} else if err != nil {
			return Sent{}, err
		}
	}
	attachments, drawings, err := s.checkAttachments(ctx, r)
	if err != nil {
		return Sent{}, err
	}

	id, err := s.d.IDs.Next()
	if err != nil {
		return Sent{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	prev, claimed, err := s.d.Dedup.Claim(ctx, r.SenderID, r.ChatID, r.ClientTempID, id)
	if err != nil {
		return Sent{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	resumed := false
	if !claimed {
		if resumed, err = s.d.Dedup.Resume(ctx, r.SenderID, r.ChatID, r.ClientTempID, prev); err != nil {
			return Sent{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if !resumed {
			return s.duplicate(ctx, r, prev)
		}
		id = prev
	}
	var sticker *postgres.Sticker
	if r.StickerID != nil {
		found, err := s.sticker(ctx, r.SenderID, *r.StickerID)
		if err != nil {
			s.abandon(ctx, r, id, resumed)
			return Sent{}, err
		}
		sticker = &found
	}

	sender := r.SenderID
	m := scylla.Message{
		ChatID: r.ChatID, ID: id, SenderID: &sender, Type: r.Type, Content: r.Content,
		ReplyTo: r.ReplyTo, CreatedAt: ids.Time(id),
	}
	if len(attachments) > 0 {
		m.AttachmentID = &attachments[0]
	}
	if len(attachments) > 1 {
		m.AlbumIDs = attachments
	}
	if sticker != nil {
		image, emoji := gocql.UUID(sticker.Image.ID), sticker.Emoji
		m.StickerID, m.AttachmentID, m.Content = &sticker.ID, &image, &emoji
	}
	if err := s.linkAttachments(ctx, m, drawings); err != nil {
		s.abandon(ctx, r, id, resumed)
		return Sent{}, err
	}
	if !resumed {
		if err := s.queueNotification(ctx, m, members); err != nil {
			s.abandon(ctx, r, id, false)
			return Sent{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	if err := s.d.Messages.Insert(ctx, m); err != nil {
		s.abandon(ctx, r, id, true)
		return Sent{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	s.delivered(ctx, m, members, r.ClientTempID)
	if sticker != nil {
		s.usedSticker(ctx, r.SenderID, sticker.ID)
	}
	return Sent{Message: m}, nil
}

const RecentStickers = 20

func (s *Service) sticker(ctx context.Context, senderID, id int64) (postgres.Sticker, error) {
	found, err := s.d.Store.Stickers().Stickers(ctx, []int64{id})
	if err != nil {
		return postgres.Sticker{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	st, ok := found[id]
	if !ok || st.Deleted {
		return postgres.Sticker{}, invalid("sticker_id is not an available sticker")
	}
	if st.PackOwnerID != nil && *st.PackOwnerID != senderID {
		seen, err := s.d.Store.Stickers().Installation(ctx, senderID, st.PackID)
		if err != nil {
			return postgres.Sticker{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if seen == nil {
			return postgres.Sticker{}, invalid("sticker_id is not an available sticker")
		}
	}
	return st, nil
}

func (s *Service) usedSticker(ctx context.Context, userID, stickerID int64) {
	if err := s.d.Store.Stickers().UseSticker(context.WithoutCancel(ctx), userID, stickerID, RecentStickers); err != nil {
		s.d.Log.WarnContext(ctx, "recent sticker", "error", err, "sticker_id", stickerID)
	}
}

func (s *Service) abandon(ctx context.Context, r SendRequest, id int64, keep bool) {
	ctx = context.WithoutCancel(ctx)
	var err error
	if keep {
		err = s.d.Dedup.Fail(ctx, r.SenderID, r.ChatID, r.ClientTempID, id)
	} else {
		err = s.d.Dedup.Release(ctx, r.SenderID, r.ChatID, r.ClientTempID, id)
	}
	if err != nil {
		s.d.Log.WarnContext(ctx, "release client_temp_id", "error", err)
	}
}

// delivered finishes a stored new message: chat order, unread counters, delivery and the event. Failures only
// leave derived state stale; the message itself is stored.
func (s *Service) delivered(ctx context.Context, m scylla.Message, members []int64, clientTempID string) {
	s.d.Accepted()
	// The chat moves up in its members' lists; a failure only leaves the order stale until the next message.
	if err := s.d.Store.Chats().TouchActivity(ctx, m.ChatID, m.ID, m.CreatedAt); err != nil {
		s.d.Log.WarnContext(ctx, "chat activity", "error", err, "chat_id", m.ChatID)
	}
	sender := int64(0)
	if m.SenderID != nil {
		sender = *m.SenderID
	}
	if err := s.d.Unread.Increment(ctx, m.ChatID, m.ID, without(members, sender)...); err != nil {
		s.d.Log.WarnContext(ctx, "unread increment", "error", err, "chat_id", m.ChatID)
	}
	s.d.Notifier.MessageCreated(ctx, m, members, clientTempID)
	s.emit(ctx, events.Event{Type: events.MessageCreated, ChatID: m.ChatID, MessageID: m.ID, ActorID: sender, At: m.CreatedAt})
}

// ErrSendInProgress: a send with this client_temp_id is still being stored. The client retries.
var ErrSendInProgress = fmt.Errorf("%w: the first send with this client_temp_id is not stored yet", ErrUnavailable)

// duplicate answers a repeated send with the first message, but only once that message is stored: until then
// the retry fails as unavailable, so a client never gets an ack for a message that does not exist.
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

// checkAttachments validates all uploads before a message can be claimed or linked.
func (s *Service) checkAttachments(ctx context.Context, r SendRequest) ([]gocql.UUID, map[gocql.UUID][]byte, error) {
	ids := r.AlbumIDs
	if r.AttachmentID != nil {
		ids = []uuid.UUID{*r.AttachmentID}
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]gocql.UUID, 0, len(ids))
	drawings := make(map[gocql.UUID][]byte, len(r.Drawings))
	for _, id := range ids {
		if seen[id] {
			return nil, nil, invalid("attachment_ids must be unique")
		}
		seen[id] = true
		a, err := s.d.Store.Attachments().Get(ctx, id)
		if errors.Is(err, postgres.ErrNotFound) {
			return nil, nil, invalid("attachment is not one of your uploads")
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		raw, drawn := r.Drawings[id]
		ok, err := s.mayAttach(ctx, r, a, drawn)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, invalid("attachment is not one of your uploads")
		}
		if r.Type == scylla.TypeVoice && !strings.HasPrefix(a.MimeType, "audio/") {
			return nil, nil, invalid("a voice message needs an audio attachment")
		}
		if len(ids) > 1 && a.Kind != "image" && a.Kind != "video" {
			return nil, nil, invalid("an album can only contain images and videos")
		}
		if drawn {
			if a.Kind != "image" {
				return nil, nil, invalid("only images can have a drawing")
			}
			normalized, err := drawing.Normalize(raw)
			if err != nil {
				return nil, nil, invalid("%s", err.Error())
			}
			drawings[gocql.UUID(id)] = normalized
		}
		out = append(out, gocql.UUID(id))
	}
	for id := range r.Drawings {
		if !seen[id] {
			return nil, nil, invalid("a drawing must belong to an attachment of the message")
		}
	}
	return out, drawings, nil
}

func (s *Service) mayAttach(ctx context.Context, r SendRequest, a postgres.Attachment, drawn bool) (bool, error) {
	if a.UploaderID != nil && *a.UploaderID == r.SenderID && a.Purpose == postgres.PurposeMessage && (a.ChatID == nil || *a.ChatID == r.ChatID) {
		return true, nil
	}
	if !drawn || r.ReplyTo == nil {
		return false, nil
	}
	return s.VisibleAttachment(ctx, r.SenderID, r.ChatID, *r.ReplyTo, a.ID)
}

// Link before storing the message so all items are accessible as soon as recipients see it.
func (s *Service) linkAttachments(ctx context.Context, m scylla.Message, drawings map[gocql.UUID][]byte) error {
	if m.Type == scylla.TypeSticker {
		return nil
	}
	for _, id := range m.AttachmentIDs() {
		if err := s.d.Store.Attachments().Link(ctx, uuid.UUID(id), m.ChatID, m.ID, drawings[id]); err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	return nil
}

func (s *Service) messageDrawings(ctx context.Context, m scylla.Message) (map[gocql.UUID][]byte, error) {
	if len(m.AttachmentIDs()) == 0 {
		return nil, nil
	}
	found, err := s.d.Store.Attachments().Drawings(ctx, m.ChatID, []int64{m.ID})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	out := make(map[gocql.UUID][]byte, len(found))
	for _, d := range found {
		out[gocql.UUID(d.AttachmentID)] = d.Drawing
	}
	return out, nil
}

// Edit replaces the text of the user's own text message.
func (s *Service) Edit(ctx context.Context, userID, chatID, messageID int64, content string) (scylla.Message, error) {
	empty := strings.TrimSpace(content) == ""
	if empty {
		content = ""
	} else if !validText(content, MaxContent) {
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
	// A forwarded copy shows someone else's words: editing it would put new words in their mouth.
	// A file keeps its attachment; only its caption changes.
	if m.SenderID == nil || *m.SenderID != userID || (m.Type != scylla.TypeText && m.Type != scylla.TypeFile) || m.Forwarded != nil {
		return scylla.Message{}, ErrForbidden
	}
	if empty && m.Type != scylla.TypeFile {
		return scylla.Message{}, invalid("content must be 1 to %d characters", MaxContent)
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

// Delete deletes a message for the user only, or for everyone: the sender may, either side of a direct chat
// may, and in a group the owner, admins and moderators may delete any message.
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
	if scope == ScopeEveryone && (m.SenderID == nil || *m.SenderID != userID) {
		if err := s.requireMayDeleteOthers(ctx, chatID, userID); err != nil {
			return err
		}
	}
	if scope == ScopeEveryone && m.Deleted {
		return s.unlink(ctx, m)
	}
	return s.deleteChecked(ctx, userID, m, scope)
}

func (s *Service) DeleteMany(ctx context.Context, userID, chatID int64, messageIDs []int64, scope string) error {
	if scope != ScopeMe && scope != ScopeEveryone {
		return invalid("scope must be me or everyone")
	}
	if err := checkIDs("message_ids", messageIDs, MaxDeleteMessages); err != nil {
		return err
	}
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return err
	}
	othersChecked := false
	done := map[int64]bool{}
	found := make([]scylla.Message, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		m, err := s.message(ctx, chatID, messageID)
		if err != nil {
			return err
		}
		found = append(found, m)
		if scope == ScopeEveryone {
			if err := s.requireNotHidden(ctx, userID, m); err != nil {
				return err
			}
			done[messageID] = m.Deleted
		}
		if scope == ScopeEveryone && !othersChecked && (m.SenderID == nil || *m.SenderID != userID) {
			if err := s.requireMayDeleteOthers(ctx, chatID, userID); err != nil {
				return err
			}
			othersChecked = true
		}
	}
	for _, m := range found {
		if done[m.ID] {
			if err := s.unlink(ctx, m); err != nil {
				return err
			}
			continue
		}
		if err := s.deleteChecked(ctx, userID, m, scope); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) requireNotHidden(ctx context.Context, userID int64, m scylla.Message) error {
	floor, err := s.floor(ctx, m.ChatID, userID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if m.ID <= floor {
		return ErrNotFound
	}
	hidden, err := s.d.Messages.Hidden(ctx, userID, m.ChatID, m.ID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if hidden {
		return ErrNotFound
	}
	return nil
}

func (s *Service) unlink(ctx context.Context, m scylla.Message) error {
	if err := s.d.Store.Attachments().UnlinkMessage(context.WithoutCancel(ctx), m.ChatID, m.ID); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

func (s *Service) deleteChecked(ctx context.Context, userID int64, m scylla.Message, scope string) error {
	chatID, messageID := m.ChatID, m.ID
	if scope == ScopeMe {
		if err := s.d.Messages.Hide(ctx, userID, chatID, messageID, scylla.HiddenDeletedForMe); err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		s.d.Notifier.MessageDeleted(ctx, chatID, messageID, ScopeMe, []int64{userID})
		s.emit(ctx, events.Event{Type: events.MessageHidden, ChatID: chatID, MessageID: messageID, UserID: userID, ActorID: userID})
		return nil
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
	return s.unlink(ctx, m)
}

func (s *Service) requireMayDeleteOthers(ctx context.Context, chatID, userID int64) error {
	p, err := s.d.Store.Chats().Participant(ctx, chatID, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if chat.Type == postgres.ChatDirect {
		return nil
	}
	switch p.Role {
	case postgres.RoleOwner, postgres.RoleAdmin, postgres.RoleModerator:
		return nil
	}
	return ErrForbidden
}

func ValidEmoji(e string) bool {
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
	if !ValidEmoji(emoji) {
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

// Read marks the chat as read up to and including the message and returns how many messages are still
// unread (at most 999). Read positions only move forward. The reader's other devices always learn about it;
// the other members only when read receipts are on.
func (s *Service) Read(ctx context.Context, userID, chatID, messageID int64) (int, error) {
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return 0, err
	}
	if _, err := s.message(ctx, chatID, messageID); err != nil {
		return 0, err
	}
	advanced, err := s.d.Store.Chats().MarkRead(ctx, chatID, userID, messageID)
	if errors.Is(err, postgres.ErrNotFound) {
		return 0, ErrNotFound // removed from the chat after the cached membership check
	}
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !advanced {
		// An older position (a lagging tab): nothing changes and nobody is told; the count is from the
		// position the user already reached.
		p, err := s.d.Store.Chats().Participant(ctx, chatID, userID)
		if err != nil {
			return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		from := messageID
		if p.LastReadMessageID != nil {
			from = *p.LastReadMessageID
		}
		if floor, err := s.floor(ctx, chatID, userID); err == nil {
			from = max(from, floor)
		}
		n, err := s.d.Messages.CountAfter(ctx, chatID, userID, from, unreadCap)
		if err != nil {
			return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return n, nil
	}
	after := messageID
	if floor, err := s.floor(ctx, chatID, userID); err == nil {
		after = max(after, floor)
	}
	unread, err := s.d.Messages.UnreadIDs(ctx, chatID, userID, after, unreadCap)
	n := len(unread)
	countErr := err
	if err != nil {
		// The marker moved but the count is unknown: the caller is told to retry (which only recounts), and the
		// other members still learn about the read below.
		s.d.Log.WarnContext(ctx, "unread recount", "error", err, "chat_id", chatID)
	} else if err := s.d.Unread.Set(ctx, userID, chatID, messageID, unread); err != nil {
		s.d.Log.WarnContext(ctx, "unread set", "error", err, "chat_id", chatID)
	}
	recipients := []int64{userID}
	settings, err := s.d.Store.Social().Privacy(ctx, []int64{userID})
	if err == nil && settings[userID].ReadReceiptsEnabled {
		if members, err := s.Members(ctx, chatID); err == nil {
			recipients = s.receiptRecipients(ctx, userID, chatID, members)
		}
	}
	s.d.Notifier.Read(ctx, chatID, userID, messageID, s.now().UTC(), recipients)
	if countErr != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, countErr)
	}
	return n, nil
}

func (s *Service) receiptRecipients(ctx context.Context, reader, chatID int64, members []int64) []int64 {
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if err != nil {
		return []int64{reader}
	}
	people := append([]int64{reader}, without(members, reader)...)
	blocks, err := s.d.Store.Social().Blocks(ctx, people, people)
	if err != nil {
		return []int64{reader}
	}
	out := []int64{reader}
	for _, m := range people[1:] {
		if blocks[[2]int64{reader, m}] || (chat.Type == postgres.ChatDirect && blocks[[2]int64{m, reader}]) {
			continue
		}
		out = append(out, m)
	}
	return out
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
func (s *Service) Typing(ctx context.Context, userID, chatID int64, typing bool, action string) error {
	if !ValidTypingAction(action) {
		return ErrInvalid
	}
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return err
	}
	if err := s.requireNotBlocked(ctx, userID, chatID); err != nil {
		return nil // dropped silently, like every typing indicator that cannot be delivered
	}
	settings, err := s.d.Store.Social().Privacy(ctx, []int64{userID})
	if err != nil || !settings[userID].TypingIndicatorsEnabled {
		return nil
	}
	members, err := s.Members(ctx, chatID)
	if err != nil {
		return err
	}
	s.d.Notifier.Typing(ctx, chatID, userID, typing, action, without(members, userID))
	return nil
}
