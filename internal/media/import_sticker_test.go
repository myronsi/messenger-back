package media_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/myronsi/messenger-back/internal/media"
)

func ffmpegSticker(t *testing.T, animated bool) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "s.webp")
	args := []string{"-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=512x512:rate=10:duration=1"}
	if animated {
		args = append(args, "-c:v", "libwebp_anim", "-loop", "0", "-quality", "40", out)
	} else {
		args = append(args, "-frames:v", "1", "-c:v", "libwebp", "-quality", "60", out)
	}
	if err := exec.Command("ffmpeg", args...).Run(); err != nil {
		t.Skipf("ffmpeg cannot write WebP: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestImportStickersDeferTheHeavyWork(t *testing.T) {
	for _, animated := range []bool{false, true} {
		name := map[bool]string{false: "still", true: "animated"}[animated]
		t.Run(name, func(t *testing.T) {
			svc, _, _, jobs := optimizingService(t)
			ctx := t.Context()
			data := ffmpegSticker(t, animated)
			a, err := svc.Upload(ctx, media.Upload{UploaderID: 1, Purpose: media.PurposeSticker, Filename: "STK-1.webp", Body: bytes.NewReader(data), ForImport: true})
			if err != nil {
				t.Fatal(err)
			}
			if a.ProcessingState == nil || *a.ProcessingState != media.ProcessingQueued || a.MimeType != "image/webp" || a.Width == nil || *a.Width != media.StickerSide {
				t.Fatalf("upload %+v", a)
			}
			if (a.Duration != nil) != animated {
				t.Fatalf("duration %v for %s", a.Duration, name)
			}
			if len(jobs.jobs) != 1 || jobs.jobs[0].Kind != media.JobImportSticker {
				t.Fatalf("jobs %+v", jobs.jobs)
			}
			if _, ok := a.Variants.Get(media.VariantNamePanel); ok {
				t.Fatal("variants made in the request")
			}
			done, err := svc.RunJob(ctx, jobs.jobs[0])
			if err != nil {
				t.Fatal(err)
			}
			panel, ok := done.Variants.Get(media.VariantNamePanel)
			if done.ProcessingState == nil || *done.ProcessingState != media.ProcessingDone || !ok || panel.Width != media.PanelSide {
				t.Fatalf("after the job %+v", done)
			}
			if _, ok := done.Variants.Get(media.VariantNameMessage); !ok {
				t.Fatalf("no message variant %+v", done.Variants)
			}
		})
	}
}

func TestStickersOutsideAnImportStayInTheRequest(t *testing.T) {
	svc, _, _, jobs := optimizingService(t)
	a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeSticker, Filename: "s.webp", Body: bytes.NewReader(ffmpegSticker(t, false))})
	if err != nil {
		t.Fatal(err)
	}
	if a.ProcessingState != nil || len(jobs.jobs) != 0 {
		t.Fatalf("normal sticker %+v, jobs %d", a, len(jobs.jobs))
	}
	if _, ok := a.Variants.Get(media.VariantNamePanel); !ok {
		t.Fatal("normal sticker needs its panel variant at once")
	}
}

func pngSticker(t *testing.T, w, h int) []byte {
	t.Helper()
	return photoPNG(t, w, h)
}

func TestImportStickersOfAnyShapeAreFinishedInTheWorker(t *testing.T) {
	svc, store, _, jobs := optimizingService(t)
	ctx := t.Context()
	a, err := svc.Upload(ctx, media.Upload{UploaderID: 1, Purpose: media.PurposeSticker, Filename: "s.png", Body: bytes.NewReader(pngSticker(t, 700, 300)), ForImport: true})
	if err != nil {
		t.Fatal(err)
	}
	if a.ProcessingState == nil || *a.ProcessingState != media.ProcessingQueued || a.MimeType != "image/png" || *a.Width != 700 || len(jobs.jobs) != 1 {
		t.Fatalf("upload %+v, jobs %d", a, len(jobs.jobs))
	}
	before, err := svc.OpenUnchecked(ctx, a.ID, media.VariantOriginal)
	if err != nil {
		t.Fatal(err)
	}
	_ = before.Object.Body.Close()
	if !before.Pending {
		t.Fatal("a sticker in processing must not be cached as final")
	}
	done, err := svc.RunJob(ctx, jobs.jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	if *done.ProcessingState != media.ProcessingDone || done.MimeType != "image/webp" || *done.Width != media.StickerSide || *done.Height != media.StickerSide {
		t.Fatalf("after the job %+v", done)
	}
	if _, ok := done.Variants.Get(media.VariantNamePanel); !ok {
		t.Fatalf("no panel variant %+v", done.Variants)
	}
	stored := store.get(t, done.StorageKey)
	if int64(len(stored)) != done.Size || string(stored[8:12]) != "WEBP" {
		t.Fatalf("main object not replaced: %d bytes, size %d", len(stored), done.Size)
	}
	after, err := svc.OpenUnchecked(ctx, a.ID, media.VariantOriginal)
	if err != nil {
		t.Fatal(err)
	}
	_ = after.Object.Body.Close()
	if after.Pending || after.ETagSuffix == before.ETagSuffix {
		t.Fatalf("final bytes need a final cache entry: before %q after %q", before.ETagSuffix, after.ETagSuffix)
	}
}
