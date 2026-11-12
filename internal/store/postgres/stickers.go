package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

const PurposeSticker = "sticker"

type StickerPack struct {
	ID         int64
	OwnerID    *int64
	BuiltinKey *string
	Title      string
	IsDefault  bool
	Slug       *string
	Hidden     bool
	Deleted    bool
	CreatedAt  time.Time
	Version    int64
}

type Sticker struct {
	ID          int64
	PackID      int64
	PackSlug    *string
	PackOwnerID *int64
	Emoji       string
	Animated    bool
	Deleted     bool
	Image       Attachment
}

type BuiltinSticker struct {
	Key          string
	Emoji        string
	Position     int32
	Animated     bool
	AttachmentID uuid.UUID
}

type StickerRepository interface {
	EnsureBuiltinPack(ctx context.Context, key, title string, isDefault bool) (int64, error)
	BuiltinStickerIDs(ctx context.Context, packID int64) (map[string]int64, error)
	AddBuiltinSticker(ctx context.Context, packID int64, s BuiltinSticker) error
	UpdateBuiltinSticker(ctx context.Context, id int64, s BuiltinSticker) error
	RetireBuiltinStickers(ctx context.Context, packID int64, keep []string) error
	Pack(ctx context.Context, id int64) (StickerPack, error)
	Installation(ctx context.Context, userID, packID int64) (*bool, error)
	InstalledPacks(ctx context.Context, userID int64, limit int) ([]StickerPack, error)
	SetInstalled(ctx context.Context, userID, packID int64, installed bool) error
	Remember(ctx context.Context, userID, packID int64) error
	Stickers(ctx context.Context, ids []int64) (map[int64]Sticker, error)
	PackStickers(ctx context.Context, packIDs []int64) (map[int64][]Sticker, error)
	UseSticker(ctx context.Context, userID, stickerID int64, keep int) error
	Recent(ctx context.Context, userID int64, limit int) ([]Sticker, error)
	CreatePack(ctx context.Context, ownerID int64, title, slug string, maxPacks int) (StickerPack, error)
	OwnedPacks(ctx context.Context, ownerID int64, limit int) ([]StickerPack, error)
	PackBySlug(ctx context.Context, slug string) (StickerPack, error)
	RenamePack(ctx context.Context, ownerID, packID int64, title string) (StickerPack, error)
	DeletePack(ctx context.Context, ownerID, packID int64) error
	AddSticker(ctx context.Context, ownerID, packID int64, image uuid.UUID, emoji string, maxStickers int) (int64, error)
	SetStickerEmoji(ctx context.Context, ownerID, packID, stickerID int64, emoji string) error
	DeleteSticker(ctx context.Context, ownerID, packID, stickerID int64) error
	ReorderStickers(ctx context.Context, ownerID, packID int64, ids []int64) error
	Report(ctx context.Context, r PackReport) (hidden bool, err error)
	Unhide(ctx context.Context, packID int64) (bool, error)
}

type PackReport struct {
	PackID     int64
	ReporterID int64
	Reason     string
	Since      time.Time
	MaxReports int
	HideAfter  int
}

type stickerRepo struct{ s *Store }

var _ StickerRepository = stickerRepo{}

func (s *Store) Stickers() StickerRepository { return stickerRepo{s} }

func packFrom(p sqlcdb.StickerPack) StickerPack {
	return StickerPack{
		ID: p.ID, OwnerID: p.OwnerID, BuiltinKey: p.BuiltinKey, Title: p.Title, IsDefault: p.IsDefault,
		Slug: p.Slug, Hidden: p.HiddenAt != nil, Deleted: p.DeletedAt != nil, CreatedAt: p.CreatedAt, Version: p.Version,
	}
}

func stickerFrom(s sqlcdb.Sticker, slug *string, owner *int64, a sqlcdb.Attachment) Sticker {
	return Sticker{
		ID: s.ID, PackID: s.PackID, PackSlug: slug, PackOwnerID: owner, Emoji: s.Emoji, Animated: s.IsAnimated,
		Deleted: s.DeletedAt != nil, Image: attachmentFrom(a),
	}
}

