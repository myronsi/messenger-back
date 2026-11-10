package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/myronsi/messenger-back/internal/stickers"
)

type StickerOptions struct {
	Service  *stickers.Service
	BasePath string
	Log      *slog.Logger
}

type StickerServer struct {
	o StickerOptions
}

func NewStickerServer(o StickerOptions) *StickerServer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &StickerServer{o: o}
}

func (s *StickerServer) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	var field *stickers.FieldError
	switch {
	case errors.As(err, &field):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: field.Field, Message: field.Message})
	case errors.Is(err, stickers.ErrNotFound):
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
	case errors.Is(err, stickers.ErrForbidden):
		WriteProblem(w, http.StatusForbidden, ErrorCodeForbidden)
	case errors.Is(err, stickers.ErrLimit):
		WriteProblem(w, http.StatusConflict, ErrorCodeLimitReached)
	case errors.Is(err, stickers.ErrRateLimited):
		w.Header().Set("Retry-After", "3600")
		WriteProblem(w, http.StatusTooManyRequests, ErrorCodeRateLimited)
	case errors.Is(err, stickers.ErrUnavailable):
		s.o.Log.WarnContext(r.Context(), what, "error", err)
		w.Header().Set("Retry-After", "2")
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
	default:
		s.o.Log.ErrorContext(r.Context(), what, "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
	}
}

func (s *StickerServer) presentPack(p stickers.Pack, viewerID int64) StickerPack {
	out := StickerPack{
		Id: FormatID(p.ID), Title: p.Title, IsOfficial: p.BuiltinKey != nil, IsInstalled: p.Installed,
		IsMine: stickers.IsMine(p.StickerPack, viewerID), IsHidden: p.Hidden, OwnerId: formatIDPtr(p.OwnerID),
		Slug: p.Slug, Stickers: make([]Sticker, 0, len(p.Stickers)),
	}
	for _, st := range p.Stickers {
		out.Stickers = append(out.Stickers, PresentSticker(st, s.o.BasePath))
	}
	return out
}

