package media

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"

	xdraw "golang.org/x/image/draw"
)

const (
	StickerSide     = 512
	MaxStickerBytes = 512 << 10
)

var stickerTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

func animatedWebP(data []byte) bool {
	return len(data) > 20 && string(data[12:16]) == "VP8X" && data[20]&0x02 != 0
}

func StickerImage(data []byte, contentType string) (*image.NRGBA, error) {
	if !stickerTypes[contentType] || (contentType == "image/webp" && animatedWebP(data)) {
		return nil, ErrUnsupported
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrUnsupported
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxImageSide || cfg.Height > MaxImageSide || cfg.Width*cfg.Height > MaxImagePixels {
		return nil, ErrImageTooLarge
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrUnsupported
	}
	if contentType == "image/jpeg" {
		src = orient(src, jpegOrientation(data))
	}
	b := src.Bounds()
	w, h := StickerSide, StickerSide
	if b.Dx() > b.Dy() {
		h = max(1, b.Dy()*StickerSide/b.Dx())
	} else {
		w = max(1, b.Dx()*StickerSide/b.Dy())
	}
	dst := image.NewNRGBA(image.Rect(0, 0, StickerSide, StickerSide))
	at := image.Rect((StickerSide-w)/2, (StickerSide-h)/2, (StickerSide-w)/2+w, (StickerSide-h)/2+h)
	xdraw.CatmullRom.Scale(dst, at, src, b, xdraw.Src, nil)
	return dst, nil
}

func (s *Service) processSticker(ctx context.Context, f *os.File, size int64, contentType string) (ProcessedImage, error) {
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
	img, err := StickerImage(data, contentType)
	if err != nil {
		return ProcessedImage{}, err
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
		return ProcessedImage{}, err
	}
	out := ProcessedImage{Data: buf.Bytes(), ContentType: "image/png", Width: StickerSide, Height: StickerSide}
	if webp, err := s.encodeWebP(ctx, out.Data); err == nil {
		out.Data, out.ContentType = webp, "image/webp"
	} else {
		s.o.Log.WarnContext(ctx, "sticker kept as PNG", "error", err)
	}
	if len(out.Data) > MaxStickerBytes {
		return ProcessedImage{}, ErrTooLarge
	}
	return out, nil
}

func (s *Service) encodeWebP(ctx context.Context, pngData []byte) ([]byte, error) {
	if s.o.Prober == nil || s.o.Prober.ffmpeg == "" {
		return nil, exec.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.o.Prober.ffmpeg, "-nostdin", "-v", "error",
		"-protocol_whitelist", "pipe", "-threads", "1", "-f", "png_pipe", "-i", "pipe:0",
		"-frames:v", "1", "-pix_fmt", "yuva420p", "-c:v", "libwebp", "-quality", "85", "-threads", "1",
		"-f", "webp", "pipe:1")
	cmd.Stdin = bytes.NewReader(pngData)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	if len(out) < 30 || string(out[:4]) != "RIFF" || string(out[8:12]) != "WEBP" || animatedWebP(out) {
		return nil, ErrUnsupported
	}
	return out, nil
}
