package media

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"

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
	return ProcessedImage{Data: buf.Bytes(), ContentType: "image/png", Width: EmojiSide, Height: EmojiSide}, nil
}

func (s *Service) processEmoji(ctx context.Context, f *os.File, size int64, contentType string) (ProcessedImage, error) {
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
	if len(out.Data) > MaxEmojiBytes {
		return ProcessedImage{}, fmt.Errorf("%w: the emoji is still over 256 KB after scaling; use fewer frames or colors", ErrTooLarge)
	}
	return out, nil
}

func (s *Service) canSeeEmoji(ctx context.Context, viewerID int64, a postgres.Attachment) (bool, error) {
	if a.UploaderID != nil && *a.UploaderID == viewerID {
		return true, nil
	}
	return s.o.Attachments.IsEmojiImage(ctx, a.ID)
}
