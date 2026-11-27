package media

import (
	"errors"
	"testing"
	"time"
)

func TestWebPStickerFrames(t *testing.T) {
	src := fixture(t, "animated.webp")
	ms := func(v int) *int { return &v }

	clip, err := webpStickerFrames(src, Upload{})
	if err != nil {
		t.Fatal(err)
	}
	if clip.length <= 0 || clip.length > MaxStickerClip || len(clip.frames) != sampleCount(clip.length) {
		t.Fatalf("length %v with %d frames", clip.length, len(clip.frames))
	}
	for i, f := range clip.frames {
		if f == nil || f.Bounds().Dx() != StickerSide || f.Bounds().Dy() != StickerSide {
			t.Fatalf("frame %d: %v", i, f)
		}
	}
	if a := clip.frames[0].NRGBAAt(StickerSide/2, 2).A; a != 0 {
		t.Fatalf("a wide WebP needs a transparent border above it, alpha %d", a)
	}
	if a := clip.frames[0].NRGBAAt(StickerSide/2, StickerSide/2).A; a != 0xff {
		t.Fatalf("centre alpha %d", a)
	}

	cropped, err := webpStickerFrames(src, Upload{Crop: &StickerCrop{X: 0, Y: 0, Size: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if a := cropped.frames[0].NRGBAAt(StickerSide/2, 2).A; a != 0xff {
		t.Fatalf("a square crop fills the sticker, alpha %d", a)
	}

	half := clip.length / 2
	later, err := webpStickerFrames(src, Upload{StartMS: ms(int(half / time.Millisecond))})
	if err != nil || later.length != clip.length-half.Truncate(time.Millisecond) {
		t.Fatalf("from the middle: %v %v", later.length, err)
	}
	if _, err := webpStickerFrames(src, Upload{StartMS: ms(int(clip.length/time.Millisecond) + 1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("start after the end: %v", err)
	}
	if _, err := webpStickerFrames(src[:len(src)-40], Upload{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("truncated: %v", err)
	}
}

func TestWebPFrameTime(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{0: defaultWebPFrameTime, 10 * time.Millisecond: defaultWebPFrameTime, 40 * time.Millisecond: 40 * time.Millisecond} {
		if got := webpFrameTime(in); got != want {
			t.Errorf("%v: got %v", in, got)
		}
	}
}
