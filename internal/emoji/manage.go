package emoji

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func (s *Service) Add(ctx context.Context, userID, packID int64, attachmentID uuid.UUID, shortcode string) (postgres.CustomEmoji, error) {
	if err := checkShortcode(shortcode); err != nil {
		return postgres.CustomEmoji{}, err
	}
	p, err := s.editable(ctx, userID, packID)
	if err != nil {
		return postgres.CustomEmoji{}, err
	}
	a, err := s.store.Attachments().Get(ctx, attachmentID)
	if errors.Is(err, postgres.ErrNotFound) || (err == nil && (a.UploaderID == nil || *a.UploaderID != userID || a.Purpose != postgres.PurposeEmoji)) {
		return postgres.CustomEmoji{}, invalid("attachment_id", "not one of your emoji uploads")
	}
	if err != nil {
		return postgres.CustomEmoji{}, storeError(err)
	}
	limit := MaxPackEmoji
	if p.ChatID != nil {
		limit = MaxGroupEmoji
	}
	e, err := s.store.Emoji().AddEmoji(ctx, packID, attachmentID, shortcode, a.MimeType == "image/gif", limit)
	return e, storeError(err)
}

func (s *Service) RenameEmoji(ctx context.Context, userID, packID, emojiID int64, shortcode string) (postgres.CustomEmoji, error) {
	if err := checkShortcode(shortcode); err != nil {
		return postgres.CustomEmoji{}, err
	}
	if _, err := s.editable(ctx, userID, packID); err != nil {
		return postgres.CustomEmoji{}, err
	}
	e, err := s.store.Emoji().RenameEmoji(ctx, packID, emojiID, shortcode)
	return e, storeError(err)
}

func (s *Service) DeleteEmoji(ctx context.Context, userID, packID, emojiID int64) error {
	if _, err := s.editable(ctx, userID, packID); err != nil {
		return err
	}
	return storeError(s.store.Emoji().DeleteEmoji(ctx, packID, emojiID))
}

func (s *Service) Report(ctx context.Context, userID, packID int64, reason string) error {
	if !ReportReasons[reason] {
		return invalid("reason", "unknown reason")
	}
	stored, err := s.store.Emoji().Pack(ctx, packID)
	if err != nil {
		return storeError(err)
	}
	p, err := s.open(ctx, userID, stored, true)
	if err != nil {
		return err
	}
	if p.ChatID != nil || p.IsMine {
		return ErrForbidden
	}
	err = s.store.Emoji().Report(ctx, postgres.EmojiReport{
		PackID: packID, ReporterID: userID, Reason: reason, Since: time.Now().Add(-24 * time.Hour),
		MaxReports: MaxReportsPerDay, HideAfter: HideAfterReports,
	})
	if errors.Is(err, postgres.ErrLimit) {
		return ErrRateLimited
	}
	return storeError(err)
}

func (s *Service) GroupPack(ctx context.Context, userID, chatID int64) (Pack, error) {
	chat, err := s.store.Chats().Get(ctx, chatID)
	if err != nil {
		return Pack{}, storeError(err)
	}
	if chat.Type != postgres.ChatGroup {
		return Pack{}, ErrNotFound
	}
	if _, member, err := s.groupRole(ctx, chatID, userID); err != nil || !member {
		if err == nil {
			err = ErrNotFound
		}
		return Pack{}, err
	}
	title := "Group emoji"
	if chat.Name != nil {
		title = *chat.Name
	}
	p, err := s.store.Emoji().GroupPack(ctx, chatID, title)
	if err != nil {
		return Pack{}, storeError(err)
	}
	return s.open(ctx, userID, p, false)
}

func (s *Service) Emoji(ctx context.Context, id int64) (postgres.EmojiImage, error) {
	e, err := s.store.Emoji().Emoji(ctx, id)
	return e, storeError(err)
}

func (s *Service) Clean(ctx context.Context, userID int64, text string) (string, error) {
	ids := IDs(text)
	if len(ids) == 0 {
		return text, nil
	}
	usable, err := s.store.Emoji().Usable(ctx, userID, ids)
	if err != nil {
		return "", storeError(err)
	}
	return Rewrite(text, func(t Token) string {
		if e, ok := usable[t.ID]; ok {
			return Format(e.Shortcode, e.ID, e.IsAnimated)
		}
		return Fallback(t.Shortcode)
	}), nil
}

func (s *Service) Allowed(ctx context.Context, userID, emojiID int64) (bool, error) {
	usable, err := s.store.Emoji().Usable(ctx, userID, []int64{emojiID})
	if err != nil {
		return false, storeError(err)
	}
	_, ok := usable[emojiID]
	return ok, nil
}
