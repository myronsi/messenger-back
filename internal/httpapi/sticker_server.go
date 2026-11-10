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
	switch {
	case errors.Is(err, stickers.ErrNotFound):
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
	case errors.Is(err, stickers.ErrUnavailable):
		s.o.Log.WarnContext(r.Context(), what, "error", err)
		w.Header().Set("Retry-After", "2")
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
	default:
		s.o.Log.ErrorContext(r.Context(), what, "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
	}
}

func (s *StickerServer) presentPack(p stickers.Pack) StickerPack {
	out := StickerPack{
		Id: FormatID(p.ID), Title: p.Title, IsOfficial: p.BuiltinKey != nil, IsInstalled: p.Installed,
		Stickers: make([]Sticker, 0, len(p.Stickers)),
	}
	for _, st := range p.Stickers {
		out.Stickers = append(out.Stickers, PresentSticker(st, s.o.BasePath))
	}
	return out
}

func (s *StickerServer) ListStickerPacks(w http.ResponseWriter, r *http.Request, _ ListStickerPacksParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	packs, err := s.o.Service.Installed(r.Context(), p.UserID)
	if err != nil {
		s.fail(w, r, "list sticker packs", err)
		return
	}
	out := StickerPackList{Items: make([]StickerPack, 0, len(packs))}
	for _, pack := range packs {
		out.Items = append(out.Items, s.presentPack(pack))
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
	writeJSONStatus(w, http.StatusOK, s.presentPack(pack))
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
