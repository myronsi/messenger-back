package media_test

import (
	"bytes"
	"context"
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
