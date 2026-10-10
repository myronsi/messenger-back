package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/myronsi/messenger-back/internal/search"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

// RateMessageSearch limits message searches per user.
var RateMessageSearch = redis.Rate{Name: "message_search", Rate: 60, Period: time.Minute, Burst: 20}

func (u Unimplemented) SearchAllMessages(w http.ResponseWriter, r *http.Request, _ SearchAllMessagesParams) {
	notImplemented(w, r)
}

// SearchMessages implements GET /chats/{id}/messages/search.
func (m *MessageServer) SearchMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, params SearchMessagesParams) {
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	m.search(w, r, search.Query{Text: params.Q, ChatID: id, Limit: pageLimit(params.Limit), After: cursorParam(params.After)})
}

// SearchAllMessages implements GET /search/messages.
func (m *MessageServer) SearchAllMessages(w http.ResponseWriter, r *http.Request, params SearchAllMessagesParams) {
	q := search.Query{Text: params.Q, Limit: pageLimit(params.Limit), After: cursorParam(params.After)}
	var ok bool
	if params.ChatId != nil {
		if q.ChatID, ok = parseID(w, "chat_id", *params.ChatId); !ok {
			return
		}
	}
	if params.SenderId != nil {
		if q.SenderID, ok = parseID(w, "sender_id", *params.SenderId); !ok {
			return
		}
	}
	if params.Type != nil {
		if !params.Type.Valid() {
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "type", Message: "text, file or voice"})
			return
		}
		q.Type = string(*params.Type)
	}
	if params.From != nil {
		q.From = *params.From
	}
	if params.To != nil {
		q.To = *params.To
	}
	m.search(w, r, q)
}

func (m *MessageServer) search(w http.ResponseWriter, r *http.Request, q search.Query) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if m.o.Searcher == nil {
		notImplemented(w, r)
		return
	}
	if !allow(w, r, m.o.Limiter, RateMessageSearch, strconv.FormatInt(p.UserID, 10), m.o.Log) {
		return
	}
	hits, next, err := m.o.Searcher.Search(r.Context(), p.UserID, q)
	switch {
	case errors.Is(err, search.ErrBadCursor):
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest, ProblemError{Field: "after", Message: "unknown cursor"})
		return
	case errors.Is(err, search.ErrInvalid):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "q", Message: err.Error()})
		return
	case errors.Is(err, search.ErrNotFound):
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	case errors.Is(err, search.ErrUnavailable):
		m.o.Log.WarnContext(r.Context(), "search", "error", err)
		w.Header().Set("Retry-After", "2")
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
		return
	case err != nil:
		serviceError(w, r, m.o.Log, "search", err)
		return
	}
	// Render the hits chat by chat (senders, files and reactions as the caller sees them), keeping their order.
	byChat := map[int64][]scylla.Message{}
	for _, h := range hits {
		byChat[h.Message.ChatID] = append(byChat[h.Message.ChatID], h.Message)
	}
	rendered := map[int64]Message{}
	for chatID, msgs := range byChat {
		items, err := m.render(r.Context(), p.UserID, chatID, msgs, "")
		if err != nil {
			serviceError(w, r, m.o.Log, "render search hits", err)
			return
		}
		for i, msg := range msgs {
			rendered[msg.ID] = items[i]
		}
	}
	out := MessageSearchPage{Items: make([]SearchHit, len(hits))}
	for i, h := range hits {
		out.Items[i] = SearchHit{Message: rendered[h.Message.ID], Highlight: h.Highlight}
	}
	if next != "" {
		out.NextCursor = &next
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}
