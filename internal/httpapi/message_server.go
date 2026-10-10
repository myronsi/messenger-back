package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/search"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

// Message rates. Sending shares its bucket with the WebSocket, so a client cannot double its rate by using
// both.
var (
	RateSend    = redis.Rate{Name: "ws_send", Rate: 30, Period: 10 * time.Second, Burst: 20}
	RateForward = redis.Rate{Name: "forward", Rate: 30, Period: time.Minute, Burst: 10}
)

// MessageStore is the PostgreSQL side the message endpoints read.
type MessageStore interface {
	Attachments() postgres.AttachmentRepository
}

// MessageOptions configures the message endpoints.
type MessageOptions struct {
	Service   *messages.Service
	Store     MessageStore
	Directory *users.Directory
	// Searcher answers message searches. Optional (searches then answer 501).
	Searcher *search.Searcher
	Limiter  RateAllower
	BasePath string
	Log      *slog.Logger
}

// MessageServer implements history, sending, editing, deleting and forwarding over HTTP, and the media lists.
type MessageServer struct{ o MessageOptions }

// NewMessageServer returns the endpoints.
func NewMessageServer(o MessageOptions) *MessageServer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &MessageServer{o: o}
}

// render renders messages of one chat for one viewer: senders as the viewer sees them, files, reactions and
// read receipts. clientTempID goes on the viewer's own copy of a message just sent.
func (m *MessageServer) render(ctx context.Context, viewer, chatID int64, msgs []scylla.Message, clientTempID string) ([]Message, error) {
	return m.renderWith(ctx, viewer, chatID, msgs, clientTempID, nil)
}

// renderWith is render with files that are known already (the media lists read them with the links).
func (m *MessageServer) renderWith(ctx context.Context, viewer, chatID int64, msgs []scylla.Message, clientTempID string, known map[uuid.UUID]postgres.Attachment) ([]Message, error) {
	deco, err := m.o.Service.Decorate(ctx, viewer, chatID, msgs)
	if err != nil {
		return nil, err
	}
	var senders []int64
	files := map[uuid.UUID]*postgres.Attachment{}
	for _, msg := range msgs {
		if msg.SenderID != nil {
			senders = append(senders, *msg.SenderID)
		}
		if msg.AttachmentID != nil && !msg.Deleted {
			id := uuid.UUID(*msg.AttachmentID)
			if a, ok := known[id]; ok {
				files[id] = &a
			} else {
				files[id] = nil
			}
		}
	}
	people, err := m.o.Directory.ForViewer(ctx, viewer, uniq(senders))
	if err != nil {
		return nil, err
	}
	for id, have := range files {
		if have != nil {
			continue
		}
		a, err := m.o.Store.Attachments().Get(ctx, id)
		if err == nil {
			files[id] = &a
		} else if !errors.Is(err, postgres.ErrNotFound) {
			return nil, err
		}
	}
	out := make([]Message, len(msgs))
	for i, msg := range msgs {
		parts := MessageParts{Reactions: deco.Reactions[msg.ID], BasePath: m.o.BasePath}
		if msg.SenderID != nil {
			v := people[*msg.SenderID]
			parts.Sender = &v
			if *msg.SenderID == viewer {
				parts.ClientTempID = clientTempID
			}
		}
		if msg.AttachmentID != nil {
			parts.Attachment = files[uuid.UUID(*msg.AttachmentID)]
		}
		for _, r := range deco.ReadBy(msg) {
			parts.ReadBy = append(parts.ReadBy, ReadReceipt{UserId: FormatID(r.UserID), ReadAt: utc(*r.At)})
		}
		out[i] = PresentMessage(msg, parts)
	}
	return out, nil
}

