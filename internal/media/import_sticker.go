package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"io"
	"os"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const JobImportSticker = "import_sticker"

type importedSticker struct {
	data     []byte
	duration *time.Duration
}

func importStickerFrom(path, contentType string) (importedSticker, bool) {
	if contentType != "image/webp" {
		return importedSticker{}, false
	}
	data, err := os.ReadFile(path) //nolint:gosec // our temporary file
	if err != nil {
		return importedSticker{}, false
	}
	st, err := prepareImportSticker(data)
	return st, err == nil
}

func prepareImportSticker(data []byte) (importedSticker, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return importedSticker{}, ErrUnsupported
	}
	if animatedWebP(data) {
		return prepareAnimatedImportSticker(data)
	}
	still, ok := stripStillWebP(data)
	if !ok || len(still) > MaxStickerBytes {
		return importedSticker{}, ErrUnsupported
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(still))
	if err != nil || cfg.Width != StickerSide || cfg.Height != StickerSide {
		return importedSticker{}, ErrUnsupported
	}
	return importedSticker{data: still}, nil
}

func prepareAnimatedImportSticker(data []byte) (importedSticker, error) {
	riffSize := int(binary.LittleEndian.Uint32(data[4:8]))
	if riffSize < 4 || riffSize > len(data)-8 {
		return importedSticker{}, ErrUnsupported
	}
	chunks, err := riffChunks(data[12 : 8+riffSize])
	if err != nil || len(chunks) < 3 || chunks[0].fourCC != "VP8X" || len(chunks[0].payload) < 10 {
		return importedSticker{}, ErrUnsupported
	}
	head := chunks[0].payload
	if uint24(head[4:7])+1 != StickerSide || uint24(head[7:10])+1 != StickerSide {
		return importedSticker{}, ErrUnsupported
	}
	vp8x := make([]byte, 10)
	copy(vp8x, head[:10])
	vp8x[0] &= webpFlagAlpha | webpFlagAnimation
	body := appendChunk(nil, "VP8X", vp8x)
	var total time.Duration
	frames := 0
	seenAnim := false
	for _, c := range chunks[1:] {
		switch c.fourCC {
		case "ANIM":
			if seenAnim || frames > 0 || len(c.payload) != 6 {
				return importedSticker{}, ErrUnsupported
			}
			seenAnim = true
			body = appendChunk(body, "ANIM", c.payload)
		case "ANMF":
			if !seenAnim {
				return importedSticker{}, ErrUnsupported
			}
			f, payload, err := parseANMF(c.payload, StickerSide, StickerSide)
			if err != nil {
				return importedSticker{}, ErrUnsupported
			}
			frames++
			total += webpFrameTime(f.duration)
			body = appendChunk(body, "ANMF", payload)
		}
	}
	out := riffWebP(body)
	if frames == 0 || frames > maxGIFFrames || total > MaxStickerClip || len(out) > MaxAnimatedStickerBytes {
		return importedSticker{}, ErrUnsupported
	}
	return importedSticker{data: out, duration: &total}, nil
}

func (s *Service) runImportSticker(ctx context.Context, a postgres.Attachment) (postgres.Attachment, error) {
	obj, err := s.o.Storage.Get(ctx, a.StorageKey)
	if err != nil {
		return a, err
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, MaxAnimatedStickerBytes+1))
	_ = obj.Body.Close()
	if err != nil {
		return a, err
	}
	select {
	case s.images <- struct{}{}:
	case <-ctx.Done():
		return a, ctx.Err()
	}
	defer func() { <-s.images }()
	vs := newVariantSet(a.StorageKey)
	if animatedWebP(data) {
		clip, err := webpStickerFrames(data, Upload{Purpose: PurposeSticker})
		if err != nil {
			return a, ErrUnsupported
		}
		if err := s.animatedStickerVariants(ctx, framesStickerSource(clip.frames), nil, vs); err != nil {
			return a, err
		}
	} else {
		img, err := StickerImage(data, "image/webp")
		if err != nil {
			return a, ErrUnsupported
		}
		s.addResized(ctx, vs, VariantNamePanel, img, PanelSide, 80)
		s.addResized(ctx, vs, VariantNameMessage, img, MessageStickerSide, 80)
	}
	if err := s.putAll(ctx, vs.puts); err != nil {
		return a, err
	}
	done := ProcessingDone
	updated, err := s.o.Attachments.MergeVariants(ctx, a.ID, postgres.AttachmentUpdate{Variants: vs.variants, ProcessingState: &done})
	if err != nil {
		s.remove(context.WithoutCancel(ctx), vs.puts)
		return a, err
	}
	return updated, nil
}
