package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/draw"
	"os/exec"
	"strconv"
	"time"

	xdraw "golang.org/x/image/draw"
)

const encodeTimeout = 30 * time.Second

var ErrNoEncoder = errors.New("media: no encoder available")

func (p *Prober) HasEncoder() bool { return p != nil && p.ffmpeg != "" }

func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok && n.Bounds().Min == (image.Point{}) && n.Stride == 4*n.Bounds().Dx() {
		return n
	}
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

func fit(img image.Image, side int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= side && h <= side {
		return img
	}
	if w >= h {
		h, w = max(1, h*side/w), side
	} else {
		w, h = max(1, w*side/h), side
	}
	src, sr := img, b
	if b.Dx() > 2*w || b.Dy() > 2*h {
		mid := image.NewNRGBA(image.Rect(0, 0, min(2*w, b.Dx()), min(2*h, b.Dy())))
		xdraw.ApproxBiLinear.Scale(mid, mid.Bounds(), img, b, draw.Src, nil)
		src, sr = mid, mid.Bounds()
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sr, draw.Src, nil)
	return dst
}

type WebPOptions struct {
	Quality  int
	Lossless bool
}

func (p *Prober) EncodeWebP(ctx context.Context, img image.Image, o WebPOptions) ([]byte, error) {
	if !p.HasEncoder() {
		return nil, ErrNoEncoder
	}
	n := toNRGBA(img)
	w, h := n.Bounds().Dx(), n.Bounds().Dy()
	if w <= 0 || h <= 0 || w > MaxImageSide || h > MaxImageSide {
		return nil, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, encodeTimeout)
	defer cancel()
	args := []string{"-nostdin", "-v", "error", "-protocol_whitelist", "pipe", "-threads", "1",
		"-f", "rawvideo", "-pix_fmt", "rgba", "-s", strconv.Itoa(w) + "x" + strconv.Itoa(h), "-i", "pipe:0",
		"-frames:v", "1", "-c:v", "libwebp"}
	if o.Lossless {
		args = append(args, "-lossless", "1", "-compression_level", "4")
	} else {
		pix := "yuv420p"
		if !opaque(n) {
			pix = "yuva420p"
		}
		args = append(args, "-pix_fmt", pix, "-quality", strconv.Itoa(o.Quality))
	}
	args = append(args, "-threads", "1", "-f", "webp", "pipe:1")
	cmd := exec.CommandContext(ctx, p.ffmpeg, args...) //nolint:gosec
	cmd.Stdin = bytes.NewReader(n.Pix)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrMeasureTimeout
		}
		return nil, err
	}
	if len(out) < 30 || string(out[:4]) != "RIFF" || string(out[8:12]) != "WEBP" || animatedWebP(out) {
		return nil, ErrUnsupported
	}
	return out, nil
}

type EncodedImage struct {
	Data        []byte
	ContentType string
	Width       int
	Height      int
}

func (p *Prober) Resized(ctx context.Context, img image.Image, side, quality int) (EncodedImage, error) {
	scaledImg := fit(img, side)
	b := scaledImg.Bounds()
	data, err := p.EncodeWebP(ctx, scaledImg, WebPOptions{Quality: quality})
	if err != nil {
		return EncodedImage{}, err
	}
	return EncodedImage{Data: data, ContentType: "image/webp", Width: b.Dx(), Height: b.Dy()}, nil
}

func fitSize(w, h, side int) (int, int) {
	if w <= side && h <= side {
		return w, h
	}
	if w >= h {
		return side, max(1, h*side/w)
	}
	return max(1, w*side/h), side
}

func (p *Prober) AnimatedWebP(ctx context.Context, gifData []byte, side, quality int) ([]byte, error) {
	if !p.HasEncoder() {
		return nil, ErrNoEncoder
	}
	ctx, cancel := context.WithTimeout(ctx, encodeTimeout)
	defer cancel()
	scale := "scale=w='min(" + strconv.Itoa(side) + ",iw)':h='min(" + strconv.Itoa(side) + ",ih)':force_original_aspect_ratio=decrease"
	cmd := exec.CommandContext(ctx, p.ffmpeg, "-nostdin", "-v", "error", "-protocol_whitelist", "pipe", "-threads", "1", //nolint:gosec
		"-f", "gif", "-i", "pipe:0", "-vf", scale, "-pix_fmt", "yuva420p", "-c:v", "libwebp_anim", "-quality", strconv.Itoa(quality),
		"-loop", "0", "-threads", "1", "-f", "webp", "pipe:1")
	cmd.Stdin = bytes.NewReader(gifData)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrMeasureTimeout
		}
		return nil, err
	}
	if len(out) < 30 || string(out[:4]) != "RIFF" || string(out[8:12]) != "WEBP" {
		return nil, ErrUnsupported
	}
	return out, nil
}

func (p *Prober) StillToPNG(ctx context.Context, path string) ([]byte, error) {
	if !p.HasEncoder() {
		return nil, ErrNoEncoder
	}
	ctx, cancel := context.WithTimeout(ctx, encodeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, p.ffmpeg, "-nostdin", "-v", "error", "-protocol_whitelist", "file", "-threads", "1", //nolint:gosec
		"-i", path, "-frames:v", "1", "-c:v", "png", "-f", "image2pipe", "pipe:1").Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrMeasureTimeout
		}
		return nil, err
	}
	if len(out) < 8 || string(out[1:4]) != "PNG" {
		return nil, ErrUnsupported
	}
	return out, nil
}

const maxPreviewPixels = 16_000_000

func (p *Prober) ShrinkImage(ctx context.Context, data []byte, side, quality int) ([]byte, string, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", ErrUnsupported
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPreviewPixels {
		return nil, "", ErrImageTooLarge
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", ErrUnsupported
	}
	out, err := p.Resized(ctx, img, side, quality)
	if err != nil {
		return nil, "", err
	}
	return out.Data, out.ContentType, nil
}
