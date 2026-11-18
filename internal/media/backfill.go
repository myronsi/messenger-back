package media

import (
	"bytes"
	"context"
	"image"
	"io"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type BackfillKind struct {
	Kinds    []string
	Purposes []string
	Missing  string
	Job      string
	Refresh  bool
}

var BackfillKinds = map[string]BackfillKind{
	"video":      {Kinds: []string{KindVideo}, Purposes: []string{PurposeMessage}, Missing: VariantNameOptimized, Job: JobVideo},
	"audio":      {Kinds: []string{KindAudio}, Purposes: []string{PurposeMessage}, Missing: VariantNameOptimized, Job: JobAudio},
	"gif":        {Kinds: []string{KindImage}, Purposes: []string{PurposeMessage}, Missing: VariantNameAnimation, Job: JobGIF},
	"audio-tags": {Kinds: []string{KindAudio}, Purposes: []string{PurposeMessage}, Missing: "tags", Refresh: true},
	"image":      {Kinds: []string{KindImage}, Purposes: []string{PurposeMessage, PurposeProfileBackground}, Missing: VariantNameDisplay, Refresh: true},
	"stickers":   {Kinds: []string{KindImage}, Purposes: []string{PurposeSticker}, Missing: VariantNamePanel, Refresh: true},
	"emoji":      {Kinds: []string{KindImage}, Purposes: []string{PurposeEmoji}, Missing: VariantNameInline, Refresh: true},
	"avatars":    {Kinds: []string{KindImage}, Purposes: []string{PurposeAvatar}, Missing: AvatarVariant(128), Refresh: true},
}

func (s *Service) BackfillPage(ctx context.Context, k BackfillKind, after uuid.UUID, limit int) ([]postgres.Attachment, error) {
	return s.o.Attachments.ListForBackfill(ctx, postgres.BackfillFilter{
		Kinds: k.Kinds, Purposes: k.Purposes, MissingVariant: k.Missing, After: after, Limit: limit,
	})
}

func (s *Service) EnqueueJob(ctx context.Context, a postgres.Attachment, kind string) (bool, error) {
	if s.o.Jobs == nil {
		return false, ErrUnsupported
	}
	queued, err := s.o.Jobs.Enqueue(ctx, a.ID, kind)
	if err != nil || !queued {
		return queued, err
	}
	state := ProcessingQueued
	return true, s.o.Attachments.SetProcessing(ctx, a.ID, &state)
}

func (s *Service) Refresh(ctx context.Context, a postgres.Attachment) (bool, error) {
	if a.Kind == KindAudio {
		return s.refreshAudio(ctx, a)
	}
	if a.MimeType == "image/gif" {
		if a.Purpose != PurposeEmoji {
			return false, nil
		}
		return s.refreshAnimatedEmoji(ctx, a)
	}
	obj, err := s.o.Storage.Get(ctx, a.StorageKey)
	if err != nil {
		return false, err
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, max(a.Size, s.o.Limits.Image)+1))
	_ = obj.Body.Close()
	if err != nil {
		return false, err
	}
	if animatedWebP(data) {
		return false, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width*cfg.Height > MaxImagePixels {
		return false, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false, nil
	}
	vs := newVariantSet(a.StorageKey)
	var placeholder *string
	switch a.Purpose {
	case PurposeSticker:
		s.addResized(ctx, vs, VariantNamePanel, img, PanelSide, 80)
		s.addResized(ctx, vs, VariantNameMessage, img, MessageStickerSide, 80)
	case PurposeEmoji:
		s.addResized(ctx, vs, VariantNameInline, img, InlineEmojiSide, 85)
	case PurposeAvatar:
		for _, side := range avatarSides {
			s.addResized(ctx, vs, AvatarVariant(side), img, side, 82)
		}
	default:
		if NeedsDisplay(a.MimeType, cfg.Width, cfg.Height) {
			s.addResized(ctx, vs, VariantNameDisplay, img, DisplaySide, 80)
		}
	}
	if a.Placeholder == nil && (a.Purpose == PurposeMessage || a.Purpose == PurposeProfileBackground) {
		if ph := Placeholder(img); ph != "" {
			placeholder = &ph
		}
	}
	return s.saveRefresh(ctx, a, vs, postgres.AttachmentUpdate{Placeholder: placeholder})
}

func (s *Service) saveRefresh(ctx context.Context, a postgres.Attachment, vs *variantSet, u postgres.AttachmentUpdate) (bool, error) {
	if len(vs.puts) == 0 && u.Placeholder == nil && u.AudioTitle == nil && u.AudioArtist == nil && u.AudioAlbum == nil {
		return false, nil
	}
	if err := s.putAll(ctx, vs.puts); err != nil {
		return false, err
	}
	u.Variants = vs.variants
	if _, err := s.o.Attachments.MergeVariants(ctx, a.ID, u); err != nil {
		s.remove(ctx, vs.puts)
		return false, err
	}
	return true, nil
}

func (s *Service) refreshAnimatedEmoji(ctx context.Context, a postgres.Attachment) (bool, error) {
	obj, err := s.o.Storage.Get(ctx, a.StorageKey)
	if err != nil {
		return false, err
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, MaxEmojiBytes+1))
	_ = obj.Body.Close()
	if err != nil {
		return false, err
	}
	out, err := s.o.Prober.AnimatedWebP(ctx, data, InlineEmojiSide, 80)
	if err != nil {
		return false, nil
	}
	vs := newVariantSet(a.StorageKey)
	w, h := InlineEmojiSide, InlineEmojiSide
	if a.Width != nil && a.Height != nil {
		w, h = fitSize(int(*a.Width), int(*a.Height), InlineEmojiSide)
	}
	vs.add(VariantNameInline, EncodedImage{Data: out, ContentType: "image/webp", Width: w, Height: h})
	return s.saveRefresh(ctx, a, vs, postgres.AttachmentUpdate{})
}

func (s *Service) refreshAudio(ctx context.Context, a postgres.Attachment) (bool, error) {
	if a.AudioTitle != nil || a.AudioArtist != nil || a.AudioAlbum != nil || !s.o.Prober.Available() {
		return false, nil
	}
	path, cleanup, err := s.fetch(ctx, a)
	if err != nil {
		return false, err
	}
	defer cleanup()
	t := s.o.Prober.tags(ctx, path)
	return s.saveRefresh(ctx, a, newVariantSet(a.StorageKey), postgres.AttachmentUpdate{AudioTitle: t.Title, AudioArtist: t.Artist, AudioAlbum: t.Album})
}
