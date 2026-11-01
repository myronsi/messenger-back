package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/notifications"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type NotificationOptions struct {
	Store  postgres.NotificationRepository
	Config config.Notifications
}

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
		Enabled  *bool `json:"enabled"`
		Sound    *bool `json:"sound"`
		Previews *bool `json:"previews"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Enabled == nil || b.Sound == nil || b.Previews == nil {
		WriteProblem(w, 400, ErrorCodeInvalidRequest)
		return
	}
	if err := s.notifications.Store.SetPreferences(r.Context(), p.UserID, postgres.NotificationPreferences{Enabled: *b.Enabled, Sound: *b.Sound, Previews: *b.Previews}); err != nil {
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
