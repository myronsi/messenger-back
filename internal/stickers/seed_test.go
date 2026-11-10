package stickers_test

import (
	"context"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/myronsi/messenger-back/internal/stickers"
	"github.com/myronsi/messenger-back/internal/testenv"
)

func TestBuiltinPacksAreValid(t *testing.T) {
	fsys := stickers.Builtin()
	files, err := fs.Glob(fsys, "*/*.webp")
	if err != nil || len(files) == 0 {
		t.Fatalf("no built-in stickers: %v", err)
	}
	for _, name := range files {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatal(err)
		}
		img, err := stickers.ProbeWebP(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if img.Width != 512 || img.Height != 512 || !img.Animated {
			t.Fatalf("%s: %+v", name, img)
		}
	}
}

func TestProbeWebPRejectsOtherImages(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n0000000000000000000000000000000000")
	if _, err := stickers.ProbeWebP(png); err == nil {
		t.Fatal("a PNG passed as WebP")
	}
}

type puts struct {
	mu   sync.Mutex
	keys []string
}

func (p *puts) put(_ context.Context, key string, _ []byte, contentType string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if contentType != "image/webp" {
		panic(contentType)
	}
	p.keys = append(p.keys, key)
	return nil
}

func packFS(t *testing.T, manifest string, keys ...string) fstest.MapFS {
	t.Helper()
	img, err := fs.ReadFile(stickers.Builtin(), "noto-animated/1f600.webp")
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{"party/pack.json": {Data: []byte(manifest)}}
	for _, k := range keys {
		out["party/"+k+".webp"] = &fstest.MapFile{Data: img}
	}
	return out
}

func TestSeedIsIdempotentAndRetiresRemovedStickers(t *testing.T) {
	ctx := t.Context()
	store := testenv.Postgres(t)
	p := &puts{}
	deps := stickers.SeedDeps{Stickers: store.Stickers(), Attachments: store.Attachments(), Put: p.put}
	svc := stickers.New(store.Stickers())

	two := packFS(t, `{"key":"party","title":"Party","default":true,"stickers":[{"key":"a","emoji":"🎉"},{"key":"b","emoji":"👍"}]}`, "a", "b")
	for range 2 {
		if err := stickers.Seed(ctx, deps, two); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.keys) != 2 {
		t.Fatalf("a second seed must not upload again: %v", p.keys)
	}
	packs, err := svc.Installed(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].Title != "Party" || len(packs[0].Stickers) != 2 || !packs[0].Stickers[0].Animated {
		t.Fatalf("installed: %+v", packs)
	}
	if packs[0].Stickers[0].Emoji != "🎉" || packs[0].Stickers[0].Image.Purpose != "sticker" {
		t.Fatalf("first sticker: %+v", packs[0].Stickers[0])
	}
	keep := packs[0].Stickers[1]

	one := packFS(t, `{"key":"party","title":"Party time","default":true,"stickers":[{"key":"b","emoji":"👌"}]}`, "b")
	if err := stickers.Seed(ctx, deps, one); err != nil {
		t.Fatal(err)
	}
	pack, err := svc.Pack(ctx, 1, packs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if pack.Title != "Party time" || len(pack.Stickers) != 1 || pack.Stickers[0].ID != keep.ID || pack.Stickers[0].Emoji != "👌" {
		t.Fatalf("after the manifest changed: %+v", pack)
	}
	found, err := store.Stickers().Stickers(ctx, []int64{packs[0].Stickers[0].ID})
	if err != nil || !found[packs[0].Stickers[0].ID].Deleted {
		t.Fatalf("a removed sticker stays readable for sent messages, marked deleted: %+v %v", found, err)
	}

	other, err := fs.ReadFile(stickers.Builtin(), "noto-animated/1f44d.webp")
	if err != nil {
		t.Fatal(err)
	}
	one["party/b.webp"] = &fstest.MapFile{Data: other}
	if err := stickers.Seed(ctx, deps, one); err != nil {
		t.Fatal(err)
	}
	if len(p.keys) != 3 {
		t.Fatalf("a changed image must be uploaded: %v", p.keys)
	}
	pack, err = svc.Pack(ctx, 1, packs[0].ID)
	if err != nil || len(pack.Stickers) != 1 || pack.Stickers[0].ID == keep.ID || pack.Stickers[0].Image.Size != int64(len(other)) {
		t.Fatalf("after the image changed: %+v %v", pack, err)
	}
	found, err = store.Stickers().Stickers(ctx, []int64{keep.ID})
	if err != nil || !found[keep.ID].Deleted || found[keep.ID].Image.Size == int64(len(other)) {
		t.Fatalf("the old version keeps its image for sent messages: %+v %v", found, err)
	}
}

func TestSeedRejectsBadManifests(t *testing.T) {
	ctx := t.Context()
	store := testenv.Postgres(t)
	deps := stickers.SeedDeps{Stickers: store.Stickers(), Attachments: store.Attachments(), Put: (&puts{}).put}
	for name, fsys := range map[string]fstest.MapFS{
		"key differs from folder": packFS(t, `{"key":"other","title":"x","stickers":[{"key":"a","emoji":"🎉"}]}`, "a"),
		"missing image":           packFS(t, `{"key":"party","title":"x","stickers":[{"key":"a","emoji":"🎉"}]}`),
		"no emoji":                packFS(t, `{"key":"party","title":"x","stickers":[{"key":"a","emoji":""}]}`, "a"),
		"unknown field":           packFS(t, `{"key":"party","title":"x","author":"me","stickers":[{"key":"a","emoji":"🎉"}]}`, "a"),
	} {
		if err := stickers.Seed(ctx, deps, fsys); err == nil {
			t.Errorf("%s: seeded", name)
		}
	}
}

func TestBuiltinPacksSeed(t *testing.T) {
	store := testenv.Postgres(t)
	p := &puts{}
	deps := stickers.SeedDeps{Stickers: store.Stickers(), Attachments: store.Attachments(), Put: p.put}
	if err := stickers.Seed(t.Context(), deps, stickers.Builtin()); err != nil {
		t.Fatal(err)
	}
	packs, err := stickers.New(store.Stickers()).Installed(t.Context(), 1)
	if err != nil || len(packs) != 1 || packs[0].Title != "Noto Animated Emoji" || len(packs[0].Stickers) != 16 {
		t.Fatalf("built-in packs: %+v %v", packs, err)
	}
	if len(p.keys) != 16 {
		t.Fatalf("uploads: %d", len(p.keys))
	}
}
