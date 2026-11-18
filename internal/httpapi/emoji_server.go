package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/emoji"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type EmojiImages interface {
	OpenUnchecked(ctx context.Context, id uuid.UUID, v media.Variant) (media.Download, error)
}

type EmojiOptions struct {
	Service  *emoji.Service
	Images   EmojiImages
	BasePath string
	Log      *slog.Logger
}

type EmojiServer struct {
	o EmojiOptions
}

func NewEmojiServer(o EmojiOptions) *EmojiServer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &EmojiServer{o: o}
}

func (s *EmojiServer) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	var field *emoji.FieldError
	switch {
	case errors.As(err, &field):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: field.Field, Message: field.Message})
	case errors.Is(err, emoji.ErrNotFound):
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
	case errors.Is(err, emoji.ErrForbidden):
		WriteProblem(w, http.StatusForbidden, ErrorCodeForbidden)
	case errors.Is(err, emoji.ErrLimit):
		WriteProblem(w, http.StatusConflict, ErrorCodeLimitReached)
	case errors.Is(err, emoji.ErrConflict):
		WriteProblem(w, http.StatusConflict, ErrorCodeConflict, ProblemError{Field: "shortcode", Message: "already used in this pack"})
	case errors.Is(err, emoji.ErrRateLimited):
		w.Header().Set("Retry-After", "3600")
		WriteProblem(w, http.StatusTooManyRequests, ErrorCodeRateLimited)
	case errors.Is(err, emoji.ErrUnavailable):
		s.o.Log.WarnContext(r.Context(), what, "error", err)
		w.Header().Set("Retry-After", "2")
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
	default:
		s.o.Log.ErrorContext(r.Context(), what, "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
	}
}

func EmojiImagePath(basePath string, id int64) string {
	return basePath + "/emoji/items/" + strconv.FormatInt(id, 10) + "/image"
}

func (s *EmojiServer) presentEmoji(e postgres.CustomEmoji, slug *string) CustomEmoji {
	return CustomEmoji{
		Id: FormatID(e.ID), PackId: FormatID(e.PackID), Shortcode: e.Shortcode, IsAnimated: e.IsAnimated,
		Url: EmojiImagePath(s.o.BasePath, e.ID), PackSlug: slug,
	}
}

func (s *EmojiServer) presentPack(p emoji.Pack) EmojiPack {
	out := EmojiPack{
		Id: FormatID(p.ID), Title: p.Title, Version: int(p.Version), Slug: p.Slug, ChatId: formatIDPtr(p.ChatID),
		OwnerId: formatIDPtr(p.OwnerID), IsMine: p.IsMine, IsInstalled: p.Installed || p.IsMine || p.ChatID != nil,
		IsHidden: p.Hidden, CanEdit: p.CanEdit, Emoji: make([]CustomEmoji, 0, len(p.Emoji)),
	}
	for _, e := range p.Emoji {
		out.Emoji = append(out.Emoji, s.presentEmoji(e, p.Slug))
	}
	return out
}

func (s *EmojiServer) pack(w http.ResponseWriter, r *http.Request, status int, what string, load func(userID int64) (emoji.Pack, error)) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	pack, err := load(p.UserID)
	if err != nil {
		s.fail(w, r, what, err)
		return
	}
	noStore(w)
	writeJSONStatus(w, status, s.presentPack(pack))
}

