package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"time"
)

const (
	MaxAnimatedStickerSource = 15 << 20
	MaxAnimatedStickerBytes  = 1 << 20
	MaxMessageStickerBytes   = 512 << 10
	MaxStickerClip           = 3 * time.Second
	stickerEncodeTimeout     = 60 * time.Second
)

type StickerCrop struct {
	X, Y, Size float64
}

type stickerStep struct {
	quality, fps int
}

var stickerLadder = []stickerStep{{75, 30}, {60, 24}, {50, 20}, {40, 15}}

func AnimatedStickerSource(d Detected, head []byte) bool {
	return d.Kind == KindVideo || d.ContentType == "image/gif" || (d.ContentType == "image/webp" && animatedWebP(head))
}

func validCrop(c *StickerCrop) bool {
	if c == nil {
		return true
	}
	return c.X >= 0 && c.Y >= 0 && c.Size > 0 && c.Size <= 1 && c.X <= 1 && c.Y <= 1 &&
		!math.IsNaN(c.X) && !math.IsNaN(c.Y) && !math.IsNaN(c.Size)
}

func clipWindow(startMS, endMS *int, total time.Duration, known bool) (time.Duration, time.Duration, error) {
	start := time.Duration(0)
	if startMS != nil {
		start = time.Duration(*startMS) * time.Millisecond
	}
	length := MaxStickerClip
	if endMS != nil {
		end := time.Duration(*endMS) * time.Millisecond
		if end <= start {
			return 0, 0, fmt.Errorf("%w: end_ms must be after start_ms", ErrInvalid)
		}
		if end-start > MaxStickerClip+musicClipTolerance {
			return 0, 0, fmt.Errorf("%w: a sticker keeps at most 3 s", ErrInvalid)
		}
		length = min(end-start, MaxStickerClip)
	}
	if known && start >= total {
		return 0, 0, fmt.Errorf("%w: start_ms is after the end of the clip", ErrInvalid)
	}
	return start, length, nil
}

func stickerFilter(side, fps int, crop *StickerCrop) string {
	s := strconv.Itoa(side)
	f := ""
	if crop != nil {
		size := "min(iw\\,ih)*" + formatFraction(crop.Size)
		f = "crop=w=" + size + ":h=" + size + ":x=min(iw*" + formatFraction(crop.X) + "\\,iw-" + size + "):y=min(ih*" + formatFraction(crop.Y) + "\\,ih-" + size + "),"
	}
	f += "fps=" + strconv.Itoa(fps) + ",scale=" + s + ":" + s + ":force_original_aspect_ratio=decrease:flags=lanczos,format=rgba," +
		"pad=" + s + ":" + s + ":(ow-iw)/2:(oh-ih)/2:color=black@0,format=yuva420p"
	return f
}

func formatFraction(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }

type stickerSource struct {
	args  []string
	stdin func() io.Reader
}

func fileStickerSource(path string, start, length time.Duration) stickerSource {
	args := []string{"-protocol_whitelist", "file"}
	if start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start.Seconds(), 'f', 3, 64))
	}
	return stickerSource{args: append(args, "-t", strconv.FormatFloat(length.Seconds(), 'f', 3, 64), "-i", path)}
}

func framesStickerSource(frames []*image.NRGBA) stickerSource {
	side := strconv.Itoa(StickerSide)
	return stickerSource{
		args: []string{"-protocol_whitelist", "pipe", "-f", "rawvideo", "-pix_fmt", "rgba", "-s", side + "x" + side,
			"-framerate", strconv.Itoa(webpStickerFPS), "-i", "pipe:0"},
		stdin: func() io.Reader {
			parts := make([]io.Reader, len(frames))
			for i, f := range frames {
				parts[i] = bytes.NewReader(f.Pix)
			}
			return io.MultiReader(parts...)
		},
	}
}

