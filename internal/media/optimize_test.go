package media_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func ffmpegProber(t *testing.T) *media.Prober {
	t.Helper()
	p := media.NewProber("", "")
	if !p.Available() {
		t.Skip("ffmpeg and ffprobe are not installed")
	}
	return p
}

func optimizingService(t *testing.T) (*media.Service, *memStorage, *memAttachments, *memJobs) {
	store, rows, jobs := newMemStorage(0), newMemAttachments(), &memJobs{}
	svc := media.NewService(media.Options{
		Storage: store, Attachments: rows, Prober: ffmpegProber(t), Jobs: jobs, Sessions: newMemSessions(),
	})
	return svc, store, rows, jobs
}

func readObject(t *testing.T, d media.Download) []byte {
	t.Helper()
	defer func() { _ = d.Object.Body.Close() }()
	data, err := io.ReadAll(d.Object.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPhotoUploadStoresWebPThumbnailDisplayAndPlaceholder(t *testing.T) {
	svc, store, _, _ := optimizingService(t)
	a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "p.jpg", Body: bytes.NewReader(photoJPEG(t, 2400, 1600))})
	if err != nil {
		t.Fatal(err)
	}
	if a.ThumbnailKey == nil || !strings.HasSuffix(*a.ThumbnailKey, ".thumb.webp") {
		t.Fatalf("thumbnail key %v", a.ThumbnailKey)
	}
	display, ok := a.Variants.Get(media.VariantNameDisplay)
	if !ok || display.ContentType != "image/webp" || display.Width != media.DisplaySide || display.Height != 1066 {
		t.Fatalf("display %+v %v", display, ok)
	}
	if _, ok := store.keys()[display.Key]; !ok {
		t.Fatal("display object not stored")
	}
	if a.Placeholder == nil || *a.Placeholder == "" {
		t.Fatal("no placeholder")
	}
	d, err := svc.Open(t.Context(), 1, a.ID, media.VariantDisplay)
	if err != nil || d.ContentType != "image/webp" {
		t.Fatalf("open display %v %s", err, d.ContentType)
	}
	if data := readObject(t, d); string(data[8:12]) != "WEBP" {
		t.Fatal("display is not a WebP")
	}
	thumb, err := svc.Open(t.Context(), 1, a.ID, media.VariantThumbnail)
	if err != nil || thumb.ContentType != "image/webp" {
		t.Fatalf("open thumbnail %v %s", err, thumb.ContentType)
	}
	_ = thumb.Object.Body.Close()
}

func transparentPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 200, A: uint8((x + y) % 256)})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAvatarAndStickerUploadsStoreSizes(t *testing.T) {
	svc, _, _, _ := optimizingService(t)
	avatar, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeAvatar, Filename: "a.jpg", Body: bytes.NewReader(photoJPEG(t, 512, 512))})
	if err != nil {
		t.Fatal(err)
	}
	for _, side := range []int{64, 128, 256} {
		v, ok := avatar.Variants.Get(media.AvatarVariant(side))
		if !ok || v.Width != side || v.ContentType != "image/webp" {
			t.Fatalf("avatar %d: %+v %v", side, v, ok)
		}
	}
	d, err := svc.OpenUnchecked(t.Context(), avatar.ID, media.AvatarSizeVariant(100))
	if err != nil || d.ETagSuffix != "-avatar_128" {
		t.Fatalf("open avatar size %v %q", err, d.ETagSuffix)
	}
	_ = d.Object.Body.Close()

	sticker, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeSticker, Filename: "s.png", Body: bytes.NewReader(transparentPNG(t, 300, 200))})
	if err != nil {
		t.Fatal(err)
	}
	panel, ok := sticker.Variants.Get(media.VariantNamePanel)
	message, ok2 := sticker.Variants.Get(media.VariantNameMessage)
	if !ok || !ok2 || panel.Width != media.PanelSide || message.Width != media.MessageStickerSide {
		t.Fatalf("sticker variants %+v %+v", panel, message)
	}
}

