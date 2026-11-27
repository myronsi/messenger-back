package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"io"
	"os"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const JobImportImage = "import_image"

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

var keptPNGChunks = map[string]bool{
	"IHDR": true, "PLTE": true, "tRNS": true, "IDAT": true, "IEND": true,
	"gAMA": true, "cHRM": true, "sRGB": true, "sBIT": true, "bKGD": true, "pHYs": true,
}

type importedImage struct {
	data          []byte
	width, height int
}

func stripPNG(data []byte) ([]byte, bool) {
	if !bytes.HasPrefix(data, pngSignature) {
		return nil, false
	}
	out := append([]byte{}, pngSignature...)
	for pos := len(pngSignature); pos < len(data); {
		if len(data)-pos < 12 {
			return nil, false
		}
		length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		end := pos + 12 + length
		if length < 0 || end > len(data) {
			return nil, false
		}
		name := string(data[pos+4 : pos+8])
		if crc32.ChecksumIEEE(data[pos+4:pos+8+length]) != binary.BigEndian.Uint32(data[pos+8+length:end]) {
			return nil, false
		}
		if keptPNGChunks[name] {
			out = append(out, data[pos:end]...)
		}
		pos = end
		if name == "IEND" {
			return out, true
		}
	}
	return nil, false
}

func stripStillWebP(data []byte) ([]byte, bool) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || animatedWebP(data) {
		return nil, false
	}
	riffSize := int(binary.LittleEndian.Uint32(data[4:8]))
	if riffSize < 4 || riffSize > len(data)-8 {
		return nil, false
	}
	chunks, err := riffChunks(data[12 : 8+riffSize])
	if err != nil || len(chunks) == 0 {
		return nil, false
	}
	var body []byte
	var hasAlpha bool
	var bitstream *riffChunk
	var head []byte
	for i := range chunks {
		switch chunks[i].fourCC {
		case "VP8X":
			if len(chunks[i].payload) < 10 {
				return nil, false
			}
			head = chunks[i].payload
		case "ALPH":
			hasAlpha = true
		case "VP8 ", "VP8L":
			if bitstream != nil {
				return nil, false
			}
			bitstream = &chunks[i]
		}
	}
	if bitstream == nil {
		return nil, false
	}
	if head != nil {
		vp8x := make([]byte, 10)
		copy(vp8x, head[:10])
		vp8x[0] &= webpFlagAlpha
		body = appendChunk(body, "VP8X", vp8x)
		if hasAlpha {
			for _, c := range chunks {
				if c.fourCC == "ALPH" {
					body = appendChunk(body, "ALPH", c.payload)
				}
			}
		}
	}
	body = appendChunk(body, bitstream.fourCC, bitstream.payload)
	return riffWebP(body), true
}

func prepareImportImage(data []byte, contentType string) (importedImage, error) {
	var stripped []byte
	ok := false
	switch contentType {
	case "image/jpeg":
		if jpegOrientation(data) == 1 {
			stripped, ok = stripJPEG(data)
		}
	case "image/png":
		stripped, ok = stripPNG(data)
	case "image/webp":
		stripped, ok = stripStillWebP(data)
	}
	if !ok {
		return importedImage{}, ErrUnsupported
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(stripped))
	if err != nil {
		return importedImage{}, ErrUnsupported
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxImageSide || cfg.Height > MaxImageSide || cfg.Width*cfg.Height > MaxImagePixels {
		return importedImage{}, ErrImageTooLarge
	}
	return importedImage{data: stripped, width: cfg.Width, height: cfg.Height}, nil
}

func (s *Service) importImageFrom(path, contentType string) (importedImage, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // our temporary file
	if err != nil {
		return importedImage{}, false
	}
	img, err := prepareImportImage(data, contentType)
	return img, err == nil
}

func (s *Service) runImportImage(ctx context.Context, a postgres.Attachment) (postgres.Attachment, error) {
	obj, err := s.o.Storage.Get(ctx, a.StorageKey)
	if err != nil {
		return a, err
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, s.o.Limits.Max()+1))
	_ = obj.Body.Close()
	if err != nil {
		return a, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return a, ErrUnsupported
	}
	done := ProcessingDone
	update := postgres.AttachmentUpdate{ProcessingState: &done}
	vs := newVariantSet(a.StorageKey)
	b := img.Bounds()
	if max(b.Dx(), b.Dy()) > ThumbnailWebPSide {
		if t, terr := s.o.Prober.Resized(ctx, img, ThumbnailWebPSide, 75); terr == nil {
			key := a.StorageKey + ".thumb" + extensionOf(t.ContentType)
			vs.puts = append(vs.puts, put{key, bytes.NewReader(t.Data), int64(len(t.Data)), t.ContentType})
			update.ThumbnailKey = &key
		} else if t, terr := thumbnail(img); terr == nil {
			key := a.StorageKey + ".thumb.jpg"
			vs.puts = append(vs.puts, put{key, bytes.NewReader(t), int64(len(t)), "image/jpeg"})
			update.ThumbnailKey = &key
		}
	}
	if NeedsDisplay(a.MimeType, b.Dx(), b.Dy()) {
		s.addResized(ctx, vs, VariantNameDisplay, img, DisplaySide, 80)
	}
	if ph := Placeholder(img); ph != "" {
		update.Placeholder = &ph
	}
	if err := s.putAll(ctx, vs.puts); err != nil {
		return a, err
	}
	update.Variants = vs.variants
	updated, err := s.o.Attachments.MergeVariants(ctx, a.ID, update)
	if err != nil {
		s.remove(context.WithoutCancel(ctx), vs.puts)
		return a, err
	}
	return updated, nil
}