func (r stickerRepo) EnsureBuiltinPack(ctx context.Context, key, title string, isDefault bool) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	id, err := r.s.q.UpsertBuiltinStickerPack(ctx, sqlcdb.UpsertBuiltinStickerPackParams{BuiltinKey: &key, Title: title, IsDefault: isDefault})
	return id, mapError(err)
}

func (r stickerRepo) BuiltinStickerIDs(ctx context.Context, packID int64) (map[string]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListBuiltinStickerKeys(ctx, packID)
	if err != nil {
		return nil, mapError(err)
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.BuiltinKey] = row.ID
	}
	return out, nil
}

func (r stickerRepo) AddBuiltinSticker(ctx context.Context, packID int64, s BuiltinSticker) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.InsertBuiltinSticker(ctx, sqlcdb.InsertBuiltinStickerParams{
		PackID: packID, AttachmentID: s.AttachmentID, BuiltinKey: &s.Key, Emoji: s.Emoji,
		Position: s.Position, IsAnimated: s.Animated,
	}))
}

func (r stickerRepo) UpdateBuiltinSticker(ctx context.Context, id int64, s BuiltinSticker) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.UpdateBuiltinSticker(ctx, sqlcdb.UpdateBuiltinStickerParams{
		ID: id, Emoji: s.Emoji, Position: s.Position, IsAnimated: s.Animated,
	}))
}

func (r stickerRepo) RetireBuiltinStickers(ctx context.Context, packID int64, keep []string) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	if keep == nil {
		keep = []string{}
	}
	return mapError(r.s.q.RetireBuiltinStickers(ctx, sqlcdb.RetireBuiltinStickersParams{PackID: packID, Keep: keep}))
}

func (r stickerRepo) Pack(ctx context.Context, id int64) (StickerPack, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p, err := r.s.q.GetStickerPack(ctx, id)
	if err != nil {
		return StickerPack{}, mapError(err)
	}
	return packFrom(p), nil
}

func (r stickerRepo) Installation(ctx context.Context, userID, packID int64) (*bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	installed, err := r.s.q.GetStickerPackInstallation(ctx, sqlcdb.GetStickerPackInstallationParams{UserID: userID, PackID: packID})
	if err = mapError(err); errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &installed, nil
}

func (r stickerRepo) InstalledPacks(ctx context.Context, userID int64, limit int) ([]StickerPack, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListInstalledStickerPacks(ctx, sqlcdb.ListInstalledStickerPacksParams{UserID: userID, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	return packsFrom(rows), nil
}

func (r stickerRepo) SetInstalled(ctx context.Context, userID, packID int64, installed bool) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.SetStickerPackInstalled(ctx, sqlcdb.SetStickerPackInstalledParams{UserID: userID, PackID: packID, Installed: installed}))
}

func (r stickerRepo) Remember(ctx context.Context, userID, packID int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.RememberStickerPack(ctx, sqlcdb.RememberStickerPackParams{UserID: userID, PackID: packID}))
}

func (r stickerRepo) Stickers(ctx context.Context, ids []int64) (map[int64]Sticker, error) {
	if len(ids) == 0 {
		return map[int64]Sticker{}, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListStickersByID(ctx, ids)
	if err != nil {
		return nil, mapError(err)
	}
	out := make(map[int64]Sticker, len(rows))
	for _, row := range rows {
		s := stickerFrom(row.Sticker, row.PackSlug, row.PackOwnerID, row.Attachment)
		s.Deleted = s.Deleted || row.PackDeletedAt != nil || row.PackHiddenAt != nil
		out[s.ID] = s
	}
	return out, nil
}

func (r stickerRepo) PackStickers(ctx context.Context, packIDs []int64) (map[int64][]Sticker, error) {
	out := make(map[int64][]Sticker, len(packIDs))
	if len(packIDs) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListPackStickers(ctx, packIDs)
	if err != nil {
		return nil, mapError(err)
	}
	for _, row := range rows {
		out[row.Sticker.PackID] = append(out[row.Sticker.PackID], stickerFrom(row.Sticker, row.PackSlug, row.PackOwnerID, row.Attachment))
	}
	return out, nil
}

func (r stickerRepo) UseSticker(ctx context.Context, userID, stickerID int64, keep int) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := q.TouchRecentSticker(ctx, sqlcdb.TouchRecentStickerParams{UserID: userID, StickerID: stickerID}); err != nil {
			return err
		}
		return q.TrimRecentStickers(ctx, sqlcdb.TrimRecentStickersParams{UserID: userID, Keep: clampLimit(keep)})
	})
}

