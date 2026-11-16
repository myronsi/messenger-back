package emoji

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	MaxPackEmoji     = 200
	MaxGroupEmoji    = 50
	MaxPacks         = 100
	MaxTitle         = 64
	MaxReportsPerDay = 20
	HideAfterReports = 5
	slugLength       = 12
)

var (
	ErrNotFound    = errors.New("emoji pack not found")
	ErrForbidden   = errors.New("not allowed to change this emoji pack")
	ErrLimit       = errors.New("emoji limit reached")
	ErrInvalid     = errors.New("invalid emoji request")
	ErrConflict    = errors.New("shortcode already used in this pack")
	ErrRateLimited = errors.New("too many emoji pack reports")
	ErrUnavailable = errors.New("temporarily unavailable")
)

var ReportReasons = map[string]bool{"spam": true, "violence": true, "sexual": true, "hate": true, "copyright": true, "other": true}

type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

func (e *FieldError) Unwrap() error { return ErrInvalid }

func invalid(field, message string) error { return &FieldError{Field: field, Message: message} }

type Store interface {
	Emoji() postgres.EmojiRepository
	Attachments() postgres.AttachmentRepository
	Chats() postgres.ChatRepository
}

type Pack struct {
	postgres.EmojiPack
	Emoji   []postgres.CustomEmoji
	IsMine  bool
	CanEdit bool
}

type Service struct {
	store Store
}

func New(store Store) *Service { return &Service{store: store} }

func storeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, postgres.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, postgres.ErrLimit):
		return ErrLimit
	case errors.Is(err, postgres.ErrConflict):
		return ErrConflict
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

func cleanTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	n := utf8.RuneCountInString(title)
	if n == 0 || n > MaxTitle || strings.IndexFunc(title, unicode.IsControl) >= 0 {
		return "", invalid("title", "1 to 64 characters")
	}
	return title, nil
}

func checkShortcode(shortcode string) error {
	if !ShortcodePattern.MatchString(shortcode) {
		return invalid("shortcode", "2 to 32 lowercase letters, digits or underscores")
	}
	return nil
}

func isOwner(p postgres.EmojiPack, userID int64) bool {
	return p.OwnerID != nil && *p.OwnerID == userID
}

func (s *Service) groupRole(ctx context.Context, chatID, userID int64) (postgres.Role, bool, error) {
	part, err := s.store.Chats().Participant(ctx, chatID, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, storeError(err)
	}
	return part.Role, true, nil
}

func (s *Service) access(ctx context.Context, userID int64, p postgres.EmojiPack) (visible, canEdit bool, err error) {
	if p.Deleted {
		return false, false, nil
	}
	if p.ChatID != nil {
		role, member, err := s.groupRole(ctx, *p.ChatID, userID)
		if err != nil || !member {
			return false, false, err
		}
		return true, role == postgres.RoleOwner || role == postgres.RoleAdmin, nil
	}
	if isOwner(p, userID) {
		return true, true, nil
	}
	return !p.Hidden, false, nil
}

func (s *Service) withEmoji(ctx context.Context, userID int64, packs []postgres.EmojiPack, canEdit map[int64]bool) ([]Pack, error) {
	ids := make([]int64, len(packs))
	for i, p := range packs {
		ids[i] = p.ID
	}
	items, err := s.store.Emoji().PackEmoji(ctx, ids)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]Pack, len(packs))
	for i, p := range packs {
		out[i] = Pack{EmojiPack: p, Emoji: items[p.ID], IsMine: isOwner(p, userID), CanEdit: canEdit[p.ID]}
	}
	return out, nil
}