func (s *EmojiServer) done(w http.ResponseWriter, r *http.Request, what string, run func(userID int64) error) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if err := run(p.UserID); err != nil {
		s.fail(w, r, what, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *EmojiServer) ListEmojiPacks(w http.ResponseWriter, r *http.Request, _ ListEmojiPacksParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	packs, err := s.o.Service.Packs(r.Context(), p.UserID)
	if err != nil {
		s.fail(w, r, "list emoji packs", err)
		return
	}
	out := EmojiPackList{Items: make([]EmojiPack, 0, len(packs))}
	for _, pack := range packs {
		out.Items = append(out.Items, s.presentPack(pack))
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (s *EmojiServer) CreateEmojiPack(w http.ResponseWriter, r *http.Request, _ CreateEmojiPackParams) {
	var body EmojiPackTitleRequest
	if !decode(w, r, &body) {
		return
	}
	s.pack(w, r, http.StatusCreated, "create emoji pack", func(userID int64) (emoji.Pack, error) {
		return s.o.Service.Create(r.Context(), userID, body.Title)
	})
}

func (s *EmojiServer) GetEmojiPack(w http.ResponseWriter, r *http.Request, packID EmojiPackId, _ GetEmojiPackParams) {
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	s.pack(w, r, http.StatusOK, "get emoji pack", func(userID int64) (emoji.Pack, error) {
		return s.o.Service.Pack(r.Context(), userID, id)
	})
}

func (s *EmojiServer) GetEmojiPackByLink(w http.ResponseWriter, r *http.Request, slug StickerPackSlug, _ GetEmojiPackByLinkParams) {
	s.pack(w, r, http.StatusOK, "get emoji pack by link", func(userID int64) (emoji.Pack, error) {
		return s.o.Service.PackByLink(r.Context(), userID, slug)
	})
}

func (s *EmojiServer) GetGroupEmoji(w http.ResponseWriter, r *http.Request, chatID ChatId, _ GetGroupEmojiParams) {
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	s.pack(w, r, http.StatusOK, "get group emoji", func(userID int64) (emoji.Pack, error) {
		return s.o.Service.GroupPack(r.Context(), userID, id)
	})
}

func (s *EmojiServer) UpdateEmojiPack(w http.ResponseWriter, r *http.Request, packID EmojiPackId, _ UpdateEmojiPackParams) {
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	var body EmojiPackTitleRequest
	if !decode(w, r, &body) {
		return
	}
	s.pack(w, r, http.StatusOK, "rename emoji pack", func(userID int64) (emoji.Pack, error) {
		return s.o.Service.Rename(r.Context(), userID, id, body.Title)
	})
}

func (s *EmojiServer) DeleteEmojiPack(w http.ResponseWriter, r *http.Request, packID EmojiPackId, _ DeleteEmojiPackParams) {
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	s.done(w, r, "delete emoji pack", func(userID int64) error { return s.o.Service.Delete(r.Context(), userID, id) })
}

func (s *EmojiServer) AddEmojiPack(w http.ResponseWriter, r *http.Request, packID EmojiPackId, _ AddEmojiPackParams) {
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	s.done(w, r, "add emoji pack", func(userID int64) error { return s.o.Service.SetInstalled(r.Context(), userID, id, true) })
}

func (s *EmojiServer) RemoveEmojiPack(w http.ResponseWriter, r *http.Request, packID EmojiPackId, _ RemoveEmojiPackParams) {
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	s.done(w, r, "remove emoji pack", func(userID int64) error { return s.o.Service.SetInstalled(r.Context(), userID, id, false) })
}

func (s *EmojiServer) ReportEmojiPack(w http.ResponseWriter, r *http.Request, packID EmojiPackId, _ ReportEmojiPackParams) {
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	var body ReportStickerPackRequest
	if !decode(w, r, &body) {
		return
	}
	s.done(w, r, "report emoji pack", func(userID int64) error {
		return s.o.Service.Report(r.Context(), userID, id, string(body.Reason))
	})
}

func (s *EmojiServer) AddCustomEmoji(w http.ResponseWriter, r *http.Request, packID EmojiPackId, _ AddCustomEmojiParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	var body AddCustomEmojiRequest
	if !decode(w, r, &body) {
		return
	}
	e, err := s.o.Service.Add(r.Context(), p.UserID, id, body.AttachmentId, body.Shortcode)
	if err != nil {
		s.fail(w, r, "add emoji", err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, s.presentEmoji(e, nil))
}

func (s *EmojiServer) UpdateCustomEmoji(w http.ResponseWriter, r *http.Request, packID EmojiPackId, emojiID EmojiId, _ UpdateCustomEmojiParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	pack, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	id, ok := parseID(w, "emoji_id", emojiID)
	if !ok {
		return
	}
	var body UpdateCustomEmojiRequest
	if !decode(w, r, &body) {
		return
	}
	e, err := s.o.Service.RenameEmoji(r.Context(), p.UserID, pack, id, body.Shortcode)
	if err != nil {
		s.fail(w, r, "rename emoji", err)
		return
	}
	writeJSONStatus(w, http.StatusOK, s.presentEmoji(e, nil))
}

func (s *EmojiServer) DeleteCustomEmoji(w http.ResponseWriter, r *http.Request, packID EmojiPackId, emojiID EmojiId, _ DeleteCustomEmojiParams) {
	pack, ok := parseID(w, "pack_id", packID)
	if !ok {
		return
	}
	id, ok := parseID(w, "emoji_id", emojiID)
	if !ok {
		return
	}
	s.done(w, r, "delete emoji", func(userID int64) error { return s.o.Service.DeleteEmoji(r.Context(), userID, pack, id) })
}

func (s *EmojiServer) liveEmoji(w http.ResponseWriter, r *http.Request, emojiID EmojiId) (postgres.EmojiImage, bool) {
	if _, ok := principalOr401(w, r); !ok {
		return postgres.EmojiImage{}, false
	}
	id, ok := parseID(w, "emoji_id", emojiID)
	if !ok {
		return postgres.EmojiImage{}, false
	}
	e, err := s.o.Service.Emoji(r.Context(), id)
	if err == nil && e.Deleted {
		err = emoji.ErrNotFound
	}
	if err != nil {
		s.fail(w, r, "get emoji", err)
		return postgres.EmojiImage{}, false
	}
	return e, true
}

func (s *EmojiServer) GetCustomEmoji(w http.ResponseWriter, r *http.Request, emojiID EmojiId, _ GetCustomEmojiParams) {
	e, ok := s.liveEmoji(w, r, emojiID)
	if !ok {
		return
	}
	writeJSONCached(w, r, s.presentEmoji(e.CustomEmoji, e.PackSlug))
}

func (s *EmojiServer) GetCustomEmojiImage(w http.ResponseWriter, r *http.Request, emojiID EmojiId, params GetCustomEmojiImageParams) {
	e, ok := s.liveEmoji(w, r, emojiID)
	if !ok {
		return
	}
	variant, suffix := media.VariantOriginal, ""
	if params.Variant != nil && *params.Variant == GetCustomEmojiImageParamsVariantInline {
		variant, suffix = media.VariantInline, "-inline"
	}
	d, err := s.o.Images.OpenUnchecked(r.Context(), e.AttachmentID, variant)
	if err != nil {
		s.fail(w, r, "open emoji image", err)
		return
	}
	serveDownload(w, r, d, cacheForever, "emoji-"+strconv.FormatInt(e.ID, 10)+suffix)
}
