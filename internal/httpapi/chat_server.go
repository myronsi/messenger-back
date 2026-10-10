package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/chats"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

// ChatOptions configures the chat list, direct chat and request endpoints.
type ChatOptions struct {
	Service  *chats.Service
	Limiter  RateAllower
	BasePath string
	Log      *slog.Logger
}

// ChatServer implements the chat list, direct chats, pins, read markers and approval requests.
type ChatServer struct{ o ChatOptions }

// NewChatServer returns the endpoints.
func NewChatServer(o ChatOptions) *ChatServer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &ChatServer{o: o}
}

// RateCreateChat limits new direct chats and requests per user.
var RateCreateChat = redis.Rate{Name: "create_chat", Rate: 60, Period: time.Hour, Burst: 20}

// PresentRole renders a group role; the contract knows no moderators, who act as admins.
func PresentRole(r postgres.Role) GroupRole {
	switch r {
	case postgres.RoleOwner:
		return GroupRoleOwner
	case postgres.RoleAdmin, postgres.RoleModerator:
		return GroupRoleAdmin
	}
	return GroupRoleMember
}

// PresentChat renders a chat as its viewer sees it.
func PresentChat(v chats.View, basePath string) Chat {
	c := v.Entry.Chat
	out := Chat{
		Id: FormatID(c.ID), Type: ChatType(c.Type), IsPinned: v.Entry.PinnedAt != nil,
		UnreadCount: int(v.Unread), CreatedAt: utc(c.CreatedAt),
	}
	if c.Type == postgres.ChatDirect {
		if v.Peer != nil {
			p := PresentUser(*v.Peer)
			out.Peer = &p
			out.Name = v.Peer.DisplayName
			if v.Peer.ContactName != nil {
				out.Name = *v.Peer.ContactName
			}
			out.AvatarUrl = v.Peer.AvatarURL
		}
	} else {
		if c.Name != nil {
			out.Name = *c.Name
		}
		if c.AvatarAttachmentID != nil {
			url := basePath + "/attachments/" + c.AvatarAttachmentID.String() + "/content"
			out.AvatarUrl = &url
		}
		role := PresentRole(v.Entry.Role)
		out.MyRole = &role
	}
	if l := v.Last; l != nil {
		parts := MessageParts{Sender: l.Sender, Attachment: l.Attachment, BasePath: basePath}
		for _, m := range l.ReadBy {
			parts.ReadBy = append(parts.ReadBy, ReadReceipt{UserId: FormatID(m.UserID), ReadAt: utc(*m.At)})
		}
		msg := PresentMessage(l.Message, parts)
		out.LastMessage = &msg
	}
	return out
}

// PresentRequest renders an approval request for its recipient.
func PresentRequest(r chats.RequestView) ApprovalRequest {
	out := ApprovalRequest{
		Id: FormatID(r.Request.ID), Type: ApprovalRequestType(r.Request.Type), Status: ApprovalRequestStatus(r.Request.Status),
		Requester: PresentUser(r.Requester), GroupName: r.GroupName, CreatedAt: utc(r.Request.CreatedAt),
	}
	if r.Request.Type == postgres.RequestDirectMessage {
		out.Preview = r.Request.MessageText
	}
	return out
}

// serviceError answers a failure of the chat or message service.
func serviceError(w http.ResponseWriter, r *http.Request, log *slog.Logger, what string, err error) {
	detail := func() string {
		_, msg, _ := strings.Cut(err.Error(), ": ")
		return msg
	}
	switch {
	case errors.Is(err, messages.ErrNotFound):
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
	case errors.Is(err, messages.ErrBlocked):
		WriteProblem(w, http.StatusForbidden, ErrorCodeBlockedByUser)
	case errors.Is(err, messages.ErrForbidden):
		WriteProblem(w, http.StatusForbidden, ErrorCodeForbidden)
	case errors.Is(err, chats.ErrConflict):
		WriteProblem(w, http.StatusConflict, ErrorCodeConflict, ProblemError{Message: detail()})
	case errors.Is(err, messages.ErrInvalid):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Message: detail()})
	case errors.Is(err, messages.ErrUnavailable):
		log.WarnContext(r.Context(), what, "error", err)
		w.Header().Set("Retry-After", "2")
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
	default:
		log.ErrorContext(r.Context(), what, "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
	}
}

// parseID reads a path or body id; a malformed one is answered with 400.
func parseID(w http.ResponseWriter, field, s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest, ProblemError{Field: field, Message: "not an id"})
		return 0, false
	}
	return id, true
}

func cursorParam(after *After) string {
	if after == nil {
		return ""
	}
	return *after
}

// ListChats implements GET /chats.
func (c *ChatServer) ListChats(w http.ResponseWriter, r *http.Request, params ListChatsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	views, next, err := c.o.Service.List(r.Context(), p.UserID, cursorParam(params.After), pageLimit(params.Limit))
	if err != nil {
		serviceError(w, r, c.o.Log, "list chats", err)
		return
	}
	page := ChatPage{Items: make([]Chat, len(views))}
	for i, v := range views {
		page.Items[i] = PresentChat(v, c.o.BasePath)
	}
	if next != "" {
		page.NextCursor = &next
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, page)
}

