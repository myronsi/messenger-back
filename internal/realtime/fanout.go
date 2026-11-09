package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/calls"
	"github.com/myronsi/messenger-back/internal/chats"
	"github.com/myronsi/messenger-back/internal/httpapi"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

// Frame kinds on the bus. A frame wraps the client-facing event with what the receiving gateway needs to
// decide whether to forward it.
const (
	kindEvent    = "event"
	kindRemoved  = "removed"  // the user left or was removed from the chat: stop delivering it
	kindPresence = "presence" // forwarded only when newer than the last change of that user
)

// frame is what travels on user:{id}. Event is the exact JSON sent to the client.
type frame struct {
	Kind    string          `json:"k"`
	ChatID  int64           `json:"c,omitempty"`
	Subject int64           `json:"s,omitempty"`
	Version int64           `json:"v,omitempty"`
	Session *uuid.UUID      `json:"x,omitempty"`
	Event   json.RawMessage `json:"e"`
}

// control is what travels on the broadcast channel.
type control struct {
	CloseSessions []uuid.UUID     `json:"close_sessions,omitempty"`
	Notice        json.RawMessage `json:"notice,omitempty"`
}

// Publisher is the bus as the fan-out uses it.
type Publisher interface {
	PublishEach(ctx context.Context, payloads map[int64][]byte) error
	Broadcast(ctx context.Context, payload []byte) error
}

// Store is the PostgreSQL side the fan-out reads.
type Store interface {
	Attachments() postgres.AttachmentRepository
	Social() postgres.SocialRepository
	Users() postgres.UserRepository
}

// Fanout renders events for every recipient and publishes them. It implements messages.Notifier and is
// how presence changes and membership removals reach clients.
type Fanout struct {
	bus      Publisher
	dir      *users.Directory
	store    Store
	members  func(ctx context.Context, chatID int64) ([]int64, error)
	reacts   func(ctx context.Context, chatID int64, ids []int64) (map[int64][]scylla.Reaction, error)
	replies  ReplyLookup
	ids      messages.IDSource
	basePath string
	log      *slog.Logger
}

var _ messages.Notifier = (*Fanout)(nil)

type ReplyLookup interface {
	Get(ctx context.Context, chatID, messageID int64) (scylla.Message, error)
	HiddenAmong(ctx context.Context, chatID, messageID int64, userIDs []int64) (map[int64]bool, error)
}

// FanoutDeps are the collaborators of the fan-out.
type FanoutDeps struct {
	Bus       Publisher
	Directory *users.Directory
	Store     Store
	// Members returns the members of a chat (cached).
	Members func(ctx context.Context, chatID int64) ([]int64, error)
	// Reactions loads the reactions of messages (for edit events).
	Reactions func(ctx context.Context, chatID int64, ids []int64) (map[int64][]scylla.Reaction, error)
	Replies   ReplyLookup
	// IDs numbers the events (event_id).
	IDs      messages.IDSource
	BasePath string
	Log      *slog.Logger
}

// NewFanout returns the fan-out.
func NewFanout(d FanoutDeps) *Fanout {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Fanout{bus: d.Bus, dir: d.Directory, store: d.Store, members: d.Members, reacts: d.Reactions, replies: d.Replies, ids: d.IDs, basePath: d.BasePath, log: d.Log}
}

// eventID numbers an event; without an id source (Redis lost the node lease) the event still goes out.
func (f *Fanout) eventID() string {
	id, err := f.ids.Next()
	if err != nil {
		return "0"
	}
	return idString(id)
}

func (f *Fanout) encode(typ string, chatID int64, data any, clientTempID string) (json.RawMessage, error) {
	ev := serverEvent{Type: typ, EventID: f.eventID(), ChatID: chatRef(chatID), Data: data}
	if clientTempID != "" {
		ev.ClientTempID = &clientTempID
	}
	return json.Marshal(ev)
}

func (f *Fanout) publish(ctx context.Context, frames map[int64]frame) {
	payloads := make(map[int64][]byte, len(frames))
	for uid, fr := range frames {
		b, err := json.Marshal(fr)
		if err != nil {
			f.log.ErrorContext(ctx, "encode frame", "error", err)
			return
		}
		payloads[uid] = b
	}
	// Delivery outlives the request that caused it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := f.bus.PublishEach(ctx, payloads); err != nil {
		f.log.WarnContext(ctx, "publish events", "error", err, "recipients", len(payloads))
	}
}

// same sends one event to all recipients.
func (f *Fanout) same(ctx context.Context, typ string, chatID int64, data any, recipients []int64) {
	frames := make(map[int64]frame, len(recipients))
	for _, uid := range recipients {
		raw, err := f.encode(typ, chatID, data, "")
		if err != nil {
			f.log.ErrorContext(ctx, "encode event", "error", err, "type", typ)
			return
		}
		frames[uid] = frame{Kind: kindEvent, ChatID: chatID, Event: raw}
	}
	f.publish(ctx, frames)
}