func uniq(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func optionalID(w http.ResponseWriter, field string, s *Id) (int64, bool) {
	if s == nil {
		return 0, true
	}
	return parseID(w, field, *s)
}

// ListMessages implements GET /chats/{id}/messages.
func (m *MessageServer) ListMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, params ListMessagesParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	before, ok1 := optionalID(w, "before", params.Before)
	if !ok1 {
		return
	}
	after, ok2 := optionalID(w, "after", params.After)
	if !ok2 {
		return
	}
	around, ok3 := optionalID(w, "around", params.Around)
	if !ok3 {
		return
	}
	page, err := m.o.Service.History(r.Context(), p.UserID, scylla.PageQuery{
		ChatID: id, Before: before, After: after, Around: around, Limit: pageLimit(params.Limit),
	})
	if err != nil {
		serviceError(w, r, m.o.Log, "message history", err)
		return
	}
	items, err := m.render(r.Context(), p.UserID, id, page.Messages, "")
	if err != nil {
		serviceError(w, r, m.o.Log, "render messages", err)
		return
	}
	out := MessagePage{Items: items}
	if n := len(page.Messages); n > 0 {
		oldest, newest := FormatID(page.Messages[0].ID), FormatID(page.Messages[n-1].ID)
		older, newer := &oldest, &newest
		if !page.HasOlder {
			older = nil
		}
		if !page.HasNewer {
			newer = nil
		}
		// next continues in the direction of the request: newer for an after request, older otherwise.
		if after != 0 {
			out.NextCursor, out.PrevCursor = newer, older
		} else {
			out.NextCursor, out.PrevCursor = older, newer
		}
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

// SendMessage implements POST /chats/{id}/messages: 201 for a new message, 200 when this client_temp_id was
// used before.
func (m *MessageServer) SendMessage(w http.ResponseWriter, r *http.Request, chatID ChatId, _ SendMessageParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body SendMessageRequest
	if !decode(w, r, &body) {
		return
	}
	reply, ok := optionalID(w, "reply_to", body.ReplyTo)
	if !ok {
		return
	}
	if !allow(w, r, m.o.Limiter, RateSend, strconv.FormatInt(p.UserID, 10), m.o.Log) {
		return
	}
	req := messages.SendRequest{
		ChatID: id, SenderID: p.UserID, ClientTempID: body.ClientTempId, Type: string(body.Type), Content: body.Content,
	}
	if body.AttachmentId != nil {
		a := *body.AttachmentId
		req.AttachmentID = &a
	}
	if reply != 0 {
		req.ReplyTo = &reply
	}
	sent, err := m.o.Service.Send(r.Context(), req)
	if err != nil {
		serviceError(w, r, m.o.Log, "send message", err)
		return
	}
	items, err := m.render(r.Context(), p.UserID, id, []scylla.Message{sent.Message}, body.ClientTempId)
	if err != nil {
		serviceError(w, r, m.o.Log, "render message", err)
		return
	}
	status := http.StatusCreated
	if sent.Duplicate {
		status = http.StatusOK
	}
	noStore(w)
	writeJSONStatus(w, status, items[0])
}

// EditMessage implements PATCH /messages/{id}.
func (m *MessageServer) EditMessage(w http.ResponseWriter, r *http.Request, messageID MessageId, _ EditMessageParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "message_id", messageID)
	if !ok {
		return
	}
	var body EditMessageRequest
	if !decode(w, r, &body) {
		return
	}
	chat, err := m.o.Service.LocateVisible(r.Context(), p.UserID, id)
	if err != nil {
		serviceError(w, r, m.o.Log, "locate message", err)
		return
	}
	edited, err := m.o.Service.Edit(r.Context(), p.UserID, chat, id, body.Content)
	if err != nil {
		serviceError(w, r, m.o.Log, "edit message", err)
		return
	}
	items, err := m.render(r.Context(), p.UserID, chat, []scylla.Message{edited}, "")
	if err != nil {
		serviceError(w, r, m.o.Log, "render message", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, items[0])
}

// DeleteMessage implements DELETE /messages/{id}?scope=me|everyone.
func (m *MessageServer) DeleteMessage(w http.ResponseWriter, r *http.Request, messageID MessageId, params DeleteMessageParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "message_id", messageID)
	if !ok {
		return
	}
	if !params.Scope.Valid() {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "scope", Message: "me or everyone"})
		return
	}
	// Deleting for oneself works on any message of one's chats; for everyone only on messages one can see.
	locate := m.o.Service.LocateVisible
	if params.Scope == DeleteMessageParamsScopeMe {
		locate = m.o.Service.Locate
	}
	chat, err := locate(r.Context(), p.UserID, id)
	if err != nil {
		serviceError(w, r, m.o.Log, "locate message", err)
		return
	}
	if err := m.o.Service.Delete(r.Context(), p.UserID, chat, id, string(params.Scope)); err != nil {
		serviceError(w, r, m.o.Log, "delete message", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ForwardMessage implements POST /messages/{id}/forward.
func (m *MessageServer) ForwardMessage(w http.ResponseWriter, r *http.Request, messageID MessageId, _ ForwardMessageParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "message_id", messageID)
	if !ok {
		return
	}
	var body ForwardMessageRequest
	if !decode(w, r, &body) {
		return
	}
	targets := make([]int64, len(body.ChatIds))
	for i, s := range body.ChatIds {
		if targets[i], ok = parseID(w, "chat_ids", s); !ok {
			return
		}
	}
	subject := strconv.FormatInt(p.UserID, 10)
	if !allow(w, r, m.o.Limiter, RateForward, subject, m.o.Log) {
		return
	}
	// Every copy is a message: it uses up the sender's send budget like one.
	if !allowN(w, r, m.o.Limiter, RateSend, subject, len(targets), m.o.Log) {
		return
	}
	copies, err := m.o.Service.Forward(r.Context(), p.UserID, id, targets)
	if err != nil && len(copies) == 0 {
		serviceError(w, r, m.o.Log, "forward message", err)
		return
	}
	if err != nil {
		// Some copies are delivered: answering an error would make the client forward them again.
		m.o.Log.WarnContext(r.Context(), "forward stopped midway", "error", err, "written", len(copies))
	}
	items := make([]Message, 0, len(copies))
	for _, c := range copies {
		rendered, err := m.render(r.Context(), p.UserID, c.ChatID, []scylla.Message{c}, "")
		if err != nil {
			// Written already; a plainer rendering beats an error.
			m.o.Log.WarnContext(r.Context(), "render forwarded copy", "error", err)
			rendered = []Message{PresentMessage(c, MessageParts{BasePath: m.o.BasePath})}
		}
		items = append(items, rendered...)
	}
	noStore(w)
	writeJSONStatus(w, http.StatusCreated, map[string]any{"items": items})
}

// ListChatMedia implements GET /chats/{id}/media?kind=image|audio.
func (m *MessageServer) ListChatMedia(w http.ResponseWriter, r *http.Request, chatID ChatId, params ListChatMediaParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	kinds, known := messages.MediaKinds[string(params.Kind)]
	if !known {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "kind", Message: "image or audio"})
		return
	}
	var before int64
	if params.After != nil {
		if before, ok = parseID(w, "after", *params.After); !ok {
			return
		}
	}
	found, next, err := m.o.Service.Media(r.Context(), p.UserID, id, kinds, before, pageLimit(params.Limit))
	if err != nil {
		serviceError(w, r, m.o.Log, "chat media", err)
		return
	}
	msgs := make([]scylla.Message, len(found))
	files := make(map[uuid.UUID]postgres.Attachment, len(found))
	for i, it := range found {
		msgs[i], files[it.Attachment.ID] = it.Message, it.Attachment
	}
	rendered, err := m.renderWith(r.Context(), p.UserID, id, msgs, "", files)
	if err != nil {
		serviceError(w, r, m.o.Log, "render media", err)
		return
	}
	out := MessageSearchPage{Items: make([]SearchHit, len(rendered))}
	for i, msg := range rendered {
		out.Items[i] = SearchHit{Message: msg}
	}
	if next != 0 {
		c := FormatID(next)
		out.NextCursor = &c
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}