// GetChat implements GET /chats/{id}.
func (c *ChatServer) GetChat(w http.ResponseWriter, r *http.Request, chatID ChatId, _ GetChatParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	v, err := c.o.Service.Get(r.Context(), p.UserID, id)
	if err != nil {
		serviceError(w, r, c.o.Log, "get chat", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PresentChat(v, c.o.BasePath))
}

// CreateChat implements POST /chats: 201 for a new chat, 200 for an existing one, 202 when the other user
// approves first.
func (c *ChatServer) CreateChat(w http.ResponseWriter, r *http.Request, _ CreateChatParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	var body CreateChatRequest
	if !decode(w, r, &body) {
		return
	}
	other, ok := parseID(w, "user_id", body.UserId)
	if !ok {
		return
	}
	if !allow(w, r, c.o.Limiter, RateCreateChat, strconv.FormatInt(p.UserID, 10), c.o.Log) {
		return
	}
	res, err := c.o.Service.CreateDirect(r.Context(), p.UserID, other, body.InitialMessage)
	if err != nil {
		serviceError(w, r, c.o.Log, "create chat", err)
		return
	}
	noStore(w)
	switch {
	case res.Request != nil:
		writeJSONStatus(w, http.StatusAccepted, PresentRequest(*res.Request))
	case res.New:
		writeJSONStatus(w, http.StatusCreated, PresentChat(*res.Chat, c.o.BasePath))
	default:
		writeJSONStatus(w, http.StatusOK, PresentChat(*res.Chat, c.o.BasePath))
	}
}

// DeleteChat implements DELETE /chats/{id}.
func (c *ChatServer) DeleteChat(w http.ResponseWriter, r *http.Request, chatID ChatId, _ DeleteChatParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	if err := c.o.Service.Delete(r.Context(), p.UserID, id); err != nil {
		serviceError(w, r, c.o.Log, "delete chat", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PinChat implements PUT /chats/{id}/pin.
func (c *ChatServer) PinChat(w http.ResponseWriter, r *http.Request, chatID ChatId, _ PinChatParams) {
	c.pin(w, r, chatID, true)
}

// UnpinChat implements DELETE /chats/{id}/pin.
func (c *ChatServer) UnpinChat(w http.ResponseWriter, r *http.Request, chatID ChatId, _ UnpinChatParams) {
	c.pin(w, r, chatID, false)
}

func (c *ChatServer) pin(w http.ResponseWriter, r *http.Request, chatID string, on bool) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var err error
	if on {
		err = c.o.Service.Pin(r.Context(), p.UserID, id)
	} else {
		err = c.o.Service.Unpin(r.Context(), p.UserID, id)
	}
	if err != nil {
		serviceError(w, r, c.o.Log, "pin chat", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MarkChatRead implements POST /chats/{id}/read.
func (c *ChatServer) MarkChatRead(w http.ResponseWriter, r *http.Request, chatID ChatId, _ MarkChatReadParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body MarkReadRequest
	if !decode(w, r, &body) {
		return
	}
	msg, ok := parseID(w, "message_id", body.MessageId)
	if !ok {
		return
	}
	n, err := c.o.Service.MarkRead(r.Context(), p.UserID, id, msg)
	if err != nil {
		serviceError(w, r, c.o.Log, "mark read", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, map[string]int{"unread_count": n})
}

// ListApprovalRequests implements GET /requests.
func (c *ChatServer) ListApprovalRequests(w http.ResponseWriter, r *http.Request, params ListApprovalRequestsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	reqs, next, err := c.o.Service.Requests(r.Context(), p.UserID, cursorParam(params.After), pageLimit(params.Limit))
	if err != nil {
		serviceError(w, r, c.o.Log, "list requests", err)
		return
	}
	items := make([]ApprovalRequest, len(reqs))
	for i, rv := range reqs {
		items[i] = PresentRequest(rv)
	}
	var cursor *Cursor
	if next != "" {
		cursor = &next
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, map[string]any{"items": items, "next_cursor": cursor})
}

// ApproveRequest implements POST /requests/{id}/approve.
func (c *ChatServer) ApproveRequest(w http.ResponseWriter, r *http.Request, requestID RequestId, _ ApproveRequestParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "request_id", requestID)
	if !ok {
		return
	}
	v, err := c.o.Service.Approve(r.Context(), p.UserID, id)
	if err != nil {
		serviceError(w, r, c.o.Log, "approve request", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PresentChat(v, c.o.BasePath))
}

// RejectRequest implements POST /requests/{id}/reject.
func (c *ChatServer) RejectRequest(w http.ResponseWriter, r *http.Request, requestID RequestId, _ RejectRequestParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "request_id", requestID)
	if !ok {
		return
	}
	if err := c.o.Service.Reject(r.Context(), p.UserID, id); err != nil {
		serviceError(w, r, c.o.Log, "reject request", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