func (s *Service) encodeStickerClip(ctx context.Context, src stickerSource, side int, step stickerStep, crop *StickerCrop, still bool) ([]byte, error) {
	if s.o.Prober == nil || s.o.Prober.ffmpeg == "" {
		return nil, exec.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, stickerEncodeTimeout)
	defer cancel()
	args := append([]string{"-nostdin", "-v", "error", "-threads", "2"}, src.args...)
	args = append(args, "-an", "-map_metadata", "-1", "-vf", stickerFilter(side, step.fps, crop))
	if still {
		args = append(args, "-frames:v", "1", "-c:v", "libwebp", "-quality", strconv.Itoa(step.quality), "-f", "webp", "pipe:1")
	} else {
		args = append(args, "-c:v", "libwebp_anim", "-loop", "0", "-quality", strconv.Itoa(step.quality), "-f", "webp", "pipe:1")
	}
	cmd := exec.CommandContext(ctx, s.o.Prober.ffmpeg, args...) //nolint:gosec // the arguments are built here; the input is a spooled temp file or a pipe
	if src.stdin != nil {
		cmd.Stdin = src.stdin()
	}
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrBusy
		}
		return nil, ErrUnsupported
	}
	if len(out) < 30 || string(out[:4]) != "RIFF" || string(out[8:12]) != "WEBP" {
		return nil, ErrUnsupported
	}
	return out, nil
}

type AnimatedSticker struct {
	Data     []byte
	Duration time.Duration
}

func (s *Service) stickerSourceOf(ctx context.Context, path string, u Upload, webp bool) (stickerSource, *StickerCrop, time.Duration, error) {
	if webp {
		data, err := os.ReadFile(path) //nolint:gosec // a spooled temp file
		if err != nil {
			return stickerSource{}, nil, 0, err
		}
		clip, err := webpStickerFrames(data, u)
		if err != nil {
			return stickerSource{}, nil, 0, err
		}
		return framesStickerSource(clip.frames), nil, clip.length, nil
	}
	total, known := s.o.Prober.duration(ctx, path)
	start, length, err := clipWindow(u.StartMS, u.EndMS, total, known)
	if err != nil {
		return stickerSource{}, nil, 0, err
	}
	if known {
		length = min(length, total-start)
	}
	return fileStickerSource(path, start, length), u.Crop, length, nil
}

func (s *Service) processAnimatedSticker(ctx context.Context, path string, u Upload, webp bool, vs *variantSet) (AnimatedSticker, error) {
	if !validCrop(u.Crop) {
		return AnimatedSticker{}, fmt.Errorf("%w: crop_x, crop_y and crop_size must be fractions of the frame", ErrInvalid)
	}
	select {
	case s.images <- struct{}{}:
	case <-ctx.Done():
		return AnimatedSticker{}, ErrBusy
	}
	defer func() { <-s.images }()
	src, crop, length, err := s.stickerSourceOf(ctx, path, u, webp)
	if err != nil {
		return AnimatedSticker{}, err
	}
	var data []byte
	for _, step := range stickerLadder {
		data, err = s.encodeStickerClip(ctx, src, StickerSide, step, crop, false)
		if err != nil {
			return AnimatedSticker{}, err
		}
		if len(data) <= MaxAnimatedStickerBytes {
			break
		}
	}
	if len(data) > MaxAnimatedStickerBytes {
		return AnimatedSticker{}, ErrTooLarge
	}
	if err := s.animatedStickerVariants(ctx, src, crop, vs); err != nil {
		return AnimatedSticker{}, err
	}
	return AnimatedSticker{Data: data, Duration: length}, nil
}

func (s *Service) animatedStickerVariants(ctx context.Context, src stickerSource, crop *StickerCrop, vs *variantSet) error {
	if panel, err := s.encodeStickerClip(ctx, src, PanelSide, stickerStep{80, 1}, crop, true); err == nil {
		vs.add(VariantNamePanel, EncodedImage{Data: panel, ContentType: "image/webp", Width: PanelSide, Height: PanelSide})
	} else if !errors.Is(err, ErrUnsupported) {
		return err
	}
	s.animatedMessageVariant(ctx, src, crop, vs)
	return nil
}

func (s *Service) animatedMessageVariant(ctx context.Context, src stickerSource, crop *StickerCrop, vs *variantSet) {
	for _, step := range stickerLadder[1:] {
		small, err := s.encodeStickerClip(ctx, src, MessageStickerSide, step, crop, false)
		if err != nil {
			return
		}
		if len(small) <= MaxMessageStickerBytes {
			vs.add(VariantNameMessage, EncodedImage{Data: small, ContentType: "image/webp", Width: MessageStickerSide, Height: MessageStickerSide})
			return
		}
	}
}
