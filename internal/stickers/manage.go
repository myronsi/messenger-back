package stickers

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	MaxOwnPacks      = 50
	MaxTitle         = 64
	MaxReportsPerDay = 20
	HideAfterReports = 5
	slugLength       = 12
)

var (
	ErrForbidden   = errors.New("not the owner of the sticker pack")
	ErrLimit       = errors.New("sticker limit reached")
	ErrInvalid     = errors.New("invalid sticker request")
	ErrRateLimited = errors.New("too many sticker pack reports")
)

var ReportReasons = map[string]bool{"spam": true, "violence": true, "sexual": true, "hate": true, "copyright": true, "other": true}

type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

func (e *FieldError) Unwrap() error { return ErrInvalid }

func invalid(field, message string) error { return &FieldError{Field: field, Message: message} }

func storeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, postgres.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, postgres.ErrForbidden):
		return ErrForbidden
	case errors.Is(err, postgres.ErrLimit):
		return ErrLimit
	}
	return unavailable(err)
}

func cleanTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	n := utf8.RuneCountInString(title)
	if n == 0 || n > MaxTitle || strings.IndexFunc(title, unicode.IsControl) >= 0 {
		return "", invalid("title", "1 to 64 characters")
	}
	return title, nil
}

func checkEmoji(emoji string) error {
	if !messages.ValidEmoji(emoji) {
		return invalid("emoji", "one emoji")
	}
	return nil
}

func IsMine(p postgres.StickerPack, userID int64) bool {
	return p.OwnerID != nil && *p.OwnerID == userID
}

func visible(p postgres.StickerPack, userID int64) bool {
	return !p.Deleted && (!p.Hidden || IsMine(p, userID))
}

func newSlug() string {
	return rand.Text()[:slugLength]
}

func (s *Service) Owned(ctx context.Context, userID int64) ([]Pack, error) {
	packs, err := s.store.OwnedPacks(ctx, userID, MaxOwnPacks)
	if err != nil {
		return nil, unavailable(err)
	}
	installed, err := s.store.InstalledPacks(ctx, userID, MaxPacks)
	if err != nil {
		return nil, unavailable(err)
	}
	inPanel := make(map[int64]bool, len(installed))
	for _, p := range installed {
		inPanel[p.ID] = true
	}
	ids := make([]int64, len(packs))
	for i, p := range packs {
		ids[i] = p.ID
	}
	items, err := s.store.PackStickers(ctx, ids)
	if err != nil {
		return nil, unavailable(err)
	}
	out := make([]Pack, len(packs))
	for i, p := range packs {
		out[i] = Pack{StickerPack: p, Installed: inPanel[p.ID], Stickers: items[p.ID]}
	}
	return out, nil
}

func (s *Service) PackByLink(ctx context.Context, userID int64, slug string) (Pack, error) {
	p, err := s.store.PackBySlug(ctx, slug)
	if err != nil {
		return Pack{}, storeError(err)
	}
	if !visible(p, userID) {
		return Pack{}, ErrNotFound
	}
	if !IsMine(p, userID) {
		if err := s.store.Remember(ctx, userID, p.ID); err != nil {
			return Pack{}, unavailable(err)
		}
	}
	return s.Pack(ctx, userID, p.ID)
}

func (s *Service) Create(ctx context.Context, userID int64, title string) (Pack, error) {
	title, err := cleanTitle(title)
	if err != nil {
		return Pack{}, err
	}
	for range 3 {
		p, err := s.store.CreatePack(ctx, userID, title, newSlug(), MaxOwnPacks)
		if errors.Is(err, postgres.ErrConflict) {
			continue
		}
		if err != nil {
			return Pack{}, storeError(err)
		}
		return Pack{StickerPack: p, Installed: true, Stickers: []postgres.Sticker{}}, nil
	}
	return Pack{}, unavailable(errors.New("no free sticker pack link"))
}

func (s *Service) Rename(ctx context.Context, userID, packID int64, title string) (Pack, error) {
	title, err := cleanTitle(title)
	if err != nil {
		return Pack{}, err
	}
	if _, err := s.store.RenamePack(ctx, userID, packID, title); err != nil {
		return Pack{}, storeError(err)
	}
	return s.Pack(ctx, userID, packID)
}

func (s *Service) Delete(ctx context.Context, userID, packID int64) error {
	return storeError(s.store.DeletePack(ctx, userID, packID))
}

func (s *Service) sticker(ctx context.Context, id int64) (postgres.Sticker, error) {
	found, err := s.store.Stickers(ctx, []int64{id})
	if err != nil {
		return postgres.Sticker{}, unavailable(err)
	}
	st, ok := found[id]
	if !ok {
		return postgres.Sticker{}, ErrNotFound
	}
	return st, nil
}

func (s *Service) AddSticker(ctx context.Context, userID, packID int64, image uuid.UUID, emoji string) (postgres.Sticker, error) {
	if err := checkEmoji(emoji); err != nil {
		return postgres.Sticker{}, err
	}
	id, err := s.store.AddSticker(ctx, userID, packID, image, emoji, MaxPackStickers)
	if errors.Is(err, postgres.ErrInvalid) {
		return postgres.Sticker{}, invalid("attachment_id", "not a sticker image you uploaded")
	}
	if err != nil {
		return postgres.Sticker{}, storeError(err)
	}
	return s.sticker(ctx, id)
}

func (s *Service) SetEmoji(ctx context.Context, userID, packID, stickerID int64, emoji string) (postgres.Sticker, error) {
	if err := checkEmoji(emoji); err != nil {
		return postgres.Sticker{}, err
	}
	if err := s.store.SetStickerEmoji(ctx, userID, packID, stickerID, emoji); err != nil {
		return postgres.Sticker{}, storeError(err)
	}
	return s.sticker(ctx, stickerID)
}

func (s *Service) DeleteSticker(ctx context.Context, userID, packID, stickerID int64) error {
	return storeError(s.store.DeleteSticker(ctx, userID, packID, stickerID))
}

func (s *Service) Reorder(ctx context.Context, userID, packID int64, ids []int64) error {
	err := s.store.ReorderStickers(ctx, userID, packID, ids)
	if errors.Is(err, postgres.ErrInvalid) {
		return invalid("sticker_ids", "every sticker of the pack exactly once")
	}
	return storeError(err)
}

func (s *Service) Report(ctx context.Context, userID, packID int64, reason string) error {
	if !ReportReasons[reason] {
		return invalid("reason", "unknown reason")
	}
	if _, _, err := s.openPack(ctx, userID, packID); err != nil {
		return err
	}
	_, err := s.store.Report(ctx, postgres.PackReport{
		PackID: packID, ReporterID: userID, Reason: reason, Since: time.Now().Add(-24 * time.Hour),
		MaxReports: MaxReportsPerDay, HideAfter: HideAfterReports,
	})
	if errors.Is(err, postgres.ErrLimit) {
		return ErrRateLimited
	}
	return storeError(err)
}

func (s *Service) Unhide(ctx context.Context, packID int64) (bool, error) {
	ok, err := s.store.Unhide(ctx, packID)
	return ok, storeError(err)
}