func (s *StickerServer) ListStickerPacks(w http.ResponseWriter, r *http.Request, params ListStickerPacksParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	list := s.o.Service.Installed
	if params.Owned != nil && *params.Owned {
		list = s.o.Service.Owned
	}
	packs, err := list(r.Context(), p.UserID)
	if err != nil {
		s.fail(w, r, "list sticker packs", err)
		return
	}
	out := StickerPackList{Items: make([]StickerPack, 0, len(packs))}
	for _, pack := range packs {
		out.Items = append(out.Items, s.presentPack(pack, p.UserID))
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (s *StickerServer) GetStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, _ GetStickerPackParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	pack, err := s.o.Service.Pack(r.Context(), p.UserID, id)
	if err != nil {
		s.fail(w, r, "get sticker pack", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, s.presentPack(pack, p.UserID))
}

func (s *StickerServer) setInstalled(w http.ResponseWriter, r *http.Request, packID StickerPackId, installed bool) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	if err := s.o.Service.SetInstalled(r.Context(), p.UserID, id, installed); err != nil {
		s.fail(w, r, "install sticker pack", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *StickerServer) AddStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, _ AddStickerPackParams) {
	s.setInstalled(w, r, packID, true)
}

func (s *StickerServer) RemoveStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, _ RemoveStickerPackParams) {
	s.setInstalled(w, r, packID, false)
}

func (s *StickerServer) ListRecentStickers(w http.ResponseWriter, r *http.Request, _ ListRecentStickersParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	recent, err := s.o.Service.Recent(r.Context(), p.UserID)
	if err != nil {
		s.fail(w, r, "recent stickers", err)
		return
	}
	out := StickerList{Items: make([]Sticker, 0, len(recent))}
	for _, st := range recent {
		out.Items = append(out.Items, PresentSticker(st, s.o.BasePath))
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (s *StickerServer) GetStickerPackByLink(w http.ResponseWriter, r *http.Request, slug StickerPackSlug, _ GetStickerPackByLinkParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	pack, err := s.o.Service.PackByLink(r.Context(), p.UserID, slug)
	if err != nil {
		s.fail(w, r, "sticker pack by link", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, s.presentPack(pack, p.UserID))
}

func (s *StickerServer) CreateStickerPack(w http.ResponseWriter, r *http.Request, _ CreateStickerPackParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	var body CreateStickerPackRequest
	if !decode(w, r, &body) {
		return
	}
	pack, err := s.o.Service.Create(r.Context(), p.UserID, body.Title)
	if err != nil {
		s.fail(w, r, "create sticker pack", err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, s.presentPack(pack, p.UserID))
}

func (s *StickerServer) UpdateStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, _ UpdateStickerPackParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	var body UpdateStickerPackRequest
	if !decode(w, r, &body) {
		return
	}
	pack, err := s.o.Service.Rename(r.Context(), p.UserID, id, body.Title)
	if err != nil {
		s.fail(w, r, "rename sticker pack", err)
		return
	}
	writeJSONStatus(w, http.StatusOK, s.presentPack(pack, p.UserID))
}

func (s *StickerServer) DeleteStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, _ DeleteStickerPackParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	if err := s.o.Service.Delete(r.Context(), p.UserID, id); err != nil {
		s.fail(w, r, "delete sticker pack", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *StickerServer) AddSticker(w http.ResponseWriter, r *http.Request, packID StickerPackId, _ AddStickerParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	var body AddStickerRequest
	if !decode(w, r, &body) {
		return
	}
	st, err := s.o.Service.AddSticker(r.Context(), p.UserID, id, body.AttachmentId, body.Emoji)
	if err != nil {
		s.fail(w, r, "add sticker", err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, PresentSticker(st, s.o.BasePath))
}

func (s *StickerServer) stickerParams(w http.ResponseWriter, r *http.Request, packID StickerPackId, stickerID StickerId) (int64, int64, int64, bool) {
	p, ok := principalOr401(w, r)
	if !ok {
		return 0, 0, 0, false
	}
	pack, ok := parseID(w, "pack_id", packID)
	if !ok {
		return 0, 0, 0, false
	}
	st, ok := parseID(w, "sticker_id", stickerID)
	return p.UserID, pack, st, ok
}

func (s *StickerServer) UpdateSticker(w http.ResponseWriter, r *http.Request, packID StickerPackId, stickerID StickerId, _ UpdateStickerParams) {
	user, pack, id, ok := s.stickerParams(w, r, packID, stickerID)
	if !ok {
		return
	}
	var body UpdateStickerRequest
	if !decode(w, r, &body) {
		return
	}
	st, err := s.o.Service.SetEmoji(r.Context(), user, pack, id, body.Emoji)
	if err != nil {
		s.fail(w, r, "update sticker", err)
		return
	}
	writeJSONStatus(w, http.StatusOK, PresentSticker(st, s.o.BasePath))
}

func (s *StickerServer) DeleteSticker(w http.ResponseWriter, r *http.Request, packID StickerPackId, stickerID StickerId, _ DeleteStickerParams) {
	user, pack, id, ok := s.stickerParams(w, r, packID, stickerID)
	if !ok {
		return
	}
	if err := s.o.Service.DeleteSticker(r.Context(), user, pack, id); err != nil {
		s.fail(w, r, "delete sticker", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *StickerServer) ReorderStickers(w http.ResponseWriter, r *http.Request, packID StickerPackId, _ ReorderStickersParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	var body ReorderStickersRequest
	if !decode(w, r, &body) {
		return
	}
	if len(body.StickerIds) > stickers.MaxPackStickers {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "sticker_ids", Message: "at most 120 stickers"})
		return
	}
	ids, ok := parseIDs(w, "sticker_ids", body.StickerIds)
	if !ok {
		return
	}
	if err := s.o.Service.Reorder(r.Context(), p.UserID, id, ids); err != nil {
		s.fail(w, r, "reorder stickers", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *StickerServer) ReportStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, _ ReportStickerPackParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	var body ReportStickerPackRequest
	if !decode(w, r, &body) {
		return
	}
	if err := s.o.Service.Report(r.Context(), p.UserID, id, string(body.Reason)); err != nil {
		s.fail(w, r, "report sticker pack", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