// renderMessage renders m for every recipient: the sender as each of them may see them, and the attachment.
func (f *Fanout) renderMessage(ctx context.Context, m scylla.Message, recipients []int64, reactions []scylla.Reaction, clientTempID string) map[int64]httpapi.Message {
	var senders map[int64]users.View
	if m.SenderID != nil {
		var err error
		if senders, err = f.dir.ToViewers(ctx, *m.SenderID, recipients, false); err != nil {
			f.log.WarnContext(ctx, "render sender", "error", err)
			senders = nil
		}
	}
	var att *postgres.Attachment
	var album []postgres.Attachment
	for _, id := range m.AttachmentIDs() {
		a, err := f.store.Attachments().Get(ctx, uuid.UUID(id))
		if err == nil {
			if len(m.AlbumIDs) > 0 {
				album = append(album, a)
			} else {
				att = &a
			}
		} else if !errors.Is(err, postgres.ErrNotFound) {
			f.log.WarnContext(ctx, "render attachment", "error", err)
		}
	}
	drawings := f.drawings(ctx, m)
	replies := f.replyOrigins(ctx, m, recipients)
	out := make(map[int64]httpapi.Message, len(recipients))
	for _, uid := range recipients {
		parts := httpapi.MessageParts{Attachment: att, Album: album, Reactions: reactions, BasePath: f.basePath, Reply: replies[uid], Drawings: drawings}
		if m.SenderID != nil {
			v, ok := senders[uid]
			if !ok {
				// Without the directory the sender is at least identified.
				v = users.View{ID: *m.SenderID, Username: "unknown", DisplayName: "…"}
			}
			parts.Sender = &v
			if uid == *m.SenderID {
				parts.ClientTempID = clientTempID
			}
		}
		out[uid] = httpapi.PresentMessage(m, parts)
	}
	return out
}

func (f *Fanout) drawings(ctx context.Context, m scylla.Message) map[uuid.UUID][]byte {
	if len(m.AttachmentIDs()) == 0 {
		return nil
	}
	found, err := f.store.Attachments().Drawings(ctx, m.ChatID, []int64{m.ID})
	if err != nil {
		f.log.WarnContext(ctx, "render drawings", "error", err)
		return nil
	}
	return httpapi.GroupDrawings(found)[m.ID]
}

func (f *Fanout) replyOrigins(ctx context.Context, m scylla.Message, recipients []int64) map[int64]*httpapi.ReplyOrigin {
	if f.replies == nil || m.ReplyTo == nil || m.Deleted {
		return nil
	}
	o, err := f.replies.Get(ctx, m.ChatID, *m.ReplyTo)
	if err != nil {
		if !errors.Is(err, scylla.ErrNotFound) {
			f.log.WarnContext(ctx, "render reply", "error", err)
		}
		return nil
	}
	hidden, err := f.replies.HiddenAmong(ctx, m.ChatID, o.ID, recipients)
	if err != nil {
		f.log.WarnContext(ctx, "render reply", "error", err)
		return nil
	}
	var senders map[int64]users.View
	if o.SenderID != nil {
		if senders, err = f.dir.ToViewers(ctx, *o.SenderID, recipients, false); err != nil {
			f.log.WarnContext(ctx, "render reply sender", "error", err)
		}
	}
	var att *postgres.Attachment
	var drawing []byte
	if o.AttachmentID != nil && !o.Deleted {
		if a, err := f.store.Attachments().Get(ctx, uuid.UUID(*o.AttachmentID)); err == nil {
			att = &a
			drawing = f.drawings(ctx, o)[a.ID]
		}
	}
	out := make(map[int64]*httpapi.ReplyOrigin, len(recipients))
	for _, uid := range recipients {
		if hidden[uid] {
			continue
		}
		origin := &httpapi.ReplyOrigin{Message: o, Attachment: att, Drawing: drawing}
		if v, ok := senders[uid]; ok {
			origin.Sender = &v
		}
		out[uid] = origin
	}
	return out
}

func (f *Fanout) messageEvent(ctx context.Context, typ string, m scylla.Message, recipients []int64, reactions []scylla.Reaction, clientTempID string) {
	rendered := f.renderMessage(ctx, m, recipients, reactions, clientTempID)
	frames := make(map[int64]frame, len(rendered))
	for uid, msg := range rendered {
		ctid := ""
		if msg.ClientTempId != nil {
			ctid = *msg.ClientTempId
		}
		raw, err := f.encode(typ, m.ChatID, map[string]any{"message": msg}, ctid)
		if err != nil {
			f.log.ErrorContext(ctx, "encode event", "error", err, "type", typ)
			return
		}
		frames[uid] = frame{Kind: kindEvent, ChatID: m.ChatID, Event: raw}
	}
	f.publish(ctx, frames)
}

