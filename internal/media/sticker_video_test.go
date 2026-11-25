package media_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/myronsi/messenger-back/internal/media"
)

func generatedClip(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("ffmpeg", append(append([]string{"-v", "error", "-y"}, args...), out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make %s: %v %s", name, err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func animatedWebP(data []byte) bool {
	return len(data) > 20 && string(data[12:16]) == "VP8X" && data[20]&0x02 != 0
}

func TestStickersFromVideosAndGIFs(t *testing.T) {
	svc, store, _, _ := optimizingService(t)
	ctx := t.Context()
	mp4 := generatedClip(t, "clip.mp4", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=30:duration=5", "-pix_fmt", "yuv420p", "-c:v", "libx264")
	gif := generatedClip(t, "clip.gif", "-f", "lavfi", "-i", "testsrc=size=200x200:rate=10:duration=1")
	ms := func(v int) *int { return &v }

	a, err := svc.Upload(ctx, media.Upload{UploaderID: 1, Purpose: media.PurposeSticker, Kind: media.KindVideo, Filename: "clip.mp4", Body: bytes.NewReader(mp4),
		StartMS: ms(1000), EndMS: ms(3000), Crop: &media.StickerCrop{X: 0.25, Y: 0, Size: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if a.MimeType != "image/webp" || a.Kind != media.KindImage || a.Duration == nil || *a.Duration < 1.9 || *a.Duration > 2.1 || *a.Width != media.StickerSide {
		t.Fatalf("video sticker: %+v duration %v", a, a.Duration)
	}
	if !animatedWebP(store.get(t, a.StorageKey)) || len(store.get(t, a.StorageKey)) > media.MaxAnimatedStickerBytes {
		t.Fatal("the sticker must be an animated WebP of at most 1 MB")
	}
	panel, ok := a.Variants.Get(media.VariantNamePanel)
	if !ok || panel.ContentType != "image/webp" || animatedWebP(store.get(t, panel.Key)) {
		t.Fatalf("panel variant must be a still WebP: %+v %v", panel, ok)
	}
	if msg, ok := a.Variants.Get(media.VariantNameMessage); !ok || msg.Width != media.MessageStickerSide || !animatedWebP(store.get(t, msg.Key)) {
		t.Fatalf("message variant: %+v %v", msg, ok)
	}

	g, err := svc.Upload(ctx, media.Upload{UploaderID: 1, Purpose: media.PurposeSticker, Filename: "clip.gif", Body: bytes.NewReader(gif)})
	if err != nil || g.MimeType != "image/webp" || g.Duration == nil {
		t.Fatalf("GIF sticker: %+v %v", g, err)
	}

	for name, u := range map[string]media.Upload{
		"too long":       {StartMS: ms(0), EndMS: ms(4000)},
		"end before":     {StartMS: ms(2000), EndMS: ms(1000)},
		"start too late": {StartMS: ms(9000)},
		"bad crop":       {Crop: &media.StickerCrop{X: 0, Y: 0, Size: 0}},
	} {
		u.UploaderID, u.Purpose, u.Kind, u.Filename, u.Body = 1, media.PurposeSticker, media.KindVideo, "clip.mp4", bytes.NewReader(mp4)
		if _, err := svc.Upload(ctx, u); !errors.Is(err, media.ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	png := transparentPNG(t, 300, 200)
	if _, err := svc.Upload(ctx, media.Upload{UploaderID: 1, Purpose: media.PurposeSticker, Filename: "s.png", Body: bytes.NewReader(png), StartMS: ms(0)}); !errors.Is(err, media.ErrInvalid) {
		t.Fatalf("trim on an image sticker: %v", err)
	}
}

func (m *memStorage) get(t *testing.T, key string) []byte {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		t.Fatalf("no object %s", key)
	}
	return data
}
