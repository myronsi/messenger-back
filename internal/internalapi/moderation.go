package internalapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const maxBody = 16 << 10

type Moderation interface {
	Decide(ctx context.Context, d moderation.Decision) (moderation.DecisionResult, error)
	DismissReports(ctx context.Context, userID int64, reporterIDs []int64) (moderation.DismissResult, error)
	HidePack(ctx context.Context, kind moderation.PackKind, packID int64) (bool, error)
	UnhidePack(ctx context.Context, kind moderation.PackKind, packID int64) (bool, error)
}

type AccountDeleter interface {
	DeleteAccountByStaff(ctx context.Context, userID int64) (postgres.DeletedAccount, error)
}

type EventLog interface {
	Emit(ctx context.Context, e events.Event) error
}

type actions struct {
	moderation    Moderation
	accounts      AccountDeleter
	afterDeletion func(ctx context.Context, userID int64, deleted postgres.DeletedAccount)
	events        EventLog
	images        PackImages
	log           *slog.Logger
}

func (a actions) register(mux *http.ServeMux) {
	if a.moderation != nil {
		mux.HandleFunc("POST "+BasePath+"/users/{user_id}/status", a.setStatus)
		mux.HandleFunc("POST "+BasePath+"/users/{user_id}/reports/dismiss", a.dismissReports)
		mux.HandleFunc("POST "+BasePath+"/packs/{kind}/{pack_id}/hide", a.pack(true))
		mux.HandleFunc("POST "+BasePath+"/packs/{kind}/{pack_id}/unhide", a.pack(false))
	}
	if a.accounts != nil {
		mux.HandleFunc("DELETE "+BasePath+"/users/{user_id}", a.deleteAccount)
	}
	if a.images != nil {
		mux.HandleFunc("GET "+BasePath+"/pack-images/{attachment_id}", a.packImage)
	}
}

type problem struct {
	Error   string `json:"error"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message,omitempty"`
}

func invalid(w http.ResponseWriter, field, message string) {
	writeJSON(w, http.StatusBadRequest, problem{Error: "invalid_request", Field: field, Message: message})
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		invalid(w, name, "not an id")
		return 0, false
	}
	return id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		invalid(w, "body", "invalid JSON")
		return false
	}
	return true
}

func (a actions) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	var field *moderation.FieldError
	switch {
	case errors.As(err, &field):
		invalid(w, field.Field, field.Message)
	case errors.Is(err, moderation.ErrNotFound), errors.Is(err, postgres.ErrNotFound):
		writeJSON(w, http.StatusNotFound, problem{Error: "not_found"})
	case errors.Is(err, moderation.ErrForbidden):
		writeJSON(w, http.StatusForbidden, problem{Error: "forbidden"})
	default:
		a.log.ErrorContext(r.Context(), "internal api: "+what, "error", err)
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusServiceUnavailable, problem{Error: "unavailable"})
	}
}

type statusView = moderation.StatusEvent

func viewOf(v moderation.View) statusView { return moderation.EventData(v) }

type setStatusRequest struct {
	Status     string `json:"status"`
	ReasonCode string `json:"reason_code"`
	ReasonText string `json:"reason_text"`
	StaffRef   string `json:"staff_ref"`
}

type changeResponse struct {
	Before statusView `json:"before"`
	After  statusView `json:"after"`
}

func (a actions) setStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, "user_id")
	if !ok {
		return
	}
	var body setStatusRequest
	if !decodeBody(w, r, &body) {
		return
	}
	res, err := a.moderation.Decide(r.Context(), moderation.Decision{
		UserID: userID, Status: moderation.Status(body.Status), ReasonCode: body.ReasonCode, ReasonText: body.ReasonText, StaffRef: body.StaffRef,
	})
	if err != nil {
		a.fail(w, r, "set status", err)
		return
	}
	writeJSON(w, http.StatusOK, changeResponse{Before: viewOf(res.Before), After: viewOf(res.After)})
}

type dismissRequest struct {
	ReporterIDs *[]string `json:"reporter_ids"`
}

type dismissResponse struct {
	Dismissed int        `json:"dismissed"`
	Before    statusView `json:"before"`
	After     statusView `json:"after"`
}

func (a actions) dismissReports(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, "user_id")
	if !ok {
		return
	}
	var body dismissRequest
	if r.ContentLength != 0 && !decodeBody(w, r, &body) {
		return
	}
	var reporters []int64
	if body.ReporterIDs != nil {
		reporters = make([]int64, 0, len(*body.ReporterIDs))
		for _, s := range *body.ReporterIDs {
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil || id <= 0 {
				invalid(w, "reporter_ids", "not an id")
				return
			}
			reporters = append(reporters, id)
		}
	}
	res, err := a.moderation.DismissReports(r.Context(), userID, reporters)
	if err != nil {
		a.fail(w, r, "dismiss reports", err)
		return
	}
	writeJSON(w, http.StatusOK, dismissResponse{Dismissed: res.Dismissed, Before: viewOf(res.Before), After: viewOf(res.After)})
}

func (a actions) pack(hide bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := moderation.PackKind(r.PathValue("kind"))
		if kind != moderation.StickerPack && kind != moderation.EmojiPack {
			invalid(w, "kind", "sticker_pack or emoji_pack")
			return
		}
		packID, ok := pathID(w, r, "pack_id")
		if !ok {
			return
		}
		do, what := a.moderation.UnhidePack, "unhide pack"
		if hide {
			do, what = a.moderation.HidePack, "hide pack"
		}
		changed, err := do(r.Context(), kind, packID)
		if err != nil {
			a.fail(w, r, what, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"changed": changed})
	}
}

func (a actions) deleteAccount(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, "user_id")
	if !ok {
		return
	}
	deleted, err := a.accounts.DeleteAccountByStaff(r.Context(), userID)
	if err != nil && !errors.Is(err, auth.ErrRevocationIncomplete) {
		a.fail(w, r, "delete account", err)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	if a.afterDeletion != nil {
		a.afterDeletion(ctx, userID, deleted)
	}
	if a.events != nil {
		if err := a.events.Emit(ctx, events.Event{Type: events.UserDeleted, UserID: userID}); err != nil {
			a.log.WarnContext(ctx, "internal api: emit user.deleted", "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

type PackImages interface {
	OpenPackImage(ctx context.Context, id uuid.UUID) (media.Download, error)
}

func (a actions) packImage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("attachment_id"))
	if err != nil {
		invalid(w, "attachment_id", "not an attachment id")
		return
	}
	d, err := a.images.OpenPackImage(r.Context(), id)
	if errors.Is(err, media.ErrNoAccess) {
		writeJSON(w, http.StatusNotFound, problem{Error: "not_found"})
		return
	}
	if err != nil {
		a.fail(w, r, "pack image", err)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if d.RedirectURL != "" {
		http.Redirect(w, r, d.RedirectURL, http.StatusFound)
		return
	}
	defer func() { _ = d.Object.Body.Close() }()
	h.Set("Content-Type", d.ContentType)
	if d.Object.Size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(d.Object.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, d.Object.Body)
}