func (s *Service) open(ctx context.Context, userID int64, p postgres.EmojiPack, byLink bool) (Pack, error) {
	visible, canEdit, err := s.access(ctx, userID, p)
	if err != nil {
		return Pack{}, err
	}
	if !visible {
		return Pack{}, ErrNotFound
	}
	if p.ChatID == nil && !isOwner(p, userID) {
		if p.Installed, err = s.store.Emoji().Installed(ctx, userID, p.ID); err != nil {
			return Pack{}, storeError(err)
		}
		if !p.Installed && !byLink {
			return Pack{}, ErrNotFound
		}
	}
	packs, err := s.withEmoji(ctx, userID, []postgres.EmojiPack{p}, map[int64]bool{p.ID: canEdit})
	if err != nil {
		return Pack{}, err
	}
	return packs[0], nil
}

func (s *Service) Packs(ctx context.Context, userID int64) ([]Pack, error) {
	packs, err := s.store.Emoji().UserPacks(ctx, userID, MaxPacks)
	if err != nil {
		return nil, storeError(err)
	}
	canEdit := map[int64]bool{}
	for _, p := range packs {
		_, edit, err := s.access(ctx, userID, p)
		if err != nil {
			return nil, err
		}
		canEdit[p.ID] = edit
	}
	return s.withEmoji(ctx, userID, packs, canEdit)
}

func (s *Service) Pack(ctx context.Context, userID, packID int64) (Pack, error) {
	p, err := s.store.Emoji().Pack(ctx, packID)
	if err != nil {
		return Pack{}, storeError(err)
	}
	return s.open(ctx, userID, p, false)
}

func (s *Service) PackByLink(ctx context.Context, userID int64, slug string) (Pack, error) {
	p, err := s.store.Emoji().PackBySlug(ctx, slug)
	if err != nil {
		return Pack{}, storeError(err)
	}
	return s.open(ctx, userID, p, true)
}

func (s *Service) Create(ctx context.Context, userID int64, title string) (Pack, error) {
	title, err := cleanTitle(title)
	if err != nil {
		return Pack{}, err
	}
	p, err := s.store.Emoji().CreatePack(ctx, userID, title, rand.Text()[:slugLength])
	if err != nil {
		return Pack{}, storeError(err)
	}
	return Pack{EmojiPack: p, Emoji: []postgres.CustomEmoji{}, IsMine: true, CanEdit: true}, nil
}

func (s *Service) editable(ctx context.Context, userID, packID int64) (postgres.EmojiPack, error) {
	p, err := s.store.Emoji().Pack(ctx, packID)
	if err != nil {
		return p, storeError(err)
	}
	visible, canEdit, err := s.access(ctx, userID, p)
	if err != nil {
		return p, err
	}
	if !visible || (p.ChatID == nil && !canEdit) {
		return p, ErrNotFound
	}
	if !canEdit {
		return p, ErrForbidden
	}
	return p, nil
}

func (s *Service) Rename(ctx context.Context, userID, packID int64, title string) (Pack, error) {
	title, err := cleanTitle(title)
	if err != nil {
		return Pack{}, err
	}
	p, err := s.editable(ctx, userID, packID)
	if err != nil {
		return Pack{}, err
	}
	if p.ChatID != nil {
		return Pack{}, ErrForbidden
	}
	if err := s.store.Emoji().RenamePack(ctx, packID, title); err != nil {
		return Pack{}, storeError(err)
	}
	return s.Pack(ctx, userID, packID)
}

func (s *Service) Delete(ctx context.Context, userID, packID int64) error {
	p, err := s.editable(ctx, userID, packID)
	if err != nil {
		return err
	}
	if p.ChatID != nil {
		return ErrForbidden
	}
	return storeError(s.store.Emoji().DeletePack(ctx, packID))
}

func (s *Service) SetInstalled(ctx context.Context, userID, packID int64, installed bool) error {
	stored, err := s.store.Emoji().Pack(ctx, packID)
	if err != nil {
		return storeError(err)
	}
	p, err := s.open(ctx, userID, stored, true)
	if err != nil {
		return err
	}
	if p.ChatID != nil || p.IsMine {
		return nil
	}
	return storeError(s.store.Emoji().SetInstalled(ctx, userID, packID, installed))
}
