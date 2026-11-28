package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/draw"
	"io"
	"os"
	"time"

	"golang.org/x/image/webp"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const JobImportSticker = "import_sticker"

type importedSticker struct {
	data          []byte
	contentType   string
	width, height int
	duration      *time.Duration
	ready         bool
}

type strippedAnimation struct {
	data          []byte
	width, height int
	frames        int
	total         time.Duration
}

func importStickerFrom(path, contentType string) (importedSticker, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // our temporary file
	if err != nil {
		return importedSticker{}, false
	}
	st, err := prepareImportSticker(data, contentType)
	return st, err == nil
}

func prepareImportSticker(data []byte, contentType string) (importedSticker, error) {
	if contentType == "image/webp" && animatedWebP(data) {
		anim, err := stripAnimatedWebP(data)
		if err != nil || len(anim.data) > MaxAnimatedStickerSource {
			return importedSticker{}, ErrUnsupported
		}
		ready := anim.width == StickerSide && anim.height == StickerSide && anim.total <= MaxStickerClip && len(anim.data) <= MaxAnimatedStickerBytes
		total := min(anim.total, MaxStickerClip)
		return importedSticker{data: anim.data, contentType: contentType, width: anim.width, height: anim.height, duration: &total, ready: ready}, nil
	}
	img, err := prepareImportImage(data, contentType)
	if err != nil {
		return importedSticker{}, err
	}
	ready := contentType == "image/webp" && img.width == StickerSide && img.height == StickerSide && len(img.data) <= MaxStickerBytes
	return importedSticker{data: img.data, contentType: contentType, width: img.width, height: img.height, ready: ready}, nil
}

func stripAnimatedWebP(data []byte) (strippedAnimation, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return strippedAnimation{}, ErrUnsupported
	}
	riffSize := int(binary.LittleEndian.Uint32(data[4:8]))
	if riffSize < 4 || riffSize > len(data)-8 {
		return strippedAnimation{}, ErrUnsupported
	}
	chunks, err := riffChunks(data[12 : 8+riffSize])
	if err != nil || len(chunks) < 3 || chunks[0].fourCC != "VP8X" || len(chunks[0].payload) < 10 {
		return strippedAnimation{}, ErrUnsupported
	}
	head := chunks[0].payload
	out := strippedAnimation{width: uint24(head[4:7]) + 1, height: uint24(head[7:10]) + 1}
	if out.width > MaxImageSide || out.height > MaxImageSide || out.width*out.height > MaxImagePixels {
		return strippedAnimation{}, ErrImageTooLarge
	}
	vp8x := make([]byte, 10)
	copy(vp8x, head[:10])
	vp8x[0] &= webpFlagAlpha | webpFlagAnimation
	body := appendChunk(nil, "VP8X", vp8x)
	seenAnim := false
	for _, c := range chunks[1:] {
		switch c.fourCC {
		case "ANIM":
			if seenAnim || out.frames > 0 || len(c.payload) != 6 {
				return strippedAnimation{}, ErrUnsupported
			}
			seenAnim = true
			body = appendChunk(body, "ANIM", c.payload)
		case "ANMF":
			if !seenAnim {
				return strippedAnimation{}, ErrUnsupported
			}
			f, payload, err := parseANMF(c.payload, out.width, out.height)
			if err != nil {
				return strippedAnimation{}, ErrUnsupported
			}
			out.frames++
			out.total += webpFrameTime(f.duration)
			body = appendChunk(body, "ANMF", payload)
		}
	}
	if out.frames == 0 || out.frames > maxGIFFrames {
		return strippedAnimation{}, ErrUnsupported
	}
	out.data = riffWebP(body)
	return out, nil
}

func (s *Service) runImportSticker(ctx context.Context, a postgres.Attachment) (postgres.Attachment, error) {
	obj, err := s.o.Storage.Get(ctx, a.StorageKey)
	if err != nil {
		return a, err
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, max(MaxAnimatedStickerSource, s.o.Limits.Sticker)+1))
	_ = obj.Body.Close()
	if err != nil {
		return a, err
	}
	a.MimeType = stickerTypeOf(data, a.MimeType)
	if st, err := prepareImportSticker(data, a.MimeType); err == nil && st.ready {
		return s.importStickerVariants(ctx, a, st)
	}
	return s.importStickerFull(ctx, a, data)
}

