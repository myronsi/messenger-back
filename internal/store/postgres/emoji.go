package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

const PurposeEmoji = "emoji"

type EmojiPack struct {
	ID        int64
	OwnerID   *int64
	ChatID    *int64
	Title     string
	Slug      *string
	Version   int64
	Hidden    bool
	Deleted   bool
	Installed bool
}

type CustomEmoji struct {
	ID           int64
	PackID       int64
	AttachmentID uuid.UUID
	Shortcode    string
	IsAnimated   bool
	Position     int
}

type EmojiImage struct {
	CustomEmoji
	Deleted    bool
	PackSlug   *string
	StorageKey string
	MimeType   string
}

type EmojiReport struct {
	PackID     int64
	ReporterID int64
	Reason     string
	Since      time.Time
	MaxReports int
	HideAfter  int
}

type EmojiRepository interface {
	CreatePack(ctx context.Context, ownerID int64, title, slug string) (EmojiPack, error)
	GroupPack(ctx context.Context, chatID int64, title string) (EmojiPack, error)
	Pack(ctx context.Context, id int64) (EmojiPack, error)
	PackBySlug(ctx context.Context, slug string) (EmojiPack, error)
	RenamePack(ctx context.Context, id int64, title string) error
	DeletePack(ctx context.Context, id int64) error
	UserPacks(ctx context.Context, userID int64, limit int) ([]EmojiPack, error)
	Installed(ctx context.Context, userID, packID int64) (bool, error)
	SetInstalled(ctx context.Context, userID, packID int64, installed bool) error
	PackEmoji(ctx context.Context, packIDs []int64) (map[int64][]CustomEmoji, error)
	AddEmoji(ctx context.Context, packID int64, attachmentID uuid.UUID, shortcode string, animated bool, limit int) (CustomEmoji, error)
	RenameEmoji(ctx context.Context, packID, id int64, shortcode string) (CustomEmoji, error)
	DeleteEmoji(ctx context.Context, packID, id int64) error
	Emoji(ctx context.Context, id int64) (EmojiImage, error)
	Usable(ctx context.Context, userID int64, ids []int64) (map[int64]CustomEmoji, error)
	Report(ctx context.Context, r EmojiReport) error
}

type emojiRepo struct{ s *Store }

var _ EmojiRepository = emojiRepo{}

func (s *Store) Emoji() EmojiRepository { return emojiRepo{s} }

func emojiPackFrom(p sqlcdb.EmojiPack, installed bool) EmojiPack {
	return EmojiPack{
		ID: p.ID, OwnerID: p.OwnerID, ChatID: p.ChatID, Title: p.Title, Slug: p.Slug, Version: p.Version,
		Hidden: p.HiddenAt != nil, Deleted: p.DeletedAt != nil, Installed: installed,
	}
}

func customEmojiFrom(e sqlcdb.CustomEmoji) CustomEmoji {
	return CustomEmoji{ID: e.ID, PackID: e.PackID, AttachmentID: e.AttachmentID, Shortcode: e.Shortcode, IsAnimated: e.IsAnimated, Position: int(e.Position)}
}

func (r emojiRepo) CreatePack(ctx context.Context, ownerID int64, title, slug string) (EmojiPack, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p, err := r.s.q.CreateEmojiPack(ctx, sqlcdb.CreateEmojiPackParams{OwnerID: &ownerID, Title: title, Slug: &slug})
	if err != nil {
		return EmojiPack{}, mapError(err)
	}
	return emojiPackFrom(p, false), nil
}

func (r emojiRepo) GroupPack(ctx context.Context, chatID int64, title string) (EmojiPack, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	if err := r.s.q.EnsureGroupEmojiPack(ctx, sqlcdb.EnsureGroupEmojiPackParams{ChatID: &chatID, Title: title}); err != nil {
		return EmojiPack{}, mapError(err)
	}
	p, err := r.s.q.GetGroupEmojiPack(ctx, &chatID)
	if err != nil {
		return EmojiPack{}, mapError(err)
	}
	return emojiPackFrom(p, false), nil
}

func (r emojiRepo) Pack(ctx context.Context, id int64) (EmojiPack, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p, err := r.s.q.GetEmojiPack(ctx, id)
	if err != nil {
		return EmojiPack{}, mapError(err)
	}
	return emojiPackFrom(p, false), nil
}

func (r emojiRepo) PackBySlug(ctx context.Context, slug string) (EmojiPack, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p, err := r.s.q.GetEmojiPackBySlug(ctx, &slug)
	if err != nil {
		return EmojiPack{}, mapError(err)
	}
	return emojiPackFrom(p, false), nil
}

func (r emojiRepo) RenamePack(ctx context.Context, id int64, title string) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return affected(r.s.q.RenameEmojiPack(ctx, sqlcdb.RenameEmojiPackParams{ID: id, Title: title}))
}

func (r emojiRepo) DeletePack(ctx context.Context, id int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return affected(r.s.q.DeleteEmojiPack(ctx, id))
}