func (r stickerRepo) Recent(ctx context.Context, userID int64, limit int) ([]Sticker, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListRecentStickers(ctx, sqlcdb.ListRecentStickersParams{UserID: userID, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]Sticker, len(rows))
	for i, row := range rows {
		out[i] = stickerFrom(row.Sticker, row.PackSlug, row.PackOwnerID, row.Attachment)
	}
	return out, nil
}

func packsFrom(rows []sqlcdb.StickerPack) []StickerPack {
	out := make([]StickerPack, len(rows))
	for i, p := range rows {
		out[i] = packFrom(p)
	}
	return out
}

func (r stickerRepo) CreatePack(ctx context.Context, ownerID int64, title, slug string, maxPacks int) (StickerPack, error) {
	var out StickerPack
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if _, err := q.LockStickerPackOwner(ctx, ownerID); err != nil {
			return err
		}
		n, err := q.CountOwnedStickerPacks(ctx, &ownerID)
		if err != nil {
			return err
		}
		if int(n) >= maxPacks {
			return ErrLimit
		}
		p, err := q.InsertOwnedStickerPack(ctx, sqlcdb.InsertOwnedStickerPackParams{OwnerID: &ownerID, Title: title, Slug: &slug})
		if err != nil {
			return err
		}
		if err := q.SetStickerPackInstalled(ctx, sqlcdb.SetStickerPackInstalledParams{UserID: ownerID, PackID: p.ID, Installed: true}); err != nil {
			return err
		}
		out = packFrom(p)
		return nil
	})
	return out, err
}

func (r stickerRepo) OwnedPacks(ctx context.Context, ownerID int64, limit int) ([]StickerPack, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListOwnedStickerPacks(ctx, sqlcdb.ListOwnedStickerPacksParams{OwnerID: &ownerID, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	return packsFrom(rows), nil
}

func (r stickerRepo) PackBySlug(ctx context.Context, slug string) (StickerPack, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p, err := r.s.q.GetStickerPackBySlug(ctx, &slug)
	if err != nil {
		return StickerPack{}, mapError(err)
	}
	return packFrom(p), nil
}

func ownPack(ctx context.Context, q *sqlcdb.Queries, ownerID, packID int64) error {
	p, err := q.LockStickerPack(ctx, packID)
	if err != nil {
		return err
	}
	if p.DeletedAt != nil {
		return ErrNotFound
	}
	if p.OwnerID == nil || *p.OwnerID != ownerID {
		return ErrForbidden
	}
	return nil
}

func (r stickerRepo) RenamePack(ctx context.Context, ownerID, packID int64, title string) (StickerPack, error) {
	var out StickerPack
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := ownPack(ctx, q, ownerID, packID); err != nil {
			return err
		}
		p, err := q.RenameStickerPack(ctx, sqlcdb.RenameStickerPackParams{ID: packID, Title: title})
		out = packFrom(p)
		return err
	})
	return out, err
}

func (r stickerRepo) DeletePack(ctx context.Context, ownerID, packID int64) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := ownPack(ctx, q, ownerID, packID); err != nil {
			return err
		}
		return q.DeleteStickerPack(ctx, packID)
	})
}

