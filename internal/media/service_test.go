package media_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/testenv"
)

// rules is a fake of the message side: which (viewer, chat, message) is visible and who is in which chat.
type rules struct {
	visible map[[3]int64]bool
	members map[[2]int64]bool
}

func TestVideoUploadStoresAPlayablePreview(t *testing.T) {
	prober := media.NewProber("", "")
	if !prober.Available() {
		t.Skip("ffmpeg and ffprobe are required")
	}
	pg := testenv.Postgres(t)
	ctx := t.Context()
	user, err := pg.Users().Create(ctx, "videopreview", "Preview", "hash")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	videoPath := dir + "/preview.mp4"
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=960x540:d=0.2",
		"-c:v", "mpeg4", "-threads", "1", videoPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create video fixture: %v %s", err, output)
	}
	video, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := media.NewDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := media.NewService(media.Options{Storage: disk, Attachments: pg.Attachments(), Prober: prober})
	file, err := svc.Upload(ctx, media.Upload{UploaderID: user.ID, Purpose: media.PurposeMessage, Filename: "preview.mp4", Body: bytes.NewReader(video)})
	if err != nil {
		t.Fatal(err)
	}
	if file.ThumbnailKey == nil || file.Width == nil || file.Height == nil || file.Kind != media.KindVideo {
		t.Fatalf("missing video preview metadata: %+v", file)
	}
	thumb, err := svc.Open(ctx, user.ID, file.ID, media.VariantThumbnail)
	if err != nil {
		t.Fatal(err)
	}
	defer thumb.Object.Body.Close()
	frame, err := jpeg.DecodeConfig(thumb.Object.Body)
	if err != nil || frame.Width != 640 || frame.Height != 360 {
		t.Fatalf("video frame = %+v, %v", frame, err)
	}
	original, err := svc.Open(ctx, user.ID, file.ID, media.VariantOriginal)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Object.Body.Close()
	data, err := io.ReadAll(original.Object.Body)
	if err != nil || !bytes.Equal(video, data) {
		t.Fatalf("original video was changed: %v", err)
	}
	legacy, err := pg.Attachments().Create(ctx, postgres.NewAttachment{
		UploaderID: &user.ID, Purpose: media.PurposeMessage, Kind: media.KindVideo,
		StorageKey: "attachments/legacy-video", Filename: "legacy.mp4", MimeType: "video/mp4", Size: int64(len(video)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Put(ctx, legacy.StorageKey, bytes.NewReader(video), int64(len(video)), legacy.MimeType); err != nil {
		t.Fatal(err)
	}
	legacyPreview, err := svc.Open(ctx, user.ID, legacy.ID, media.VariantThumbnail)
	if err != nil {
		t.Fatal(err)
	}
	defer legacyPreview.Object.Body.Close()
	if _, err := jpeg.DecodeConfig(legacyPreview.Object.Body); err != nil {
		t.Fatalf("legacy video preview: %v", err)
	}
}

func (r rules) VisibleAttachment(_ context.Context, viewer, chat, msg int64, _ uuid.UUID) (bool, error) {
	return r.visible[[3]int64{viewer, chat, msg}], nil
}

func (r rules) IsMember(_ context.Context, chat, user int64) (bool, error) {
	return r.members[[2]int64{chat, user}], nil
}

func pngFile(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 600, 300))
	for x := range 600 {
		img.Set(x, 10, color.RGBA{G: 255, A: 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUploadLinkAndAccess(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	reg := func(name string) int64 {
		u, err := pg.Users().Register(ctx, name, name, "hash")
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	alice, bob, eve := reg("alice"), reg("bob"), reg("eve")
	chat, _, _ := pg.Chats().CreateDirect(ctx, alice, bob)
	disk, err := media.NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := rules{visible: map[[3]int64]bool{}, members: map[[2]int64]bool{}}
	svc := media.NewService(media.Options{Storage: disk, Attachments: pg.Attachments(), Visibility: r, Membership: r, Prober: media.NewProber("/nonexistent", "/nonexistent")})

	a, err := svc.Upload(ctx, media.Upload{UploaderID: alice, Purpose: media.PurposeMessage, Filename: "../holiday.png", Body: bytes.NewReader(pngFile(t))})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != media.KindImage || a.MimeType != "image/png" || *a.Width != 600 || a.ThumbnailKey == nil || a.Filename != "holiday.png" || a.ChatID != nil {
		t.Fatalf("upload: %+v", a)
	}

	open := func(viewer int64, v media.Variant) error {
		d, err := svc.Open(ctx, viewer, a.ID, v)
		if err == nil {
			b, _ := io.ReadAll(d.Object.Body)
			_ = d.Object.Body.Close()
			if len(b) == 0 || !strings.HasPrefix(d.Disposition, "inline") {
				t.Fatalf("download: %d bytes, %q", len(b), d.Disposition)
			}
		}
		return err
	}
	// Before it is sent, only the uploader sees it.
	if err := open(alice, media.VariantOriginal); err != nil {
		t.Fatalf("uploader: %v", err)
	}
	if err := open(bob, media.VariantOriginal); !errors.Is(err, media.ErrNoAccess) {
		t.Fatalf("bob before the message: %v", err)
	}
	// Sent in the chat: members who can see the message may download it.
	if err := pg.Attachments().Link(ctx, a.ID, chat.ID, 77, nil); err != nil {
		t.Fatal(err)
	}
	r.visible[[3]int64{bob, chat.ID, 77}] = true
	if err := open(bob, media.VariantThumbnail); err != nil {
		t.Fatalf("bob after the message: %v", err)
	}
	// A non-member never, whatever link exists.
	if err := open(eve, media.VariantOriginal); !errors.Is(err, media.ErrNoAccess) {
		t.Fatalf("eve: %v", err)
	}
	// The message is deleted (or hidden for bob): no more access.
	r.visible[[3]int64{bob, chat.ID, 77}] = false
	if err := open(bob, media.VariantOriginal); !errors.Is(err, media.ErrNoAccess) {
		t.Fatalf("bob after the delete: %v", err)
	}
}

func TestDisplayCopyOfLargeImages(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	u, _ := pg.Users().Register(ctx, "photographer", "P", "hash")
	root := t.TempDir()
	disk, _ := media.NewDisk(root)
	svc := media.NewService(media.Options{Storage: disk, Attachments: pg.Attachments(), Prober: media.NewProber("/nonexistent", "/nonexistent")})
	upload := func(w, h int) postgres.Attachment { return uploadPNG(t, svc, u.ID, w, h) }
	display := func(a postgres.Attachment) (string, image.Config) {
		d, err := svc.Open(ctx, u.ID, a.ID, media.VariantDisplay)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = d.Object.Body.Close() }()
		cfg, _, err := image.DecodeConfig(d.Object.Body)
		if err != nil {
			t.Fatal(err)
		}
		return d.ContentType, cfg
	}

	large := upload(2400, 1200)
	if !media.HasDisplay(large) {
		t.Fatal("a 2400 px image has no display copy")
	}
	if ctype, cfg := display(large); ctype != "image/jpeg" || cfg.Width != media.DisplaySide || cfg.Height != 800 {
		t.Fatalf("display copy: %s %dx%d", ctype, cfg.Width, cfg.Height)
	}
	if err := disk.Delete(ctx, postgres.DisplayKey(large.StorageKey)); err != nil {
		t.Fatal(err)
	}
	if _, cfg := display(large); cfg.Width != media.DisplaySide {
		t.Fatalf("regenerated display copy: %dx%d", cfg.Width, cfg.Height)
	}
	if _, err := os.Stat(root + "/" + postgres.DisplayKey(large.StorageKey)); err != nil {
		t.Fatalf("regenerated display copy is not stored: %v", err)
	}

	small := upload(800, 600)
	if media.HasDisplay(small) {
		t.Fatal("an 800 px image has a display copy")
	}
	if ctype, cfg := display(small); ctype != "image/png" || cfg.Width != 800 {
		t.Fatalf("small image: %s %dx%d", ctype, cfg.Width, cfg.Height)
	}
}

func uploadPNG(t *testing.T, svc *media.Service, uploaderID int64, w, h int) postgres.Attachment {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	a, err := svc.Upload(context.Background(), media.Upload{UploaderID: uploaderID, Purpose: media.PurposeMessage, Filename: "photo.png", Body: &b})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

type hookedStorage struct {
	media.Storage
	onGet     func(key string, err error)
	beforePut func(key string)
	signErr   error
	open      atomic.Int32
}

func (h *hookedStorage) Get(ctx context.Context, key string) (media.Object, error) {
	obj, err := h.Storage.Get(ctx, key)
	if h.onGet != nil {
		h.onGet(key, err)
	}
	if err == nil {
		h.open.Add(1)
		obj.Body = closeCounter{obj.Body, &h.open}
	}
	return obj, err
}

func (h *hookedStorage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if h.beforePut != nil {
		h.beforePut(key)
	}
	return h.Storage.Put(ctx, key, r, size, contentType)
}

func (h *hookedStorage) SignedURL(ctx context.Context, key string, ttl time.Duration, contentType, disposition string) (string, error) {
	if h.signErr != nil {
		return "", h.signErr
	}
	return h.Storage.SignedURL(ctx, key, ttl, contentType, disposition)
}

type closeCounter struct {
	io.ReadCloser
	open *atomic.Int32
}

func (c closeCounter) Close() error {
	c.open.Add(-1)
	return c.ReadCloser.Close()
}

func TestConcurrentDisplayRequestsDecodeTheOriginalOnce(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	u, _ := pg.Users().Register(ctx, "photographer", "P", "hash")
	disk, _ := media.NewDisk(t.TempDir())
	store := &hookedStorage{Storage: disk}
	svc := media.NewService(media.Options{Storage: store, Attachments: pg.Attachments(), Prober: media.NewProber("/nonexistent", "/nonexistent"), ImageWorkers: 1})
	a := uploadPNG(t, svc, u.ID, 2400, 1200)
	if err := disk.Delete(ctx, postgres.DisplayKey(a.StorageKey)); err != nil {
		t.Fatal(err)
	}

	const requests = 3
	var misses, originals atomic.Int32
	store.onGet = func(key string, err error) {
		switch {
		case key == postgres.DisplayKey(a.StorageKey) && errors.Is(err, media.ErrNotFound):
			misses.Add(1)
		case key == a.StorageKey:
			originals.Add(1)
			for deadline := time.Now().Add(5 * time.Second); misses.Load() < requests && time.Now().Before(deadline); {
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			d, err := svc.Open(ctx, u.ID, a.ID, media.VariantDisplay)
			if err != nil || d.ContentType != "image/jpeg" {
				t.Errorf("display: %q %v", d.ContentType, err)
				return
			}
			_ = d.Object.Body.Close()
		})
	}
	wg.Wait()
	if n := originals.Load(); n != 1 {
		t.Fatalf("the original was read %d times", n)
	}
}

func TestDisplayCopyReadsStoredOriginalsAboveTheUploadLimit(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	u, _ := pg.Users().Register(ctx, "photographer", "P", "hash")
	disk, _ := media.NewDisk(t.TempDir())
	a := uploadPNG(t, media.NewService(media.Options{Storage: disk, Attachments: pg.Attachments()}), u.ID, 2400, 1200)
	if err := disk.Delete(ctx, postgres.DisplayKey(a.StorageKey)); err != nil {
		t.Fatal(err)
	}
	const limit = 100
	if a.Size <= limit {
		t.Fatalf("the stored original has only %d bytes", a.Size)
	}
	svc := media.NewService(media.Options{Storage: disk, Attachments: pg.Attachments(), Limits: media.Limits{Image: limit}})
	d, err := svc.Open(ctx, u.ID, a.ID, media.VariantDisplay)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Object.Body.Close()
	if d.ContentType != "image/jpeg" {
		t.Fatalf("served %s instead of the display copy", d.ContentType)
	}
}

func TestDisplayCopyOfADeletedAttachmentIsNotKept(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	u, _ := pg.Users().Register(ctx, "photographer", "P", "hash")
	root := t.TempDir()
	disk, _ := media.NewDisk(root)
	store := &hookedStorage{Storage: disk}
	svc := media.NewService(media.Options{Storage: store, Attachments: pg.Attachments()})
	a := uploadPNG(t, svc, u.ID, 2400, 1200)
	if err := disk.Delete(ctx, postgres.DisplayKey(a.StorageKey)); err != nil {
		t.Fatal(err)
	}
	store.beforePut = func(string) {
		keys, err := pg.Attachments().Delete(ctx, a.ID)
		if err != nil {
			t.Error(err)
		}
		for _, k := range keys {
			_ = disk.Delete(ctx, k)
		}
	}
	if _, err := svc.Open(ctx, u.ID, a.ID, media.VariantDisplay); !errors.Is(err, media.ErrNoAccess) {
		t.Fatalf("open: %v", err)
	}
	if _, err := os.Stat(root + "/" + postgres.DisplayKey(a.StorageKey)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the display copy outlived its attachment: %v", err)
	}
}

func TestDisplayCopyIsClosedWhenSigningFails(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	u, _ := pg.Users().Register(ctx, "photographer", "P", "hash")
	disk, _ := media.NewDisk(t.TempDir())
	store := &hookedStorage{Storage: disk, signErr: errors.New("sign")}
	svc := media.NewService(media.Options{Storage: store, Attachments: pg.Attachments(), SignedURLs: true})
	a := uploadPNG(t, svc, u.ID, 2400, 1200)
	if _, err := svc.Open(ctx, u.ID, a.ID, media.VariantDisplay); !errors.Is(err, store.signErr) {
		t.Fatalf("open: %v", err)
	}
	if n := store.open.Load(); n != 0 {
		t.Fatalf("%d objects left open", n)
	}
}

func TestUploadsAreCheckedByContent(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	u, _ := pg.Users().Register(ctx, "uploader", "U", "hash")
	disk, _ := media.NewDisk(t.TempDir())
	svc := media.NewService(media.Options{Storage: disk, Attachments: pg.Attachments(), Prober: media.NewProber("/nonexistent", "/nonexistent"),
		Limits: media.Limits{File: 64, Image: 1 << 20}})
	up := func(purpose, kind, name string, body []byte) (postgres.Attachment, error) {
		return svc.Upload(ctx, media.Upload{UploaderID: u.ID, Purpose: purpose, Kind: kind, Filename: name, Body: bytes.NewReader(body)})
	}
	// HTML named like an image is stored as an opaque download.
	a, err := up(media.PurposeMessage, "", "cat.png", []byte("<!doctype html><script>steal()</script>"))
	if err != nil || a.Kind != media.KindFile || a.MimeType != "application/octet-stream" {
		t.Fatalf("html: %+v %v", a, err)
	}
	if d, err := svc.Open(ctx, u.ID, a.ID, media.VariantOriginal); err != nil || d.ContentType != "application/octet-stream" || !strings.HasPrefix(d.Disposition, "attachment") {
		t.Fatalf("html download: %+v %v", d, err)
	} else {
		_ = d.Object.Body.Close()
	}
	if _, err := up(media.PurposeAvatar, "", "me.png", []byte("not an image")); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("avatar that is no image: %v", err)
	}
	if _, err := up(media.PurposeMessage, media.KindFile, "big.bin", bytes.Repeat([]byte{1}, 100)); !errors.Is(err, media.ErrTooLarge) {
		t.Fatalf("over the file limit: %v", err)
	}
	if _, err := up(media.PurposeMessage, "", "empty", nil); !errors.Is(err, media.ErrInvalid) {
		t.Fatalf("empty file: %v", err)
	}
	if _, err := up("banner", "", "x", []byte("x")); !errors.Is(err, media.ErrInvalid) {
		t.Fatalf("unknown purpose: %v", err)
	}
	// A voice message without ffprobe keeps the client's measurements, clamped.
	ms := 1500
	v, err := svc.Upload(ctx, media.Upload{UploaderID: u.ID, Purpose: media.PurposeMessage, Kind: media.KindVoice, Filename: "voice.ogg",
		Body: bytes.NewReader([]byte("OggS\x00\x02rest-of-a-voice-message")), DurationMS: &ms, Waveform: []int{10, 400}})
	if err != nil || v.Kind != media.KindVoice || v.MimeType != "audio/ogg" || v.Duration == nil || *v.Duration != 1.5 || len(v.Waveform) != 2 || v.Waveform[1] != 255 {
		t.Fatalf("voice: %+v %v", v, err)
	}
}

func TestUnusedUploadsAreCollected(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	u, _ := pg.Users().Register(ctx, "uploader", "U", "hash")
	other, _ := pg.Users().Register(ctx, "other", "O", "hash")
	chat, _, _ := pg.Chats().CreateDirect(ctx, u.ID, other.ID)
	disk, _ := media.NewDisk(t.TempDir())
	svc := media.NewService(media.Options{Storage: disk, Attachments: pg.Attachments(), Prober: media.NewProber("/nonexistent", "/nonexistent")})
	mk := func() postgres.Attachment {
		a, err := svc.Upload(ctx, media.Upload{UploaderID: u.ID, Purpose: media.PurposeMessage, Filename: "f.pdf", Body: strings.NewReader("%PDF-1.7 ...")})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	unused, used, avatar := mk(), mk(), mk()
	if err := pg.Attachments().Link(ctx, used.ID, chat.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := pg.Attachments().SetUserAvatar(ctx, u.ID, &avatar.ID); err != nil {
		t.Fatal(err)
	}
	// Age them past UnusedAfter.
	if _, err := pg.Pool().Exec(ctx, `UPDATE attachments SET created_at = $1`, time.Now().Add(-media.UnusedAfter-time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := svc.Collect(ctx, 100)
	if err != nil || n != 1 {
		t.Fatalf("collected %d: %v", n, err)
	}
	if _, err := pg.Attachments().Get(ctx, unused.ID); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("unused upload kept: %v", err)
	}
	if _, err := disk.Get(ctx, unused.StorageKey); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("unused object kept: %v", err)
	}
	for _, keep := range []postgres.Attachment{used, avatar} {
		if _, err := pg.Attachments().Get(ctx, keep.ID); err != nil {
			t.Fatalf("referenced upload deleted: %v", err)
		}
	}
	hist, err := pg.Attachments().AvatarHistory(ctx, u.ID, 0, 10)
	if err != nil || len(hist) != 1 || !hist[0].IsCurrent || hist[0].AttachmentID != avatar.ID {
		t.Fatalf("avatar history: %+v %v", hist, err)
	}
}

// With ffprobe and ffmpeg installed the server measures voice messages itself and ignores the client.
func TestVoiceMessagesAreMeasured(t *testing.T) {
	prober := media.NewProber("", "")
	if !prober.Available() {
		t.Skip("ffprobe/ffmpeg are not installed")
	}
	dir := t.TempDir()
	out := dir + "/voice.ogg"
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:a", "libopus", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot encode opus here: %v %s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	pg := testenv.Postgres(t)
	ctx := context.Background()
	u, _ := pg.Users().Register(ctx, "speaker", "S", "hash")
	disk, _ := media.NewDisk(t.TempDir())
	svc := media.NewService(media.Options{Storage: disk, Attachments: pg.Attachments(), Prober: prober})
	lie := 999_000
	a, err := svc.Upload(ctx, media.Upload{UploaderID: u.ID, Purpose: media.PurposeMessage, Kind: media.KindVoice, Filename: "v.ogg",
		Body: bytes.NewReader(data), DurationMS: &lie, Waveform: []int{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Duration == nil || *a.Duration < 1.9 || *a.Duration > 2.1 || len(a.Waveform) != media.WaveformBars || a.Waveform[media.WaveformBars/2] < 200 {
		t.Fatalf("measured %v s, %d bars", a.Duration, len(a.Waveform))
	}
	// Text pretending to be Ogg is refused once the tools look at it.
	if _, err := svc.Upload(ctx, media.Upload{UploaderID: u.ID, Purpose: media.PurposeMessage, Kind: media.KindVoice, Filename: "x.ogg",
		Body: strings.NewReader("OggS but nothing else")}); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("fake ogg: %v", err)
	}
}