func (r emojiRepo) UserPacks(ctx context.Context, userID int64, limit int) ([]EmojiPack, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListUserEmojiPacks(ctx, sqlcdb.ListUserEmojiPacksParams{UserID: userID, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]EmojiPack, len(rows))
	for i, p := range rows {
		out[i] = emojiPackFrom(sqlcdb.EmojiPack{
			ID: p.ID, OwnerID: p.OwnerID, ChatID: p.ChatID, Title: p.Title, Slug: p.Slug, Version: p.Version,
			HiddenAt: p.HiddenAt, DeletedAt: p.DeletedAt, CreatedAt: p.CreatedAt,
		}, p.Installed)
	}
	return out, nil
}

func (r emojiRepo) Installed(ctx context.Context, userID, packID int64) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ok, err := r.s.q.IsEmojiPackInstalled(ctx, sqlcdb.IsEmojiPackInstalledParams{UserID: userID, PackID: packID})
	return ok, mapError(err)
}

func (r emojiRepo) SetInstalled(ctx context.Context, userID, packID int64, installed bool) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	if installed {
		return mapError(r.s.q.InstallEmojiPack(ctx, sqlcdb.InstallEmojiPackParams{UserID: userID, PackID: packID}))
	}
	return mapError(r.s.q.UninstallEmojiPack(ctx, sqlcdb.UninstallEmojiPackParams{UserID: userID, PackID: packID}))
}

func (r emojiRepo) PackEmoji(ctx context.Context, packIDs []int64) (map[int64][]CustomEmoji, error) {
	out := make(map[int64][]CustomEmoji, len(packIDs))
	if len(packIDs) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListPackEmoji(ctx, packIDs)
	if err != nil {
		return nil, mapError(err)
	}
	for _, e := range rows {
		out[e.PackID] = append(out[e.PackID], customEmojiFrom(e))
	}
	return out, nil
}

func (r emojiRepo) AddEmoji(ctx context.Context, packID int64, attachmentID uuid.UUID, shortcode string, animated bool, limit int) (CustomEmoji, error) {
	var out CustomEmoji
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if _, err := q.LockEmojiPack(ctx, packID); err != nil {
			return err
		}
		n, err := q.CountPackEmoji(ctx, packID)
		if err != nil {
			return err
		}
		if int(n) >= limit {
			return ErrLimit
		}
		e, err := q.AddCustomEmoji(ctx, sqlcdb.AddCustomEmojiParams{PackID: packID, AttachmentID: attachmentID, Shortcode: shortcode, IsAnimated: animated})
		if err != nil {
			return err
		}
		out = customEmojiFrom(e)
		return nil
	})
	return out, mapError(err)
}

func (r emojiRepo) RenameEmoji(ctx context.Context, packID, id int64, shortcode string) (CustomEmoji, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	e, err := r.s.q.RenameCustomEmoji(ctx, sqlcdb.RenameCustomEmojiParams{ID: id, PackID: packID, Shortcode: shortcode})
	if err != nil {
		return CustomEmoji{}, mapError(err)
	}
	return customEmojiFrom(e), nil
}

func (r emojiRepo) DeleteEmoji(ctx context.Context, packID, id int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return affected(r.s.q.DeleteCustomEmoji(ctx, sqlcdb.DeleteCustomEmojiParams{ID: id, PackID: packID}))
}

func (r emojiRepo) Emoji(ctx context.Context, id int64) (EmojiImage, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	e, err := r.s.q.GetCustomEmoji(ctx, id)
	if err != nil {
		return EmojiImage{}, mapError(err)
	}
	return EmojiImage{
		CustomEmoji: CustomEmoji{ID: e.ID, PackID: e.PackID, AttachmentID: e.AttachmentID, Shortcode: e.Shortcode, IsAnimated: e.IsAnimated, Position: int(e.Position)},
		Deleted:     e.DeletedAt != nil || e.PackDeletedAt != nil, PackSlug: e.PackSlug, StorageKey: e.StorageKey, MimeType: e.MimeType,
	}, nil
}

func (r emojiRepo) Usable(ctx context.Context, userID int64, ids []int64) (map[int64]CustomEmoji, error) {
	out := make(map[int64]CustomEmoji, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListUsableEmoji(ctx, sqlcdb.ListUsableEmojiParams{Ids: ids, UserID: &userID})
	if err != nil {
		return nil, mapError(err)
	}
	for _, e := range rows {
		out[e.ID] = CustomEmoji{ID: e.ID, Shortcode: e.Shortcode, IsAnimated: e.IsAnimated}
	}
	return out, nil
}

func (r emojiRepo) Report(ctx context.Context, rep EmojiReport) error {
	return mapError(r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		recent, err := q.CountRecentEmojiReports(ctx, sqlcdb.CountRecentEmojiReportsParams{ReporterID: rep.ReporterID, Since: rep.Since})
		if err != nil {
			return err
		}
		if int(recent) >= rep.MaxReports {
			return ErrLimit
		}
		if _, err := q.InsertEmojiPackReport(ctx, sqlcdb.InsertEmojiPackReportParams{PackID: rep.PackID, ReporterID: rep.ReporterID, Reason: rep.Reason}); err != nil {
			return err
		}
		_, err = q.HideReportedEmojiPack(ctx, sqlcdb.HideReportedEmojiPackParams{ID: rep.PackID, Threshold: int32(rep.HideAfter)}) //nolint:gosec // a small constant
		return err
	}))
}

func (r attachmentRepo) IsEmojiImage(ctx context.Context, attachmentID uuid.UUID) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ok, err := r.s.q.IsEmojiImage(ctx, attachmentID)
	return ok, mapError(err)
}
