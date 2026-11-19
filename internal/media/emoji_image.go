package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	PurposeEmoji   = postgres.PurposeEmoji
	EmojiSide      = 128
	MaxEmojiBytes  = 256 << 10
	MaxEmojiSource = 8 << 20
)

var emojiTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true}

func detectEmoji(head []byte) (Detected, error) {
	ct := sniff(head)
	if !emojiTypes[ct] {
		return Detected{}, ErrUnsupported
	}
	return Detected{Kind: KindImage, ContentType: ct}, nil
}

func EmojiImage(data []byte, contentType string) (ProcessedImage, error) {
	if contentType == "image/gif" {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return ProcessedImage{}, ErrUnsupported
		}
		if cfg.Width <= EmojiSide && cfg.Height <= EmojiSide && len(data) <= MaxEmojiBytes {
			if frames, area, err := scanGIF(data); err != nil || frames > maxGIFFrames || area > maxGIFArea {
				return ProcessedImage{}, ErrUnsupported
			}
			return ProcessedImage{Data: data, ContentType: "image/gif", Width: cfg.Width, Height: cfg.Height}, nil
		}
		scaled, w, h, err := ScaleGIF(data, EmojiSide)
		if err != nil {
			return ProcessedImage{}, err
		}
		return ProcessedImage{Data: scaled, ContentType: "image/gif", Width: w, Height: h}, nil
	}
	img, err := squareImage(data, contentType, EmojiSide)
	if err != nil {
		return ProcessedImage{}, err
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
		return ProcessedImage{}, err
	}
	return ProcessedImage{Data: buf.Bytes(), ContentType: "image/png", Width: EmojiSide, Height: EmojiSide, Source: img}, nil
}

func (s *Service) processEmoji(ctx context.Context, f *os.File, size int64, contentType string, vs *variantSet) (ProcessedImage, error) {
	if size > MaxEmojiSource {
		return ProcessedImage{}, ErrTooLarge
	}
	select {
	case s.images <- struct{}{}:
	case <-ctx.Done():
		return ProcessedImage{}, ErrBusy
	}
	defer func() { <-s.images }()
	data, err := io.ReadAll(io.NewSectionReader(f, 0, size))
	if err != nil {
		return ProcessedImage{}, err
	}
	out, err := EmojiImage(data, contentType)
	if err != nil {
		return ProcessedImage{}, err
	}
	if out.ContentType == "image/gif" {
		s.emojiAnimation(ctx, &out, vs)
	} else if out.Source != nil {
		s.emojiStill(ctx, &out, vs)
	}
	if len(out.Data) > MaxEmojiBytes {
		return ProcessedImage{}, fmt.Errorf("%w: the emoji is still over 256 KB after scaling; use fewer frames or colors", ErrTooLarge)
	}
	return out, nil
}

func (s *Service) emojiStill(ctx context.Context, out *ProcessedImage, vs *variantSet) {
	if data, err := s.o.Prober.EncodeWebP(ctx, out.Source, WebPOptions{Lossless: true}); err == nil && len(data) < len(out.Data) {
		out.Data, out.ContentType = data, "image/webp"
	}
	s.addResized(ctx, vs, VariantNameInline, out.Source, InlineEmojiSide, 85)
}

func (s *Service) emojiAnimation(ctx context.Context, out *ProcessedImage, vs *variantSet) {
	if !s.o.Prober.HasEncoder() {
		return
	}
	gifData := out.Data
	if data, err := s.o.Prober.AnimatedWebP(ctx, gifData, EmojiSide, 80); err == nil && len(data) < len(out.Data) {
		out.Data, out.ContentType = data, "image/webp"
	}
	if data, err := s.o.Prober.AnimatedWebP(ctx, gifData, InlineEmojiSide, 80); err == nil {
		w, h := fitSize(out.Width, out.Height, InlineEmojiSide)
		vs.add(VariantNameInline, EncodedImage{Data: data, ContentType: "image/webp", Width: w, Height: h})
	}
}

func (s *Service) canSeeEmoji(ctx context.Context, viewerID int64, a postgres.Attachment) (bool, error) {
	if a.UploaderID != nil && *a.UploaderID == viewerID {
		return true, nil
	}
	return s.o.Attachments.IsEmojiImage(ctx, a.ID)
}

func (s *Service) OpenPackImage(ctx context.Context, id uuid.UUID) (Download, error) {
	a, err := s.o.Attachments.Get(ctx, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return Download{}, ErrNoAccess
	}
	if err != nil {
		return Download{}, err
	}
	var ok bool
	switch a.Purpose {
	case PurposeSticker:
		ok, err = s.o.Attachments.IsStickerImage(ctx, a.ID)
	case PurposeEmoji:
		ok, err = s.o.Attachments.IsEmojiImage(ctx, a.ID)
	}
	if err != nil {
		return Download{}, err
	}
	if !ok {
		return Download{}, ErrNoAccess
	}
	return s.content(ctx, a, VariantOriginal)
}
