package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

const PurposeSticker = "sticker"

type StickerPack struct {
	ID         int64
	OwnerID    *int64
	BuiltinKey *string
	Title      string
	IsDefault  bool
	Deleted    bool
	CreatedAt  time.Time
}

type Sticker struct {
	ID       int64
	PackID   int64
	Emoji    string
	Animated bool
	Deleted  bool
	Image    Attachment
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
	Stickers(ctx context.Context, ids []int64) (map[int64]Sticker, error)
	PackStickers(ctx context.Context, packIDs []int64) (map[int64][]Sticker, error)
	UseSticker(ctx context.Context, userID, stickerID int64, keep int) error
	Recent(ctx context.Context, userID int64, limit int) ([]Sticker, error)
}

type stickerRepo struct{ s *Store }

var _ StickerRepository = stickerRepo{}

func (s *Store) Stickers() StickerRepository { return stickerRepo{s} }

func packFrom(p sqlcdb.StickerPack) StickerPack {
	return StickerPack{
		ID: p.ID, OwnerID: p.OwnerID, BuiltinKey: p.BuiltinKey, Title: p.Title, IsDefault: p.IsDefault,
		Deleted: p.DeletedAt != nil, CreatedAt: p.CreatedAt,
	}
}

func stickerFrom(s sqlcdb.Sticker, a sqlcdb.Attachment) Sticker {
	return Sticker{
		ID: s.ID, PackID: s.PackID, Emoji: s.Emoji, Animated: s.IsAnimated, Deleted: s.DeletedAt != nil,
		Image: attachmentFrom(a),
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
	out := make([]StickerPack, len(rows))
	for i, p := range rows {
		out[i] = packFrom(p)
	}
	return out, nil
}

func (r stickerRepo) SetInstalled(ctx context.Context, userID, packID int64, installed bool) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.SetStickerPackInstalled(ctx, sqlcdb.SetStickerPackInstalledParams{UserID: userID, PackID: packID, Installed: installed}))
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
		s := stickerFrom(row.Sticker, row.Attachment)
		s.Deleted = s.Deleted || row.PackDeletedAt != nil
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
		out[row.Sticker.PackID] = append(out[row.Sticker.PackID], stickerFrom(row.Sticker, row.Attachment))
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
		out[i] = stickerFrom(row.Sticker, row.Attachment)
	}
	return out, nil
}