func (r stickerRepo) AddSticker(ctx context.Context, ownerID, packID int64, image uuid.UUID, emoji string, maxStickers int) (int64, error) {
	var id int64
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := ownPack(ctx, q, ownerID, packID); err != nil {
			return err
		}
		if _, err := q.LockAttachment(ctx, image); errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalid
		} else if err != nil {
			return err
		}
		a, err := q.GetAttachment(ctx, image)
		if err != nil {
			return err
		}
		if a.Purpose != PurposeSticker || a.UploaderID == nil || *a.UploaderID != ownerID {
			return ErrInvalid
		}
		stats, err := q.PackStickerStats(ctx, packID)
		if err != nil {
			return err
		}
		if int(stats.Live) >= maxStickers {
			return ErrLimit
		}
		id, err = q.InsertOwnedSticker(ctx, sqlcdb.InsertOwnedStickerParams{
			PackID: packID, AttachmentID: image, Emoji: emoji, Position: stats.NextPosition,
		})
		return err
	})
	return id, err
}

func (r stickerRepo) changeSticker(ctx context.Context, ownerID, packID int64, fn func(ctx context.Context, q *sqlcdb.Queries) (int64, error)) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := ownPack(ctx, q, ownerID, packID); err != nil {
			return err
		}
		return affected(fn(ctx, q))
	})
}

func (r stickerRepo) SetStickerEmoji(ctx context.Context, ownerID, packID, stickerID int64, emoji string) error {
	return r.changeSticker(ctx, ownerID, packID, func(ctx context.Context, q *sqlcdb.Queries) (int64, error) {
		return q.SetStickerEmoji(ctx, sqlcdb.SetStickerEmojiParams{ID: stickerID, PackID: packID, Emoji: emoji})
	})
}

func (r stickerRepo) DeleteSticker(ctx context.Context, ownerID, packID, stickerID int64) error {
	return r.changeSticker(ctx, ownerID, packID, func(ctx context.Context, q *sqlcdb.Queries) (int64, error) {
		return q.DeleteSticker(ctx, sqlcdb.DeleteStickerParams{ID: stickerID, PackID: packID})
	})
}

func (r stickerRepo) ReorderStickers(ctx context.Context, ownerID, packID int64, ids []int64) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := ownPack(ctx, q, ownerID, packID); err != nil {
			return err
		}
		live, err := q.ListLiveStickerIDs(ctx, packID)
		if err != nil {
			return err
		}
		if len(ids) != len(live) {
			return ErrInvalid
		}
		want := make(map[int64]bool, len(live))
		for _, id := range live {
			want[id] = true
		}
		for _, id := range ids {
			if !want[id] {
				return ErrInvalid
			}
			delete(want, id)
		}
		return q.SetStickerPositions(ctx, sqlcdb.SetStickerPositionsParams{Ids: ids, PackID: packID})
	})
}

func (r stickerRepo) Report(ctx context.Context, rep PackReport) (bool, error) {
	var hidden bool
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		p, err := q.LockStickerPack(ctx, rep.PackID)
		if err != nil {
			return err
		}
		if p.DeletedAt != nil {
			return ErrNotFound
		}
		if p.BuiltinKey != nil || (p.OwnerID != nil && *p.OwnerID == rep.ReporterID) {
			return ErrForbidden
		}
		added, err := q.InsertStickerPackReport(ctx, sqlcdb.InsertStickerPackReportParams{
			PackID: rep.PackID, ReporterID: rep.ReporterID, Reason: rep.Reason,
		})
		if err != nil || added == 0 {
			return err
		}
		n, err := q.CountStickerReportsSince(ctx, sqlcdb.CountStickerReportsSinceParams{ReporterID: rep.ReporterID, Since: rep.Since})
		if err != nil {
			return err
		}
		if int(n) > rep.MaxReports {
			return ErrLimit
		}
		changed, err := q.HideReportedStickerPack(ctx, sqlcdb.HideReportedStickerPackParams{ID: rep.PackID, Threshold: clampLimit(rep.HideAfter)})
		hidden = changed > 0
		return err
	})
	return hidden, err
}

func (r stickerRepo) Unhide(ctx context.Context, packID int64) (bool, error) {
	var changed int64
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		var err error
		if changed, err = q.UnhideStickerPack(ctx, packID); err != nil {
			return err
		}
		return q.ClearStickerPackReports(ctx, packID)
	})
	return changed > 0, err
}
