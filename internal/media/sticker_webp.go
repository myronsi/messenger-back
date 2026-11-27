package media

import (
	"errors"
	"image"
	"image/draw"
	"time"
)

const (
	webpStickerFPS       = 30
	shortestWebPFrame    = 10 * time.Millisecond
	defaultWebPFrameTime = 100 * time.Millisecond
)

type webpStickerClip struct {
	frames []*image.NRGBA
	length time.Duration
}

func webpFrameTime(d time.Duration) time.Duration {
	if d <= shortestWebPFrame {
		return defaultWebPFrameTime
	}
	return d
}

func sampleTime(k int) time.Duration {
	return time.Duration(k) * time.Second / webpStickerFPS
}

func sampleCount(length time.Duration) int {
	return max(1, int((length*webpStickerFPS+time.Second-1)/time.Second))
}

func cropRect(b image.Rectangle, c *StickerCrop) image.Rectangle {
	if c == nil {
		return b
	}
	size := max(1, int(float64(min(b.Dx(), b.Dy()))*c.Size))
	x := min(int(float64(b.Dx())*c.X), b.Dx()-size)
	y := min(int(float64(b.Dy())*c.Y), b.Dy()-size)
	return image.Rect(b.Min.X+x, b.Min.Y+y, b.Min.X+x+size, b.Min.Y+y+size)
}

func webpStickerFrames(data []byte, u Upload) (webpStickerClip, error) {
	start, length, err := clipWindow(u.StartMS, u.EndMS, 0, false)
	if err != nil {
		return webpStickerClip{}, err
	}
	samples := make([]*image.NRGBA, sampleCount(length))
	var canvas *image.NRGBA
	var at time.Duration
	next := 0
	_, err = sanitizeAnimatedWebP(data, func(w, h int, f webpFrame, img image.Image) error {
		if canvas == nil {
			canvas = image.NewNRGBA(image.Rect(0, 0, w, h))
		}
		r := image.Rect(f.x, f.y, f.x+f.width, f.y+f.height)
		op := draw.Src
		if f.blend {
			op = draw.Over
		}
		draw.Draw(canvas, r, img, img.Bounds().Min, op)
		at += webpFrameTime(f.duration)
		var fitted *image.NRGBA
		for next < len(samples) && start+sampleTime(next) < at {
			if fitted == nil {
				fitted = fitSquare(canvas, cropRect(canvas.Bounds(), u.Crop), StickerSide)
			}
			samples[next] = fitted
			next++
		}
		if f.dispose {
			draw.Draw(canvas, r, image.Transparent, image.Point{}, draw.Src)
		}
		return nil
	})
	if errors.Is(err, ErrImageTooLarge) {
		return webpStickerClip{}, err
	}
	if err != nil {
		return webpStickerClip{}, ErrUnsupported
	}
	if _, _, err := clipWindow(u.StartMS, u.EndMS, at, true); err != nil {
		return webpStickerClip{}, err
	}
	length = min(length, at-start)
	return webpStickerClip{frames: samples[:sampleCount(length)], length: length}, nil
}
