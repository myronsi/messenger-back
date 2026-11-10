package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/gocql/gocql"
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
	Stickers() postgres.StickerRepository
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
	origins := make([]scylla.Message, 0, len(deco.Replies))
	for _, o := range deco.Replies {
		origins = append(origins, o)
	}
	for _, msg := range append(slices.Clone(msgs), origins...) {
		if msg.SenderID != nil {
			senders = append(senders, *msg.SenderID)
		}
		for _, attachmentID := range msg.AttachmentIDs() {
			id := uuid.UUID(attachmentID)
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
	drawings, err := m.drawings(ctx, chatID, append(slices.Clone(msgs), origins...))
	if err != nil {
		return nil, err
	}
	stickers, err := m.o.Store.Stickers().Stickers(ctx, uniq(StickerIDs(msgs...)))
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
		parts := MessageParts{Reactions: deco.Reactions[msg.ID], BasePath: m.o.BasePath, Drawings: drawings[msg.ID]}
		if msg.StickerID != nil {
			if st, ok := stickers[*msg.StickerID]; ok {
				parts.Sticker = &st
			}
		}
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
		for _, id := range msg.AlbumIDs {
			if a := files[uuid.UUID(id)]; a != nil {
				parts.Album = append(parts.Album, *a)
			}
		}
		if msg.ReplyTo != nil {
			if o, ok := deco.Replies[*msg.ReplyTo]; ok {
				parts.Reply = &ReplyOrigin{Message: o}
				if o.SenderID != nil {
					v := people[*o.SenderID]
					parts.Reply.Sender = &v
				}
				if o.AttachmentID != nil {
					parts.Reply.Attachment = files[uuid.UUID(*o.AttachmentID)]
					parts.Reply.Drawing = drawings[o.ID][uuid.UUID(*o.AttachmentID)]
				}
			}
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

func (m *MessageServer) drawings(ctx context.Context, chatID int64, msgs []scylla.Message) (map[int64]map[uuid.UUID][]byte, error) {
	var withFiles []int64
	for _, msg := range msgs {
		if len(msg.AttachmentIDs()) > 0 {
			withFiles = append(withFiles, msg.ID)
		}
	}
	found, err := m.o.Store.Attachments().Drawings(ctx, chatID, uniq(withFiles))
	if err != nil {
		return nil, err
	}
	return GroupDrawings(found), nil
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
	if body.AttachmentIds != nil {
		req.AlbumIDs = *body.AttachmentIds
	}
	if body.Drawings != nil {
		req.Drawings = make(map[uuid.UUID][]byte, len(*body.Drawings))
		for _, d := range *body.Drawings {
			if _, dup := req.Drawings[d.AttachmentId]; dup {
				WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "drawings", Message: "at most one drawing per attachment"})
				return
			}
			raw, err := json.Marshal(d.Drawing)
			if err != nil {
				WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "drawings", Message: "not a drawing"})
				return
			}
			req.Drawings[d.AttachmentId] = raw
		}
	}
	if reply != 0 {
		req.ReplyTo = &reply
	}
	if body.StickerId != nil {
		sticker, ok := parseID(w, "sticker_id", *body.StickerId)
		if !ok {
			return
		}
		req.StickerID = &sticker
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

func parseIDs(w http.ResponseWriter, field string, list []Id) ([]int64, bool) {
	out := make([]int64, len(list))
	for i, s := range list {
		id, ok := parseID(w, field, s)
		if !ok {
			return nil, false
		}
		out[i] = id
	}
	return out, true
}

func (m *MessageServer) DeleteMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, _ DeleteMessagesParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	chat, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body DeleteMessagesRequest
	if !decode(w, r, &body) {
		return
	}
	if !body.Scope.Valid() {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "scope", Message: "me or everyone"})
		return
	}
	ids, ok := parseIDs(w, "message_ids", body.MessageIds)
	if !ok {
		return
	}
	if err := m.o.Service.DeleteMany(r.Context(), p.UserID, chat, ids, string(body.Scope)); err != nil {
		serviceError(w, r, m.o.Log, "delete messages", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *MessageServer) ForwardMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, _ ForwardMessagesParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	chat, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body ForwardMessagesRequest
	if !decode(w, r, &body) {
		return
	}
	sources, ok := parseIDs(w, "message_ids", body.MessageIds)
	if !ok {
		return
	}
	targets, ok := parseIDs(w, "chat_ids", body.ChatIds)
	if !ok {
		return
	}
	if !m.chargeForward(w, r, p.UserID, sources, targets) {
		return
	}
	copies, err := m.o.Service.ForwardMany(r.Context(), p.UserID, chat, sources, targets)
	m.answerForward(w, r, copies, err)
}

func (m *MessageServer) chargeForward(w http.ResponseWriter, r *http.Request, userID int64, sources, targets []int64) bool {
	if err := messages.CheckForward(sources, targets); err != nil {
		serviceError(w, r, m.o.Log, "forward message", err)
		return false
	}
	subject := strconv.FormatInt(userID, 10)
	if !allow(w, r, m.o.Limiter, RateForward, subject, m.o.Log) {
		return false
	}
	// Every copy is a message: it uses up the sender's send budget like one.
	return allowN(w, r, m.o.Limiter, RateSend, subject, len(sources)*len(targets), m.o.Log)
}

func (m *MessageServer) answerForward(w http.ResponseWriter, r *http.Request, copies []scylla.Message, err error) {
	p, _ := PrincipalFrom(r.Context())
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
	targets, ok := parseIDs(w, "chat_ids", body.ChatIds)
	if !ok {
		return
	}
	if !m.chargeForward(w, r, p.UserID, []int64{id}, targets) {
		return
	}
	copies, err := m.o.Service.Forward(r.Context(), p.UserID, id, targets)
	m.answerForward(w, r, copies, err)
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
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "kind", Message: "image, video gallery (visual), or audio"})
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
		// Each media-list entry represents one matching file, even when its chat message is an album.
		msgs[i] = it.Message
		id := gocql.UUID(it.Attachment.ID)
		msgs[i].AttachmentID, msgs[i].AlbumIDs = &id, nil
		files[it.Attachment.ID] = it.Attachment
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
