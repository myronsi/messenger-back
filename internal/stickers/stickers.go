package stickers

import (
	"context"
	"errors"
	"fmt"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	MaxPacks  = 200
	MaxRecent = messages.RecentStickers
)

var (
	ErrNotFound    = errors.New("sticker pack not found")
	ErrUnavailable = errors.New("temporarily unavailable")
)

type Pack struct {
	postgres.StickerPack
	Installed bool
	Stickers  []postgres.Sticker
}

type Service struct {
	store postgres.StickerRepository
}

func New(store postgres.StickerRepository) *Service {
	return &Service{store: store}
}

func unavailable(err error) error {
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

func (s *Service) Installed(ctx context.Context, userID int64) ([]Pack, error) {
	packs, err := s.store.InstalledPacks(ctx, userID, MaxPacks)
	if err != nil {
		return nil, unavailable(err)
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
		out[i] = Pack{StickerPack: p, Installed: true, Stickers: items[p.ID]}
	}
	return out, nil
}

func (s *Service) livePack(ctx context.Context, packID int64) (postgres.StickerPack, error) {
	p, err := s.store.Pack(ctx, packID)
	if errors.Is(err, postgres.ErrNotFound) || (err == nil && p.Deleted) {
		return postgres.StickerPack{}, ErrNotFound
	}
	if err != nil {
		return postgres.StickerPack{}, unavailable(err)
	}
	return p, nil
}

func (s *Service) Pack(ctx context.Context, userID, packID int64) (Pack, error) {
	p, err := s.livePack(ctx, packID)
	if err != nil {
		return Pack{}, err
	}
	if !visible(p, userID) {
		return Pack{}, ErrNotFound
	}
	installed, err := s.store.Installation(ctx, userID, packID)
	if err != nil {
		return Pack{}, unavailable(err)
	}
	items, err := s.store.PackStickers(ctx, []int64{packID})
	if err != nil {
		return Pack{}, unavailable(err)
	}
	out := Pack{StickerPack: p, Installed: p.IsDefault, Stickers: items[packID]}
	if installed != nil {
		out.Installed = *installed
	}
	return out, nil
}

func (s *Service) SetInstalled(ctx context.Context, userID, packID int64, installed bool) error {
	p, err := s.livePack(ctx, packID)
	if err != nil {
		return err
	}
	if !visible(p, userID) {
		return ErrNotFound
	}
	if err := s.store.SetInstalled(ctx, userID, packID, installed); err != nil {
		return unavailable(err)
	}
	return nil
}

func (s *Service) Recent(ctx context.Context, userID int64) ([]postgres.Sticker, error) {
	out, err := s.store.Recent(ctx, userID, MaxRecent)
	if err != nil {
		return nil, unavailable(err)
	}
	return out, nil
}
