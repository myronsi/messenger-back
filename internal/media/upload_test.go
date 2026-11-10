package media_test

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func memService(store media.Storage, rows *memAttachments, prober *media.Prober) *media.Service {
	if prober == nil {
		prober = media.NewProber("/nonexistent", "/nonexistent")
	}
	return media.NewService(media.Options{Storage: store, Attachments: rows, Prober: prober})
}

func TestLargePhotoUploadStoresNoDisplayCopyUntilAsked(t *testing.T) {
	ctx := t.Context()
	store := newMemStorage(0)
	svc := memService(store, newMemAttachments(), nil)
	src := withExif(photoJPEG(t, 2400, 1600), 1)
	a, err := svc.Upload(ctx, media.Upload{UploaderID: 7, Purpose: media.PurposeMessage, Filename: "p.jpg", Body: bytes.NewReader(src)})
	if err != nil {
		t.Fatal(err)
	}
	keys := store.keys()
	if len(keys) != 2 || a.ThumbnailKey == nil || keys[a.StorageKey] == 0 || keys[*a.ThumbnailKey] == 0 {
		t.Fatalf("stored %v", keys)
	}
	if _, ok := keys[postgres.DisplayKey(a.StorageKey)]; ok {
		t.Fatal("a display copy was made at upload")
	}
	if !media.HasDisplay(a) {
		t.Fatal("a 2400 px photo has no display copy")
	}
	for range 2 {
		d, err := svc.Open(ctx, 7, a.ID, media.VariantDisplay)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := jpeg.DecodeConfig(d.Object.Body)
		_ = d.Object.Body.Close()
		if err != nil || cfg.Width != media.DisplaySide || cfg.Height != 1066 {
			t.Fatalf("display %+v %v", cfg, err)
		}
	}
	if store.keys()[postgres.DisplayKey(a.StorageKey)] == 0 {
		t.Fatal("the display copy was not stored")
	}
}

type barrierStorage struct {
	*memStorage
	mu      sync.Mutex
	started int
	all     chan struct{}
}

func (b *barrierStorage) Put(ctx context.Context, key string, r io.Reader, size int64, ctype string) error {
	b.mu.Lock()
	b.started++
	if b.started == 2 {
		close(b.all)
	}
	b.mu.Unlock()
	select {
	case <-b.all:
	case <-time.After(5 * time.Second):
		return errors.New("the uploads ran one after the other")
	}
	return b.memStorage.Put(ctx, key, r, size, ctype)
}

func TestOriginalAndThumbnailAreStoredAtOnce(t *testing.T) {
	store := &barrierStorage{memStorage: newMemStorage(0), all: make(chan struct{})}
	svc := memService(store, newMemAttachments(), nil)
	a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "p.jpg", Body: bytes.NewReader(photoJPEG(t, 640, 480))})
	if err != nil {
		t.Fatal(err)
	}
	if a.ThumbnailKey == nil || len(store.keys()) != 2 {
		t.Fatalf("stored %v", store.keys())
	}
}

func TestFailedPutRemovesEveryObject(t *testing.T) {
	boom := errors.New("s3 down")
	for _, failing := range []string{"original", "thumbnail", "record"} {
		t.Run(failing, func(t *testing.T) {
			store := newMemStorage(time.Millisecond)
			rows := newMemAttachments()
			switch failing {
			case "original":
				store.failPut = func(key string) error {
					if !strings.HasSuffix(key, ".thumb.jpg") {
						return boom
					}
					return nil
				}
			case "thumbnail":
				store.failPut = func(key string) error {
					if strings.HasSuffix(key, ".thumb.jpg") {
						return boom
					}
					return nil
				}
			case "record":
				rows.fail = boom
			}
			svc := memService(store, rows, nil)
			_, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "p.jpg", Body: bytes.NewReader(photoJPEG(t, 640, 480))})
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v", err)
			}
			if keys := store.keys(); len(keys) != 0 {
				t.Fatalf("left behind %v", keys)
			}
			if len(store.deleted) != 2 {
				t.Fatalf("deleted %v", store.deleted)
			}
		})
	}
}

func ffmpegFixture(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	out := t.TempDir() + "/" + name
	cmd := exec.CommandContext(t.Context(), "ffmpeg", append(append([]string{"-v", "error", "-y"}, args...), out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot make %s here: %v %s", name, err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestWebMIsClassifiedByItsStreams(t *testing.T) {
	prober := media.NewProber("", "")
	if !prober.Available() {
		t.Skip("ffprobe and ffmpeg are required")
	}
	audioOnly := ffmpegFixture(t, "a.webm", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "libopus")
	withVideo := ffmpegFixture(t, "v.webm", "-f", "lavfi", "-i", "color=c=red:s=64x64:d=0.5", "-f", "lavfi", "-i", "sine=duration=0.5",
		"-c:v", "libvpx", "-c:a", "libopus", "-shortest")
	if d, _ := media.Detect(audioOnly[:512], media.PurposeMessage, ""); d.ContentType != "video/webm" {
		t.Fatalf("the sniffer no longer takes audio-only WebM for video: %+v", d)
	}
	for _, c := range []struct {
		name, kind, wantKind, wantType string
		data                           []byte
		prober                         *media.Prober
	}{
		{"audio only", "", media.KindAudio, "audio/webm", audioOnly, prober},
		{"audio only sent as video", media.KindVideo, media.KindAudio, "audio/webm", audioOnly, prober},
		{"with video", "", media.KindVideo, "video/webm", withVideo, prober},
		{"audio only without ffprobe", "", media.KindVideo, "video/webm", audioOnly, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			svc := memService(newMemStorage(0), newMemAttachments(), c.prober)
			a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Kind: c.kind, Filename: "rec.webm", Body: bytes.NewReader(c.data)})
			if err != nil {
				t.Fatal(err)
			}
			if a.Kind != c.wantKind || a.MimeType != c.wantType {
				t.Fatalf("%s %s", a.Kind, a.MimeType)
			}
			if c.wantKind == media.KindAudio && (a.Duration == nil || *a.Duration < 0.9 || *a.Duration > 1.1 || a.ThumbnailKey != nil) {
				t.Fatalf("audio not measured: duration %v thumbnail %v", a.Duration, a.ThumbnailKey)
			}
		})
	}
}
