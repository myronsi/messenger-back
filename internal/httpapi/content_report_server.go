package httpapi

import (
	"context"
	"net/http"
)

type ContentReporter interface {
	ReportMessage(ctx context.Context, reporter, chatID, messageID int64, reason string, note *string) error
	ReportGroup(ctx context.Context, reporter, chatID int64, reason string, note *string) error
	Withdraw(ctx context.Context, reporter, chatID, messageID int64) error
}

func (s *ModerationServer) contentReporter(w http.ResponseWriter, r *http.Request) (ContentReporter, int64, bool) {
	if s.o.Content == nil {
		notImplemented(w, r)
		return nil, 0, false
	}
	p, ok := principalOr401(w, r)
	if !ok {
		return nil, 0, false
	}
	return s.o.Content, p.UserID, true
}

func (s *ModerationServer) ReportMessage(w http.ResponseWriter, r *http.Request, chatID ChatId, messageID MessageId, _ ReportMessageParams) {
	c, reporter, ok := s.contentReporter(w, r)
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
	var body ContentReportRequest
	if !decode(w, r, &body) {
		return
	}
	if err := c.ReportMessage(r.Context(), reporter, chat, msg, string(body.Reason), body.Note); err != nil {
		s.fail(w, r, "report message", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *ModerationServer) UnreportMessage(w http.ResponseWriter, r *http.Request, chatID ChatId, messageID MessageId, _ UnreportMessageParams) {
	c, reporter, ok := s.contentReporter(w, r)
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
	if err := c.Withdraw(r.Context(), reporter, chat, msg); err != nil {
		s.fail(w, r, "withdraw message report", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *ModerationServer) ReportGroup(w http.ResponseWriter, r *http.Request, chatID ChatId, _ ReportGroupParams) {
	c, reporter, ok := s.contentReporter(w, r)
	if !ok {
		return
	}
	chat, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body ContentReportRequest
	if !decode(w, r, &body) {
		return
	}
	if err := c.ReportGroup(r.Context(), reporter, chat, string(body.Reason), body.Note); err != nil {
		s.fail(w, r, "report group", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *ModerationServer) UnreportGroup(w http.ResponseWriter, r *http.Request, chatID ChatId, _ UnreportGroupParams) {
	c, reporter, ok := s.contentReporter(w, r)
	if !ok {
		return
	}
	chat, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	if err := c.Withdraw(r.Context(), reporter, chat, 0); err != nil {
		s.fail(w, r, "withdraw group report", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
