package media_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"testing"

	"github.com/myronsi/messenger-back/internal/media"
)

func gifBytes(t *testing.T, side, frames int) []byte {
	t.Helper()
	g := &gif.GIF{}
	for i := range frames {
		img := image.NewPaletted(image.Rect(0, 0, side, side), color.Palette{color.Black, color.White})
		img.SetColorIndex(i%side, 0, 1)
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEmojiUploads(t *testing.T) {
	svc := memService(newMemStorage(0), newMemAttachments(), nil)
	still, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeEmoji, Filename: "e.jpg", Body: bytes.NewReader(photoJPEG(t, 400, 200))})
	if err != nil || still.MimeType != "image/png" || *still.Width != media.EmojiSide || *still.Height != media.EmojiSide {
		t.Fatalf("still emoji %+v %v", still, err)
	}
	anim, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeEmoji, Filename: "e.gif", Body: bytes.NewReader(gifBytes(t, 64, 3))})
	if err != nil || anim.MimeType != "image/gif" || *anim.Width != 64 {
		t.Fatalf("animated emoji %+v %v", anim, err)
	}
	if _, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeEmoji, Body: bytes.NewReader(gifBytes(t, 200, 2))}); !errors.Is(err, media.ErrImageTooLarge) {
		t.Fatalf("big gif: %v", err)
	}
	if _, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeEmoji, Body: bytes.NewReader([]byte("%PDF-1.7 not an image"))}); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("pdf: %v", err)
	}
	big := append(gifBytes(t, 64, 1), make([]byte, media.MaxEmojiBytes)...)
	if _, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeEmoji, Body: bytes.NewReader(big)}); !errors.Is(err, media.ErrTooLarge) {
		t.Fatalf("over 256 KB: %v", err)
	}
	if _, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeEmoji, Kind: media.KindAudio, Body: bytes.NewReader(photoJPEG(t, 10, 10))}); !errors.Is(err, media.ErrInvalid) {
		t.Fatalf("emoji as audio: %v", err)
	}
}
