package media_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/stickers"
	"github.com/myronsi/messenger-back/internal/testenv"
)

func TestStickerImagesAreSharedAndKept(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	viewer, err := pg.Users().Register(ctx, "viewer", "V", "hash")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := media.NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := media.NewService(media.Options{Storage: disk, Attachments: pg.Attachments(), Prober: media.NewProber("/nonexistent", "/nonexistent")})
	deps := stickers.SeedDeps{Stickers: pg.Stickers(), Attachments: pg.Attachments()}
	img, err := fs.ReadFile(stickers.Builtin(), "noto-animated/1f600.webp")
	if err != nil {
		t.Fatal(err)
	}
	deps.Put = func(ctx context.Context, key string, data []byte, contentType string) error {
		return disk.Put(ctx, key, bytes.NewReader(data), int64(len(data)), contentType)
	}
	if err := stickers.Seed(ctx, deps, packOf(img)); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Pool().Exec(ctx, `UPDATE attachments SET created_at = $1`, time.Now().Add(-media.UnusedAfter-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.Collect(ctx, 100); err != nil || n != 0 {
		t.Fatalf("collected sticker images: %d %v", n, err)
	}
	packs, err := stickers.New(pg.Stickers()).Installed(ctx, viewer.ID)
	if err != nil || len(packs) != 1 || len(packs[0].Stickers) != 1 {
		t.Fatalf("packs: %+v %v", packs, err)
	}
	d, err := svc.Open(ctx, viewer.ID, packs[0].Stickers[0].Image.ID, media.VariantOriginal)
	if err != nil {
		t.Fatalf("a user who never saw the sticker cannot open it: %v", err)
	}
	b, _ := io.ReadAll(d.Object.Body)
	_ = d.Object.Body.Close()
	if len(b) != len(img) || d.ContentType != "image/webp" {
		t.Fatalf("sticker content: %d bytes, %s", len(b), d.ContentType)
	}
}

func packOf(img []byte) fstest.MapFS {
	return fstest.MapFS{
		"smile/pack.json": {Data: []byte(`{"key":"smile","title":"Smile","default":true,"stickers":[{"key":"a","emoji":"😀"}]}`)},
		"smile/a.webp":    {Data: img},
	}
}

func stickerPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{R: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestStickerImageFitsASquare(t *testing.T) {
	img, err := media.StickerImage(stickerPNG(t, 300, 150), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != media.StickerSide || b.Dy() != media.StickerSide {
		t.Fatalf("size: %v", b)
	}
	if img.NRGBAAt(256, 20).A != 0 || img.NRGBAAt(256, 256).A != 255 || img.NRGBAAt(5, 256).A != 255 {
		t.Fatal("a wide image fills the width and gets a transparent border above and below")
	}
	animated, err := fs.ReadFile(stickers.Builtin(), "noto-animated/1f600.webp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := media.StickerImage(animated, "image/webp"); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("animated WebP: %v", err)
	}
	if _, err := media.StickerImage([]byte("GIF89a"), "image/gif"); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("GIF: %v", err)
	}
}

func TestStickerUploads(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	alice, err := pg.Users().Register(ctx, "alice", "A", "hash")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := pg.Users().Register(ctx, "bob", "B", "hash")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := media.NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prober := media.NewProber("", "")
	svc := media.NewService(media.Options{Storage: disk, Attachments: pg.Attachments(), Prober: prober})

	a, err := svc.Upload(ctx, media.Upload{UploaderID: alice.ID, Purpose: media.PurposeSticker, Filename: "cat.png", Body: bytes.NewReader(stickerPNG(t, 900, 600))})
	if err != nil {
		t.Fatal(err)
	}
	want := "image/png"
	if prober.Available() {
		want = "image/webp"
	}
	if a.Purpose != "sticker" || a.MimeType != want || *a.Width != 512 || *a.Height != 512 || a.Size > media.MaxStickerBytes || a.ThumbnailKey != nil {
		t.Fatalf("sticker upload: %+v", a)
	}
	d, err := svc.Open(ctx, alice.ID, a.ID, media.VariantOriginal)
	if err != nil {
		t.Fatalf("the uploader opens it: %v", err)
	}
	data, _ := io.ReadAll(d.Object.Body)
	_ = d.Object.Body.Close()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 512 || cfg.Height != 512 || "image/"+format != want {
		t.Fatalf("stored image: %+v %s %v", cfg, format, err)
	}
	if _, err := svc.Open(ctx, bob.ID, a.ID, media.VariantOriginal); !errors.Is(err, media.ErrNoAccess) {
		t.Fatalf("an upload in no pack is private: %v", err)
	}
	pack, err := pg.Stickers().CreatePack(ctx, alice.ID, "Cats", "AAAAAAAAAAAA", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Stickers().AddSticker(ctx, alice.ID, pack.ID, a.ID, "🐱", 1); err != nil {
		t.Fatal(err)
	}
	shared, err := svc.Open(ctx, bob.ID, a.ID, media.VariantOriginal)
	if err != nil {
		t.Fatalf("a sticker in a pack is for everyone: %v", err)
	}
	_ = shared.Object.Body.Close()

	animated, _ := fs.ReadFile(stickers.Builtin(), "noto-animated/1f600.webp")
	for name, u := range map[string]media.Upload{
		"animated": {Body: bytes.NewReader(animated)},
		"text":     {Body: bytes.NewReader([]byte("just text, not an image at all"))},
		"voice":    {Kind: media.KindVoice, Body: bytes.NewReader(stickerPNG(t, 10, 10))},
	} {
		u.UploaderID, u.Purpose = alice.ID, media.PurposeSticker
		if _, err := svc.Upload(ctx, u); err == nil {
			t.Errorf("%s: uploaded as a sticker", name)
		}
	}
}
