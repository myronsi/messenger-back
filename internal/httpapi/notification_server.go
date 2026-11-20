package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/notifications"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type NotificationOptions struct {
	Store       postgres.NotificationRepository
	Chats       postgres.ChatRepository
	Messages    scylla.MessageRepository
	Attachments notifications.AttachmentGetter
	Emoji       notifications.EmojiGetter
	Config      config.Notifications
	Tokens      *notifications.ActionTokens
}

const notificationAvatarSize = 256

func (s *Server) WithNotifications(o NotificationOptions) *Server { s.notifications = &o; return s }
func (s *Server) GetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if s.notifications == nil {
		notImplemented(w, r)
		return
	}
	prefs, err := s.notifications.Store.Preferences(r.Context(), p.UserID)
	if err != nil {
		WriteProblem(w, 503, ErrorCodeInternalError)
		return
	}
	mutes, err := s.notifications.Store.Mutes(r.Context(), p.UserID)
	if err != nil {
		WriteProblem(w, 503, ErrorCodeInternalError)
		return
	}
	c := s.notifications.Config
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"preferences": prefs, "mutes": mutes, "web_push": c.WebEnabled(), "android_push": c.AndroidEnabled(), "ios_push": c.IOSEnabled(), "vapid_public_key": c.VAPIDPublic})
}
func (s *Server) UpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if s.notifications == nil {
		notImplemented(w, r)
		return
	}
	var b struct {
		Enabled           *bool `json:"enabled"`
		Sound             *bool `json:"sound"`
		Previews          *bool `json:"previews"`
		BirthdayReminders *bool `json:"birthday_reminders"`
		MentionsInMuted   *bool `json:"mentions_in_muted"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Enabled == nil || b.Sound == nil || b.Previews == nil {
		WriteProblem(w, 400, ErrorCodeInvalidRequest)
		return
	}
	prefs := postgres.NotificationPreferences{Enabled: *b.Enabled, Sound: *b.Sound, Previews: *b.Previews, BirthdayReminders: true, MentionsInMuted: true}
	if b.BirthdayReminders == nil || b.MentionsInMuted == nil {
		current, err := s.notifications.Store.Preferences(r.Context(), p.UserID)
		if err != nil {
			WriteProblem(w, 503, ErrorCodeInternalError)
			return
		}
		prefs.BirthdayReminders, prefs.MentionsInMuted = current.BirthdayReminders, current.MentionsInMuted
	}
	if b.BirthdayReminders != nil {
		prefs.BirthdayReminders = *b.BirthdayReminders
	}
	if b.MentionsInMuted != nil {
		prefs.MentionsInMuted = *b.MentionsInMuted
	}
	if err := s.notifications.Store.SetPreferences(r.Context(), p.UserID, prefs); err != nil {
		WriteProblem(w, 503, ErrorCodeInternalError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) SetNotificationMute(w http.ResponseWriter, r *http.Request, chatId Id) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if s.notifications == nil {
		notImplemented(w, r)
		return
	}
	cid, err := strconv.ParseInt(chatId, 10, 64)
	if err != nil || cid <= 0 {
		WriteProblem(w, 400, ErrorCodeInvalidRequest)
		return
	}
	var b struct {
		Duration string `json:"duration"`
	}
	if !decode(w, r, &b) {
		return
	}
	until := time.Now()
	switch b.Duration {
	case "1h":
		until = until.Add(time.Hour)
	case "8h":
		until = until.Add(8 * time.Hour)
	case "forever":
		until = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	case "off":
	default:
		WriteProblem(w, 400, ErrorCodeInvalidRequest)
		return
	}
	err = s.notifications.Store.Mute(r.Context(), p.UserID, cid, until)
	if errors.Is(err, postgres.ErrNotFound) {
		WriteProblem(w, 404, ErrorCodeNotFound)
		return
	}
	if err != nil {
		WriteProblem(w, 503, ErrorCodeInternalError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) RegisterNotificationDevice(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if s.notifications == nil {
		notImplemented(w, r)
		return
	}
	var d postgres.NotificationDevice
	if !decode(w, r, &d) {
		return
	}
	if !notifications.ValidateDevice(d) {
		WriteProblem(w, 400, ErrorCodeInvalidRequest)
		return
	}
	c := s.notifications.Config
	if d.Platform == "web" && !c.WebEnabled() || d.Platform == "android" && !c.AndroidEnabled() || d.Platform == "ios" && !c.IOSEnabled() {
		WriteProblem(w, 503, ErrorCodeInternalError)
		return
	}
	// A physical destination has one owner; signing in on it moves delivery to that session.
	d.ID = uuid.NewSHA1(uuid.Nil, []byte(d.Destination))
	err := s.notifications.Store.Register(r.Context(), p.UserID, p.SessionID, d)
	if errors.Is(err, postgres.ErrConflict) {
		WriteProblem(w, 409, ErrorCodeConflict)
		return
	}
	if err != nil {
		WriteProblem(w, 503, ErrorCodeInternalError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": d.ID.String()})
}
func (s *Server) DeleteNotificationDevice(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if s.notifications == nil {
		notImplemented(w, r)
		return
	}
	if err := s.notifications.Store.Unregister(r.Context(), p.UserID, p.SessionID, id); err != nil {
		WriteProblem(w, 503, ErrorCodeInternalError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) UpdateNotificationActivity(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if s.notifications == nil {
		notImplemented(w, r)
		return
	}
	var b struct {
		Active   *bool     `json:"active"`
		ClientID uuid.UUID `json:"client_id"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Active == nil || b.ClientID == uuid.Nil {
		WriteProblem(w, 400, ErrorCodeInvalidRequest)
		return
	}
	if err := s.notifications.Store.Activity(r.Context(), p.UserID, p.SessionID, id, b.ClientID, *b.Active); err != nil {
		WriteProblem(w, 503, ErrorCodeInternalError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) actionToken(w http.ResponseWriter, r *http.Request, parse func(string) (notifications.ActionToken, error), token string) (notifications.ActionToken, bool) {
	if s.notifications == nil || s.notifications.Store == nil {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return notifications.ActionToken{}, false
	}
	t, err := parse(token)
	if err != nil {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return notifications.ActionToken{}, false
	}
	active, err := s.notifications.Store.DeviceActive(r.Context(), t.Recipient, t.Device, t.Session)
	if err != nil {
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
		return notifications.ActionToken{}, false
	}
	if !active {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return notifications.ActionToken{}, false
	}
	return t, true
}

func (s *Server) GetNotificationAvatar(w http.ResponseWriter, r *http.Request, token string) {
	if s.media == nil {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if strings.HasPrefix(token, "c.") {
		s.notificationChatAvatar(w, r, token)
		return
	}
	if strings.HasPrefix(token, "m.") {
		s.notificationMessageImage(w, r, token)
		return
	}
	t, ok := s.actionToken(w, r, s.notifications.tokens().ParseAvatar, token)
	if !ok {
		return
	}
	ctx := context.WithValue(r.Context(), principalKey{}, auth.Principal{UserID: t.Recipient})
	size := notificationAvatarSize
	s.media.GetUserAvatar(w, r.WithContext(ctx), strconv.FormatInt(t.IDs[0], 10), GetUserAvatarParams{Size: &size})
}

func (s *Server) notificationMessageImage(w http.ResponseWriter, r *http.Request, token string) {
	o := s.notifications
	if o == nil || o.Chats == nil || o.Messages == nil || o.Attachments == nil || o.Emoji == nil {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	t, ok := s.actionToken(w, r, o.tokens().ParseMessageImage, token)
	if !ok {
		return
	}
	chatID, messageID := t.IDs[0], t.IDs[1]
	part, err := o.Chats.Participant(r.Context(), chatID, t.Recipient)
	if errors.Is(err, postgres.ErrNotFound) {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if err != nil {
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
		return
	}
	m, err := o.Messages.Get(r.Context(), chatID, messageID)
	if errors.Is(err, scylla.ErrNotFound) || err == nil && part.JoinedAt.After(m.CreatedAt) {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if err != nil {
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
		return
	}
	hidden, err := o.Messages.Hidden(r.Context(), t.Recipient, chatID, messageID)
	if err != nil {
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
		return
	}
	if hidden {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	src, err := notifications.FindImage(r.Context(), m, o.Attachments, o.Emoji)
	if errors.Is(err, notifications.ErrNoImage) {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if err != nil {
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
		return
	}
	s.media.ServeNotificationImage(w, r, src)
}

func (s *Server) notificationChatAvatar(w http.ResponseWriter, r *http.Request, token string) {
	if s.notifications == nil || s.notifications.Chats == nil {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	t, ok := s.actionToken(w, r, s.notifications.tokens().ParseChatAvatar, token)
	if !ok {
		return
	}
	chats := s.notifications.Chats
	_, err := chats.Participant(r.Context(), t.IDs[0], t.Recipient)
	if errors.Is(err, postgres.ErrNotFound) {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if err != nil {
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
		return
	}
	chat, err := chats.Get(r.Context(), t.IDs[0])
	if errors.Is(err, postgres.ErrNotFound) || err == nil && chat.Type != postgres.ChatGroup {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if err != nil {
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
		return
	}
	s.media.ServeChatAvatar(w, r, chat.AvatarAttachmentID, notificationAvatarSize)
}

func (s *Server) MarkNotificationRead(w http.ResponseWriter, r *http.Request, token string) {
	if s.chats == nil {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	t, ok := s.actionToken(w, r, s.notifications.tokens().ParseRead, token)
	if !ok {
		return
	}
	if _, err := s.chats.o.Service.MarkRead(r.Context(), t.Recipient, t.IDs[0], t.IDs[1]); err != nil {
		serviceError(w, r, s.chats.o.Log, "mark read from notification", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (o *NotificationOptions) tokens() *notifications.ActionTokens {
	if o == nil {
		return nil
	}
	return o.Tokens
}
