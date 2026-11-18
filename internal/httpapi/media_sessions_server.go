package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

var RateUploadChunk = redis.Rate{Name: "upload_chunk", Rate: 240, Period: time.Minute, Burst: 60}

func contentVariant(v *GetAttachmentContentParamsVariant) (media.Variant, string) {
	if v == nil {
		return media.VariantOriginal, ""
	}
	switch *v {
	case GetAttachmentContentParamsVariantThumbnail:
		return media.VariantThumbnail, "-thumb"
	case GetAttachmentContentParamsVariantDisplay:
		return media.VariantDisplay, "-display"
	case GetAttachmentContentParamsVariantSource:
		return media.VariantSource, "-source"
	case GetAttachmentContentParamsVariantPanel:
		return media.VariantPanel, ""
	case GetAttachmentContentParamsVariantMessage:
		return media.VariantMessage, ""
	case GetAttachmentContentParamsVariantInline:
		return media.VariantInline, ""
	case GetAttachmentContentParamsVariantAnimation:
		return media.VariantAnimation, ""
	case GetAttachmentContentParamsVariantAvatar64:
		return media.VariantAvatar64, ""
	case GetAttachmentContentParamsVariantAvatar128:
		return media.VariantAvatar128, ""
	case GetAttachmentContentParamsVariantAvatar256:
		return media.VariantAvatar256, ""
	}
	return media.VariantOriginal, ""
}

func (m *MediaServer) CreateAttachmentPlayback(w http.ResponseWriter, r *http.Request, id AttachmentId, params CreateAttachmentPlaybackParams) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
		return
	}
	if m.o.Playback == nil {
		notImplemented(w, r)
		return
	}
	if _, err := m.o.Service.Authorize(r.Context(), p.UserID, id); err != nil {
		if errors.Is(err, media.ErrNoAccess) {
			WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
			return
		}
		m.o.Log.ErrorContext(r.Context(), "authorize playback", "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
		return
	}
	token, expires := m.o.Playback.Sign(id, p.UserID)
	url := m.o.BasePath + "/attachments/" + id.String() + "/content?pt=" + token
	if params.Variant != nil {
		url += "&variant=" + string(*params.Variant)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(w, http.StatusOK, PlaybackUrl{Url: url, ExpiresAt: expires.UTC()})
}

func presentSession(s postgres.UploadSession) UploadSession {
	return UploadSession{
		Id: s.ID, Size: s.Size, ChunkSize: s.ChunkSize, Received: s.Received,
		ExpiresAt: s.UpdatedAt.Add(media.UploadSessionTTL).UTC(),
	}
}

func (m *MediaServer) sessionFailed(w http.ResponseWriter, r *http.Request, sess postgres.UploadSession, err error) {
	switch {
	case errors.Is(err, media.ErrNoAccess):
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
	case errors.Is(err, media.ErrUploadOffset), errors.Is(err, media.ErrUploadIncomplete):
		writeJSONStatus(w, http.StatusConflict, presentSession(sess))
	case errors.Is(err, media.ErrTooManyUploads):
		WriteProblem(w, http.StatusTooManyRequests, ErrorCodeLimitReached)
	default:
		m.uploadFailed(w, r, err)
	}
}

func (m *MediaServer) CreateUploadSession(w http.ResponseWriter, r *http.Request, _ CreateUploadSessionParams) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
		return
	}
	var body CreateUploadSessionRequest
	if !decode(w, r, &body) {
		return
	}
	if !allow(w, r, m.o.Limiter, RateUpload, strconv.FormatInt(p.UserID, 10), m.o.Log) {
		return
	}
	kind := ""
	if body.Kind != nil {
		kind = string(*body.Kind)
	}
	sess, err := m.o.Service.CreateSession(r.Context(), media.NewSession{
		UploaderID: p.UserID, Purpose: string(body.Purpose), Kind: kind, Filename: body.Filename, Size: body.Size,
	})
	if err != nil {
		m.sessionFailed(w, r, sess, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, presentSession(sess))
}

func (m *MediaServer) GetUploadSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, _ GetUploadSessionParams) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
		return
	}
	sess, err := m.o.Service.GetSession(r.Context(), id, p.UserID)
	if err != nil {
		m.sessionFailed(w, r, sess, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(w, http.StatusOK, presentSession(sess))
}

func (m *MediaServer) DeleteUploadSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, _ DeleteUploadSessionParams) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
		return
	}
	if err := m.o.Service.CancelSession(r.Context(), id, p.UserID); err != nil {
		m.sessionFailed(w, r, postgres.UploadSession{}, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *MediaServer) PutUploadChunk(w http.ResponseWriter, r *http.Request, id uuid.UUID, params PutUploadChunkParams) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
		return
	}
	if !allow(w, r, m.o.Limiter, RateUploadChunk, strconv.FormatInt(p.UserID, 10), m.o.Log) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, media.ChunkSize+1)
	sess, err := m.o.Service.PutChunk(r.Context(), id, p.UserID, params.Offset, r.Body)
	if err != nil {
		m.sessionFailed(w, r, sess, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, presentSession(sess))
}

func (m *MediaServer) CompleteUploadSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, _ CompleteUploadSessionParams) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
		return
	}
	if !allow(w, r, m.o.Limiter, RateUpload, strconv.FormatInt(p.UserID, 10), m.o.Log) {
		return
	}
	a, tags, err := m.o.Service.CompleteSession(r.Context(), id, p.UserID)
	if err != nil {
		sess, _ := m.o.Service.GetSession(r.Context(), id, p.UserID)
		m.sessionFailed(w, r, sess, err)
		return
	}
	out := PresentAttachment(a, "", m.o.BasePath)
	if out.Tags == nil && (tags.Title != nil || tags.Artist != nil) {
		out.Tags = &AudioTags{Title: tags.Title, Artist: tags.Artist, Album: tags.Album}
	}
	writeJSONStatus(w, http.StatusCreated, out)
}

const chunkBodyLimit = media.ChunkSize + 64<<10
