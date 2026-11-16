package media_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/myronsi/messenger-back/internal/media"
)

func TestProfileMusicIsCutAndTagged(t *testing.T) {
	prober := media.NewProber("", "")
	if !prober.Available() {
		t.Skip("ffprobe and ffmpeg are required")
	}
	song := ffmpegFixture(t, "song.mp3", "-f", "lavfi", "-i", "sine=frequency=440:duration=45",
		"-metadata", "title=Test Song", "-metadata", "artist=Test Artist", "-c:a", "libmp3lame")
	svc := memService(newMemStorage(0), newMemAttachments(), prober)
	start, end := 5000, 20000
	a, tags, err := svc.UploadWithTags(t.Context(), media.Upload{
		UploaderID: 3, Purpose: media.PurposeProfileMusic, Filename: "song.mp3", Body: bytes.NewReader(song), StartMS: &start, EndMS: &end,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.MimeType != "audio/mp4" || a.Kind != media.KindAudio || a.Duration == nil || *a.Duration < 14.5 || *a.Duration > 15.5 {
		t.Fatalf("clip %s %s %v", a.MimeType, a.Kind, a.Duration)
	}
	if tags.Title == nil || *tags.Title != "Test Song" || tags.Artist == nil || *tags.Artist != "Test Artist" {
		t.Fatalf("tags %+v", tags)
	}

	whole, _, err := svc.UploadWithTags(t.Context(), media.Upload{UploaderID: 3, Purpose: media.PurposeProfileMusic, Filename: "song.mp3", Body: bytes.NewReader(song)})
	if err != nil || whole.Duration == nil || *whole.Duration > 30.5 {
		t.Fatalf("default clip %v %v", whole.Duration, err)
	}
	long := 40000
	if _, _, err := svc.UploadWithTags(t.Context(), media.Upload{UploaderID: 3, Purpose: media.PurposeProfileMusic, Body: bytes.NewReader(song), EndMS: &long}); !errors.Is(err, media.ErrInvalid) {
		t.Fatalf("40 s is too long: %v", err)
	}
}

func TestProfileMusicNeedsTheTools(t *testing.T) {
	svc := memService(newMemStorage(0), newMemAttachments(), nil)
	song := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 600)...)
	if _, err := svc.Upload(t.Context(), media.Upload{UploaderID: 3, Purpose: media.PurposeProfileMusic, Body: bytes.NewReader(song)}); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("without ffmpeg: %v", err)
	}
}

func TestProfileBackgroundUploads(t *testing.T) {
	svc := memService(newMemStorage(0), newMemAttachments(), nil)
	a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 4, Purpose: media.PurposeProfileBackground, Filename: "bg.jpg", Body: bytes.NewReader(photoJPEG(t, 1200, 400))})
	if err != nil || a.Kind != media.KindImage || a.ThumbnailKey == nil {
		t.Fatalf("image background %+v %v", a, err)
	}
	start := 0
	if _, err := svc.Upload(t.Context(), media.Upload{UploaderID: 4, Purpose: media.PurposeProfileBackground, Body: bytes.NewReader(photoJPEG(t, 64, 64)), StartMS: &start}); !errors.Is(err, media.ErrInvalid) {
		t.Fatalf("start_ms on a background: %v", err)
	}
	if _, err := svc.Upload(t.Context(), media.Upload{UploaderID: 4, Purpose: media.PurposeProfileBackground, Body: bytes.NewReader([]byte("%PDF-1.7 not a picture at all"))}); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("pdf background: %v", err)
	}

	prober := media.NewProber("", "")
	if !prober.Available() {
		t.Skip("ffprobe and ffmpeg are required for the video cases")
	}
	short := ffmpegFixture(t, "short.mp4", "-f", "lavfi", "-i", "color=c=blue:s=64x64:d=2", "-pix_fmt", "yuv420p")
	long := ffmpegFixture(t, "long.mp4", "-f", "lavfi", "-i", "color=c=blue:s=64x64:d=20", "-pix_fmt", "yuv420p")
	withTools := memService(newMemStorage(0), newMemAttachments(), prober)
	v, err := withTools.Upload(t.Context(), media.Upload{UploaderID: 4, Purpose: media.PurposeProfileBackground, Body: bytes.NewReader(short)})
	if err != nil || v.Kind != media.KindVideo || v.ThumbnailKey == nil {
		t.Fatalf("video background %+v %v", v, err)
	}
	if _, err := withTools.Upload(t.Context(), media.Upload{UploaderID: 4, Purpose: media.PurposeProfileBackground, Body: bytes.NewReader(long)}); !errors.Is(err, media.ErrInvalid) {
		t.Fatalf("20 s video: %v", err)
	}
}