func TestWebPPhotoStaysWebP(t *testing.T) {
	svc, _, _, _ := optimizingService(t)
	src := filepath.Join(t.TempDir(), "in.png")
	if err := os.WriteFile(src, photoPNG(t, 800, 600), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "in.webp")
	if err := exec.Command("ffmpeg", "-v", "error", "-y", "-i", src, "-c:v", "libwebp", "-quality", "80", out).Run(); err != nil {
		t.Skipf("ffmpeg cannot write WebP: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "x.webp", Body: bytes.NewReader(data)})
	if err != nil {
		t.Fatal(err)
	}
	if a.MimeType != "image/webp" || !strings.HasSuffix(a.Filename, ".webp") {
		t.Fatalf("stored %s %s", a.MimeType, a.Filename)
	}
}

func photoPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img, err := jpegDecode(photoJPEG(t, w, h))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegDecode(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func testVideo(t *testing.T, codec string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "in.mp4")
	cmd := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:v", codec, "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", out)
	if err := cmd.Run(); err != nil {
		t.Skipf("ffmpeg cannot write %s: %v", codec, err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestVideoJobMakesPlayableCopy(t *testing.T) {
	svc, store, rows, jobs := optimizingService(t)
	a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "clip.mp4", Body: bytes.NewReader(testVideo(t, "mpeg4"))})
	if err != nil {
		t.Fatal(err)
	}
	if a.ProcessingState == nil || *a.ProcessingState != media.ProcessingQueued || len(jobs.jobs) != 1 || jobs.jobs[0].Kind != media.JobVideo {
		t.Fatalf("state %v jobs %+v", a.ProcessingState, jobs.jobs)
	}
	if a.Placeholder == nil {
		t.Fatal("video without placeholder")
	}
	done, err := svc.RunJob(t.Context(), jobs.jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	opt, ok := done.Variants.Get(media.VariantNameOptimized)
	if !ok || opt.ContentType != "video/mp4" || *done.ProcessingState != media.ProcessingDone {
		t.Fatalf("after job %+v %v", opt, done.ProcessingState)
	}
	optimized := store.objects[opt.Key]
	info, err := ffmpegProber(t).StreamInfo(t.Context(), writeTemp(t, optimized))
	if err != nil || info.VideoCodec != "h264" || info.AudioCodec != "aac" {
		t.Fatalf("optimized %+v %v", info, err)
	}
	if !media.HasOptimized(rows.rows[a.ID]) {
		t.Fatal("row lacks the optimized copy")
	}
	d, err := svc.Open(t.Context(), 1, a.ID, media.VariantOriginal)
	if err != nil || d.ETagSuffix != "-optimized" || d.ContentType != "video/mp4" {
		t.Fatalf("default content %v %+v", err, d)
	}
	_ = d.Object.Body.Close()
	src, err := svc.Open(t.Context(), 1, a.ID, media.VariantSource)
	if err != nil || len(readObject(t, src)) != int(a.Size) {
		t.Fatalf("source %v", err)
	}
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVideoAlreadyEfficientIsSkipped(t *testing.T) {
	svc, _, _, jobs := optimizingService(t)
	a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "clip.mp4", Body: bytes.NewReader(faststart(t, testVideo(t, "libx264")))})
	if err != nil {
		t.Fatal(err)
	}
	done, err := svc.RunJob(t.Context(), jobs.jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	if media.HasOptimized(done) || *done.ProcessingState != media.ProcessingDone {
		t.Fatalf("efficient video got a copy: %+v", done.Variants)
	}
	_ = a
}

func faststart(t *testing.T, data []byte) []byte {
	t.Helper()
	in := writeTemp(t, data)
	out := in + ".mp4"
	if err := exec.Command("ffmpeg", "-v", "error", "-y", "-i", in, "-c", "copy", "-movflags", "+faststart", out).Run(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAudioUploadReadsTagsAndLosslessIsTranscoded(t *testing.T) {
	svc, _, _, jobs := optimizingService(t)
	out := filepath.Join(t.TempDir(), "song.flac")
	cmd := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "anoisesrc=d=4:c=pink:a=0.3", "-ac", "2", "-ar", "44100",
		"-metadata", "title=Morning", "-metadata", "artist=Band", "-metadata", "album=First", "-c:a", "flac", out)
	if err := cmd.Run(); err != nil {
		t.Skipf("ffmpeg cannot write FLAC: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	a, tags, err := svc.UploadWithTags(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Kind: media.KindAudio, Filename: "song.flac", Body: bytes.NewReader(data)})
	if errors.Is(err, media.ErrUnsupported) {
		t.Skip("FLAC is not an accepted audio container here")
	}
	if err != nil {
		t.Fatal(err)
	}
	if a.AudioTitle == nil || *a.AudioTitle != "Morning" || a.AudioAlbum == nil || *a.AudioAlbum != "First" || tags.Artist == nil {
		t.Fatalf("tags %v %v %+v", a.AudioTitle, a.AudioAlbum, tags)
	}
	if len(jobs.jobs) != 1 || jobs.jobs[0].Kind != media.JobAudio {
		t.Fatalf("jobs %+v", jobs.jobs)
	}
	done, err := svc.RunJob(t.Context(), jobs.jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	if opt, ok := done.Variants.Get(media.VariantNameOptimized); !ok || opt.ContentType != "audio/mp4" || opt.Size >= a.Size {
		t.Fatalf("optimized %+v %v (source %d)", opt, ok, a.Size)
	}
}

func TestGIFJobMakesAnimation(t *testing.T) {
	svc, _, _, jobs := optimizingService(t)
	out := filepath.Join(t.TempDir(), "a.gif")
	if err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=200x150:rate=10:duration=2", out).Run(); err != nil {
		t.Skipf("ffmpeg cannot write GIF: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "a.gif", Body: bytes.NewReader(data)})
	if err != nil {
		t.Fatal(err)
	}
	if a.MimeType != "image/gif" || len(jobs.jobs) != 1 || jobs.jobs[0].Kind != media.JobGIF {
		t.Fatalf("gif %s jobs %+v", a.MimeType, jobs.jobs)
	}
	done, err := svc.RunJob(t.Context(), jobs.jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	anim, ok := done.Variants.Get(media.VariantNameAnimation)
	if !ok || anim.ContentType != "video/mp4" || anim.Size >= a.Size {
		t.Fatalf("animation %+v %v (gif %d)", anim, ok, a.Size)
	}
	if d, err := svc.Open(t.Context(), 1, a.ID, media.VariantOriginal); err != nil || d.ContentType != "image/gif" {
		t.Fatalf("default content of a GIF changed: %v %s", err, d.ContentType)
	} else {
		_ = d.Object.Body.Close()
	}
}

func TestAVIFIsConvertedToAnImage(t *testing.T) {
	svc, _, _, _ := optimizingService(t)
	src := writeTemp(t, photoPNG(t, 400, 300))
	out := filepath.Join(t.TempDir(), "p.avif")
	if err := exec.Command("ffmpeg", "-v", "error", "-y", "-i", src, "-c:v", "libaom-av1", "-still-picture", "1", "-crf", "40", out).Run(); err != nil {
		t.Skipf("ffmpeg cannot write AVIF: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "IMG_1.avif", Body: bytes.NewReader(data)})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != media.KindImage || a.Width == nil || *a.Width != 400 || strings.HasSuffix(a.Filename, ".avif") {
		t.Fatalf("avif stored as %s %s %v", a.Kind, a.Filename, a.Width)
	}
}

func TestResumableUploadAssemblesChunks(t *testing.T) {
	svc, store, _, _ := optimizingService(t)
	data := photoJPEG(t, 3000, 3000)
	size := int64(len(data))
	sess, err := svc.CreateSession(t.Context(), media.NewSession{UploaderID: 7, Purpose: media.PurposeMessage, Filename: "big.jpg", Size: size})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutChunk(t.Context(), sess.ID, 7, 5, bytes.NewReader(data)); !errors.Is(err, media.ErrUploadOffset) {
		t.Fatalf("wrong offset accepted: %v", err)
	}
	if _, _, err := svc.CompleteSession(t.Context(), sess.ID, 7); !errors.Is(err, media.ErrUploadIncomplete) {
		t.Fatalf("incomplete upload completed: %v", err)
	}
	if _, err := svc.GetSession(t.Context(), sess.ID, 8); !errors.Is(err, media.ErrNoAccess) {
		t.Fatalf("other user sees the session: %v", err)
	}
	var offset int64
	for offset < size {
		end := min(offset+int64(sess.ChunkSize), size)
		next, err := svc.PutChunk(t.Context(), sess.ID, 7, offset, bytes.NewReader(data[offset:end]))
		if err != nil {
			t.Fatal(err)
		}
		offset = next.Received
	}
	a, _, err := svc.CompleteSession(t.Context(), sess.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != media.KindImage || *a.Width != 3000 {
		t.Fatalf("attachment %+v", a)
	}
	for k := range store.keys() {
		if strings.HasPrefix(k, "uploads/") {
			t.Fatalf("chunk left behind: %s", k)
		}
	}
	if _, err := svc.GetSession(t.Context(), sess.ID, 7); !errors.Is(err, media.ErrNoAccess) {
		t.Fatal("session kept after completion")
	}
}

func TestResumableUploadRefusesTooLarge(t *testing.T) {
	svc, _, _, _ := optimizingService(t)
	_, err := svc.CreateSession(t.Context(), media.NewSession{UploaderID: 7, Purpose: media.PurposeMessage, Filename: "x", Size: media.DefaultLimits.Max() + 1})
	if !errors.Is(err, media.ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	_, err = svc.CreateSession(t.Context(), media.NewSession{UploaderID: 7, Purpose: media.PurposeEmoji, Filename: "x", Size: 10})
	if !errors.Is(err, media.ErrInvalid) {
		t.Fatalf("emoji session: %v", err)
	}
}

var _ = postgres.Variants{}

func TestResumableUploadFreesIdleSessionsAtTheCap(t *testing.T) {
	store, rows, jobs := newMemStorage(0), newMemAttachments(), &memJobs{}
	sessions := newMemSessions()
	svc := media.NewService(media.Options{Storage: store, Attachments: rows, Prober: ffmpegProber(t), Jobs: jobs, Sessions: sessions})
	open := func() error {
		_, err := svc.CreateSession(t.Context(), media.NewSession{UploaderID: 7, Purpose: media.PurposeMessage, Filename: "big.mp4", Size: 20 << 20})
		return err
	}
	for range media.MaxOpenUploads {
		if err := open(); err != nil {
			t.Fatal(err)
		}
	}
	if err := open(); !errors.Is(err, media.ErrTooManyUploads) {
		t.Fatalf("sixth session while the others are active: %v", err)
	}
	sessions.age(media.UploadSessionIdle + time.Minute)
	if err := open(); err != nil {
		t.Fatalf("an idle session should make room: %v", err)
	}
	if n, _ := sessions.Count(t.Context(), 7); n != media.MaxOpenUploads {
		t.Fatalf("open sessions %d", n)
	}
	if _, err := svc.CreateSession(t.Context(), media.NewSession{UploaderID: 8, Purpose: media.PurposeMessage, Filename: "x", Size: 20 << 20}); err != nil {
		t.Fatalf("another user is not affected: %v", err)
	}
}