// MessageCreated implements messages.Notifier.
func (f *Fanout) MessageCreated(ctx context.Context, m scylla.Message, recipients []int64, clientTempID string) {
	f.messageEvent(ctx, "message", m, recipients, nil, clientTempID)
}

// MessageEdited implements messages.Notifier.
func (f *Fanout) MessageEdited(ctx context.Context, m scylla.Message, recipients []int64) {
	var reactions []scylla.Reaction
	if f.reacts != nil {
		if rx, err := f.reacts(ctx, m.ChatID, []int64{m.ID}); err == nil {
			reactions = rx[m.ID]
		}
	}
	f.messageEvent(ctx, "edit", m, recipients, reactions, "")
}

// MessageDeleted implements messages.Notifier.
func (f *Fanout) MessageDeleted(ctx context.Context, chatID, messageID int64, scope string, recipients []int64) {
	f.same(ctx, "delete", chatID, map[string]any{"message_id": idString(messageID), "scope": scope}, recipients)
}

// ReactionChanged implements messages.Notifier.
func (f *Fanout) ReactionChanged(ctx context.Context, chatID, messageID int64, r scylla.Reaction, added bool, recipients []int64) {
	typ := "reaction_remove"
	if added {
		typ = "reaction_add"
	}
	f.same(ctx, typ, chatID, map[string]any{"message_id": idString(messageID), "reaction": httpapi.PresentReaction(r)}, recipients)
}

// Read implements messages.Notifier.
func (f *Fanout) Read(ctx context.Context, chatID, userID, messageID int64, at time.Time, recipients []int64) {
	f.same(ctx, "read", chatID, map[string]any{"message_id": idString(messageID), "user_id": idString(userID), "read_at": at.UTC()}, recipients)
}

// Typing implements messages.Notifier.
func (f *Fanout) Typing(ctx context.Context, chatID, userID int64, typing bool, recipients []int64) {
	f.same(ctx, "typing", chatID, map[string]any{"user_id": idString(userID), "is_typing": typing}, recipients)
}

// Removed tells users that they are no longer in the chat (it was deleted, they left or were removed).
// Their gateways stop delivering the chat's events from this moment, even ones published by a send that
// loaded the members just before the change.
func (f *Fanout) Removed(ctx context.Context, chatID int64, userIDs []int64) {
	frames := make(map[int64]frame, len(userIDs))
	for _, uid := range userIDs {
		raw, err := f.encode("chat_deleted", chatID, struct{}{}, "")
		if err != nil {
			return
		}
		frames[uid] = frame{Kind: kindRemoved, ChatID: chatID, Event: raw}
	}
	f.publish(ctx, frames)
}

// CloseSessions makes every instance close the sockets of the sessions (they were revoked).
func (f *Fanout) CloseSessions(ctx context.Context, sessions []uuid.UUID) {
	f.closeSessions(ctx, sessions, nil)
}

// NotifyUser sends account events only to the owning user's authenticated tabs.
func (f *Fanout) NotifyUser(ctx context.Context, uid int64, kind string, data any) {
	f.same(ctx, kind, 0, data, []int64{uid})
}

// CloseSessionsWithNotice sends the reset notice and revocation in one ordered
// control message, so a gateway can flush the notice before closing each socket.
func (f *Fanout) CloseSessionsWithNotice(ctx context.Context, sessions []uuid.UUID) {
	notice, err := f.encode("security_notice", 0, map[string]string{"type": "password_reset"}, "")
	if err != nil {
		return
	}
	f.closeSessions(ctx, sessions, notice)
}

func (f *Fanout) closeSessions(ctx context.Context, sessions []uuid.UUID, notice json.RawMessage) {
	if len(sessions) == 0 {
		return
	}
	b, err := json.Marshal(control{CloseSessions: sessions, Notice: notice})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := f.bus.Broadcast(ctx, b); err != nil {
		f.log.WarnContext(ctx, "broadcast session revocation", "error", err)
	}
}