func stickerTypeOf(data []byte, stored string) string {
	switch {
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case bytes.HasPrefix(data, pngSignature):
		return "image/png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	}
	return stored
}

func (s *Service) importStickerVariants(ctx context.Context, a postgres.Attachment, st importedSticker) (postgres.Attachment, error) {
	data := st.data
	select {
	case s.images <- struct{}{}:
	case <-ctx.Done():
		return a, ctx.Err()
	}
	defer func() { <-s.images }()
	vs := newVariantSet(a.StorageKey)
	if animatedWebP(data) {
		first, err := firstWebPFrame(data)
		if err != nil {
			return a, ErrUnsupported
		}
		s.addResized(ctx, vs, VariantNamePanel, first, PanelSide, 80)
		if len(data) > MaxMessageStickerBytes {
			clip, err := webpStickerFrames(data, Upload{Purpose: PurposeSticker})
			if err != nil {
				return a, ErrUnsupported
			}
			s.animatedMessageVariant(ctx, framesStickerSource(clip.frames), nil, vs)
		}
	} else {
		img, err := StickerImage(data, "image/webp")
		if err != nil {
			return a, ErrUnsupported
		}
		s.addResized(ctx, vs, VariantNamePanel, img, PanelSide, 80)
		s.addResized(ctx, vs, VariantNameMessage, img, MessageStickerSide, 80)
	}
	var duration *float64
	if st.duration != nil {
		secs := st.duration.Seconds()
		duration = &secs
	}
	return s.finishImportSticker(ctx, a, vs, stickerFields(int64(len(data)), "image/webp", duration))
}

func stickerFields(size int64, contentType string, duration *float64) postgres.AttachmentUpdate {
	side := int32(StickerSide)
	name := "sticker" + extensionOf(contentType)
	return postgres.AttachmentUpdate{Size: &size, MimeType: &contentType, Filename: &name, Width: &side, Height: &side, Duration: duration}
}

func (s *Service) importStickerFull(ctx context.Context, a postgres.Attachment, data []byte) (postgres.Attachment, error) {
	tmp, err := os.CreateTemp("", "messenger-sticker-*")
	if err != nil {
		return a, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() { _ = tmp.Close() }()
	if _, err := tmp.Write(data); err != nil {
		return a, err
	}
	vs := newVariantSet(a.StorageKey)
	var main []byte
	contentType := "image/webp"
	var duration *float64
	if a.MimeType == "image/webp" && animatedWebP(data) {
		clip, err := s.processAnimatedSticker(ctx, tmp.Name(), Upload{Purpose: PurposeSticker}, true, vs)
		if err != nil {
			return a, err
		}
		secs := clip.Duration.Seconds()
		main, duration = clip.Data, &secs
	} else {
		img, err := s.processSticker(ctx, tmp, int64(len(data)), a.MimeType, vs)
		if err != nil {
			return a, err
		}
		main, contentType = img.Data, img.ContentType
	}
	if err := s.o.Storage.Put(ctx, a.StorageKey, bytes.NewReader(main), int64(len(main)), contentType); err != nil {
		return a, err
	}
	return s.finishImportSticker(ctx, a, vs, stickerFields(int64(len(main)), contentType, duration))
}

func (s *Service) finishImportSticker(ctx context.Context, a postgres.Attachment, vs *variantSet, update postgres.AttachmentUpdate) (postgres.Attachment, error) {
	if err := s.putAll(ctx, vs.puts); err != nil {
		return a, err
	}
	done := ProcessingDone
	update.Variants, update.ProcessingState = vs.variants, &done
	updated, err := s.o.Attachments.MergeVariants(ctx, a.ID, update)
	if err != nil {
		s.remove(context.WithoutCancel(ctx), vs.puts)
		return a, err
	}
	return updated, nil
}

func firstWebPFrame(data []byte) (*image.NRGBA, error) {
	riffSize := int(binary.LittleEndian.Uint32(data[4:8]))
	if riffSize < 4 || riffSize > len(data)-8 {
		return nil, ErrUnsupported
	}
	chunks, err := riffChunks(data[12 : 8+riffSize])
	if err != nil || len(chunks) == 0 || chunks[0].fourCC != "VP8X" || len(chunks[0].payload) < 10 {
		return nil, ErrUnsupported
	}
	w, h := uint24(chunks[0].payload[4:7])+1, uint24(chunks[0].payload[7:10])+1
	for _, c := range chunks[1:] {
		if c.fourCC != "ANMF" {
			continue
		}
		f, _, err := parseANMF(c.payload, w, h)
		if err != nil {
			return nil, ErrUnsupported
		}
		frame, err := webp.Decode(bytes.NewReader(f.still()))
		if err != nil {
			return nil, ErrUnsupported
		}
		canvas := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(canvas, image.Rect(f.x, f.y, f.x+f.width, f.y+f.height), frame, frame.Bounds().Min, draw.Src)
		return fitSquare(canvas, canvas.Bounds(), StickerSide), nil
	}
	return nil, ErrUnsupported
}
