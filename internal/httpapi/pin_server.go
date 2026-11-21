package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func (m *MessageServer) PinMessage(w http.ResponseWriter, r *http.Request, chatID ChatId, messageID MessageId, _ PinMessageParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	chat, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	msg, ok := parseID(w, "message_id", messageID)
	if !ok {
		return
	}
	var body PinMessageRequest
	if !decode(w, r, &body) {
		return
	}
	pin, err := m.o.Service.Pin(r.Context(), p.UserID, chat, msg, string(body.Scope))
	if err != nil {
		serviceError(w, r, m.o.Log, "pin message", err)
		return
	}
	out, err := m.presentPins(r.Context(), p.UserID, chat, []messages.Pin{pin})
	if err != nil {
		serviceError(w, r, m.o.Log, "render pin", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out[0])
}

func (m *MessageServer) UnpinMessage(w http.ResponseWriter, r *http.Request, chatID ChatId, messageID MessageId, params UnpinMessageParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	chat, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	msg, ok := parseID(w, "message_id", messageID)
	if !ok {
		return
	}
	if params.Scope != PinScopeMe && params.Scope != PinScopeEveryone {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest, ProblemError{Field: "scope", Message: "me or everyone"})
		return
	}
	if err := m.o.Service.Unpin(r.Context(), p.UserID, chat, msg, string(params.Scope)); err != nil {
		serviceError(w, r, m.o.Log, "unpin message", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *MessageServer) ListPinnedMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, params ListPinnedMessagesParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	chat, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var after *postgres.PinCursor
	if params.After != nil && *params.After != "" {
		if after, ok = parsePinCursor(*params.After); !ok {
			WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest, ProblemError{Field: "after", Message: "unknown cursor"})
			return
		}
	}
	page, err := m.o.Service.Pins(r.Context(), p.UserID, chat, after, pageLimit(params.Limit))
	if err != nil {
		serviceError(w, r, m.o.Log, "pinned messages", err)
		return
	}
	items, err := m.presentPins(r.Context(), p.UserID, chat, page.Pins)
	if err != nil {
		serviceError(w, r, m.o.Log, "render pins", err)
		return
	}
	out := PinnedMessagePage{Items: items}
	if page.Next != nil {
		c := strconv.FormatInt(page.Next.PinnedAt.UnixNano(), 10) + "_" + strconv.FormatInt(page.Next.MessageID, 10)
		out.NextCursor = &c
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func parsePinCursor(s string) (*postgres.PinCursor, bool) {
	ts, id, found := strings.Cut(s, "_")
	nanos, err1 := strconv.ParseInt(ts, 10, 64)
	msg, err2 := strconv.ParseInt(id, 10, 64)
	if !found || err1 != nil || err2 != nil {
		return nil, false
	}
	return &postgres.PinCursor{PinnedAt: time.Unix(0, nanos), MessageID: msg}, true
}

func (m *MessageServer) presentPins(ctx context.Context, viewer, chatID int64, pins []messages.Pin) ([]PinnedMessage, error) {
	msgs := make([]scylla.Message, len(pins))
	for i, pin := range pins {
		msgs[i] = pin.Message
	}
	rendered, err := m.render(ctx, viewer, chatID, msgs, "")
	if err != nil {
		return nil, err
	}
	out := make([]PinnedMessage, len(pins))
	for i, pin := range pins {
		out[i] = PinnedMessage{
			Message: rendered[i], Scope: PinScope(pin.Scope), PinnedBy: FormatID(pin.PinnedBy),
			PinnedAt: utc(pin.PinnedAt), CanUnpinForEveryone: pin.CanUnpinForEveryone,
		}
	}
	return out, nil
}