// partners returns the users who share a chat with the user (who may see their presence).
func (f *Fanout) partners(ctx context.Context, userID int64) ([]int64, error) {
	chats, err := f.store.Social().ChatIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{userID: true}
	var out []int64
	for _, c := range chats {
		ms, err := f.members(ctx, c)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// Presence announces a presence change to the users who share a chat with the user and may see it. An
// offline change carries the time the user was last seen.
func (f *Fanout) Presence(ctx context.Context, c redis.Change, lastSeen time.Time) {
	watchers, err := f.partners(ctx, c.UserID)
	if err != nil {
		f.log.WarnContext(ctx, "presence watchers", "error", err)
		return
	}
	visible, err := f.dir.PresenceVisibleTo(ctx, c.UserID, watchers, false)
	if err != nil {
		f.log.WarnContext(ctx, "presence visibility", "error", err)
		return
	}
	data := map[string]any{"user_id": idString(c.UserID), "is_online": c.Online, "last_seen": nil}
	if !c.Online {
		data["last_seen"] = lastSeen.UTC()
	}
	frames := make(map[int64]frame, len(watchers))
	for _, w := range watchers {
		if !visible[w] {
			continue
		}
		raw, err := f.encode("presence", 0, data, "")
		if err != nil {
			return
		}
		frames[w] = frame{Kind: kindPresence, Subject: c.UserID, Version: c.Version, Event: raw}
	}
	f.publish(ctx, frames)
}

var _ chats.Notifier = (*Fanout)(nil)

func (f *Fanout) chatEvent(ctx context.Context, typ string, chatID int64, views map[int64]chats.View) {
	frames := make(map[int64]frame, len(views))
	for uid, v := range views {
		raw, err := f.encode(typ, chatID, map[string]any{"chat": httpapi.PresentChat(v, f.basePath)}, "")
		if err != nil {
			f.log.ErrorContext(ctx, "encode event", "error", err, "type", typ)
			return
		}
		frames[uid] = frame{Kind: kindEvent, ChatID: chatID, Event: raw}
	}
	f.publish(ctx, frames)
}

// ChatCreated implements chats.Notifier: the chat appears in the users' lists, and their gateways deliver
// its events again if they had been removed from it before.
func (f *Fanout) ChatCreated(ctx context.Context, chatID int64, views map[int64]chats.View) {
	f.chatEvent(ctx, "chat_created", chatID, views)
}

// ChatUpdated implements chats.Notifier (chat_list_update).
func (f *Fanout) ChatUpdated(ctx context.Context, chatID int64, views map[int64]chats.View) {
	f.chatEvent(ctx, "chat_list_update", chatID, views)
}

// ChatRemoved implements chats.Notifier (chat_deleted).
func (f *Fanout) ChatRemoved(ctx context.Context, chatID int64, userIDs []int64) {
	f.Removed(ctx, chatID, userIDs)
}

// RequestCreated implements chats.Notifier: the request appears in the recipient's inbox.
func (f *Fanout) RequestCreated(ctx context.Context, r chats.RequestView) {
	raw, err := f.encode("approval_request_created", 0, map[string]any{"request": httpapi.PresentRequest(r)}, "")
	if err != nil {
		f.log.ErrorContext(ctx, "encode event", "error", err)
		return
	}
	f.publish(ctx, map[int64]frame{r.Request.RecipientID: {Kind: kindEvent, Event: raw}})
}

var _ chats.GroupNotifier = (*Fanout)(nil)

func (f *Fanout) groupEvent(ctx context.Context, typ string, chatID int64, views map[int64]chats.GroupView) {
	frames := make(map[int64]frame, len(views))
	for uid, v := range views {
		raw, err := f.encode(typ, chatID, map[string]any{"group": httpapi.PresentGroup(v, f.basePath)}, "")
		if err != nil {
			f.log.ErrorContext(ctx, "encode event", "error", err, "type", typ)
			return
		}
		frames[uid] = frame{Kind: kindEvent, ChatID: chatID, Event: raw}
	}
	f.publish(ctx, frames)
}

// GroupCreated implements chats.GroupNotifier: the users are in the group now, and their gateways deliver its
// events again if they had been removed from it before.
func (f *Fanout) GroupCreated(ctx context.Context, chatID int64, views map[int64]chats.GroupView) {
	f.groupEvent(ctx, "group_created", chatID, views)
}

// GroupUpdated implements chats.GroupNotifier.
func (f *Fanout) GroupUpdated(ctx context.Context, chatID int64, views map[int64]chats.GroupView) {
	f.groupEvent(ctx, "group_updated", chatID, views)
}

var _ calls.Notifier = (*Fanout)(nil)

func (f *Fanout) CallUpdated(ctx context.Context, c postgres.Call, recipients []int64) {
	f.same(ctx, "call_updated", c.ChatID, map[string]any{"call": httpapi.PresentCall(c)}, recipients)
}

func (f *Fanout) CallKeys(ctx context.Context, c postgres.Call, recipient int64, session uuid.UUID, envelopes []postgres.CallKeyEnvelope) {
	raw, err := f.encode("call_keys", c.ChatID, httpapi.PresentCallKeys(c, envelopes), "")
	if err != nil {
		f.log.ErrorContext(ctx, "encode event", "error", err, "type", "call_keys")
		return
	}
	f.publish(ctx, map[int64]frame{recipient: {Kind: kindEvent, ChatID: c.ChatID, Session: &session, Event: raw}})
}
