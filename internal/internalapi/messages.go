package internalapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type MessageRemover interface {
	RemoveByModeration(ctx context.Context, chatID, messageID int64, reason scylla.ModerationRemoval) (scylla.Message, bool, error)
}

type removeRequest struct {
	ReasonCode string `json:"reason_code"`
	ReasonText string `json:"reason_text"`
}

type removeResponse struct {
	ChatID         string  `json:"chat_id"`
	MessageID      string  `json:"message_id"`
	SenderID       *string `json:"sender_id"`
	ReasonCode     string  `json:"reason_code"`
	ReasonText     string  `json:"reason_text"`
	AlreadyRemoved bool    `json:"already_removed"`
}

func removeMessage(remover MessageRemover, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chatID, ok := pathID(w, r, "chat_id")
		if !ok {
			return
		}
		messageID, ok := pathID(w, r, "message_id")
		if !ok {
			return
		}
		var body removeRequest
		if !decodeBody(w, r, &body) {
			return
		}
		if !moderation.DecisionReasons[body.ReasonCode] {
			invalid(w, "reason_code", "unknown reason")
			return
		}
		m, changed, err := remover.RemoveByModeration(r.Context(), chatID, messageID, scylla.ModerationRemoval{Code: body.ReasonCode, Text: body.ReasonText})
		switch {
		case errors.Is(err, messages.ErrNotFound):
			writeJSON(w, http.StatusNotFound, problem{Error: "not_found"})
			return
		case errors.Is(err, messages.ErrInvalid):
			invalid(w, "message_id", "system messages cannot be removed")
			return
		case err != nil:
			log.ErrorContext(r.Context(), "internal api: remove message", "error", err)
			w.Header().Set("Retry-After", "2")
			writeJSON(w, http.StatusServiceUnavailable, problem{Error: "unavailable"})
			return
		}
		out := removeResponse{ChatID: strconv.FormatInt(chatID, 10), MessageID: strconv.FormatInt(messageID, 10), AlreadyRemoved: !changed}
		if m.SenderID != nil {
			s := strconv.FormatInt(*m.SenderID, 10)
			out.SenderID = &s
		}
		if m.Moderation != nil {
			out.ReasonCode, out.ReasonText = m.Moderation.Code, m.Moderation.Text
		}
		if changed {
			log.InfoContext(r.Context(), "internal api: message removed by moderation", "chat_id", chatID, "message_id", messageID, "reason", body.ReasonCode)
		}
		writeJSON(w, http.StatusOK, out)
	}
}
