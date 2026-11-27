package media_test

import (
	"bytes"
	"testing"

	"github.com/myronsi/messenger-back/internal/media"
)

func TestImportImagesDeferTheHeavyWork(t *testing.T) {
	svc, store, _, jobs := optimizingService(t)
	ctx := t.Context()
	a, err := svc.Upload(ctx, media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "IMG-1.jpg", Body: bytes.NewReader(photoJPEG(t, 2400, 1600)), ForImport: true})
	if err != nil {
		t.Fatal(err)
	}
	if a.ProcessingState == nil || *a.ProcessingState != media.ProcessingQueued || a.ThumbnailKey != nil || a.Placeholder != nil {
		t.Fatalf("upload did the image work in the request: %+v", a)
	}
	if a.Width == nil || *a.Width != 2400 || *a.Height != 1600 || a.MimeType != "image/jpeg" {
		t.Fatalf("size and type from the header: %+v", a)
	}
	if len(jobs.jobs) != 1 || jobs.jobs[0].Kind != media.JobImportImage {
		t.Fatalf("jobs %+v", jobs.jobs)
	}
	done, err := svc.RunJob(ctx, jobs.jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	if done.ProcessingState == nil || *done.ProcessingState != media.ProcessingDone || done.ThumbnailKey == nil || done.Placeholder == nil {
		t.Fatalf("after the job %+v", done)
	}
	if _, ok := done.Variants.Get(media.VariantNameDisplay); !ok {
		t.Fatal("a large import image needs a display copy")
	}
	if len(store.get(t, *done.ThumbnailKey)) == 0 {
		t.Fatal("thumbnail not stored")
	}
}

func TestSmallImportImagesGetNoThumbnail(t *testing.T) {
	svc, _, _, jobs := optimizingService(t)
	a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "s.jpg", Body: bytes.NewReader(photoJPEG(t, 300, 200)), ForImport: true})
	if err != nil {
		t.Fatal(err)
	}
	done, err := svc.RunJob(t.Context(), jobs.jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	if done.ThumbnailKey != nil || done.Placeholder == nil || a.ID != done.ID {
		t.Fatalf("small image %+v", done)
	}
	if _, ok := done.Variants.Get(media.VariantNameDisplay); ok {
		t.Fatal("a small image needs no display copy")
	}
}

func TestImagesOutsideAnImportStayInTheRequest(t *testing.T) {
	svc, _, _, jobs := optimizingService(t)
	a, err := svc.Upload(t.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "p.jpg", Body: bytes.NewReader(photoJPEG(t, 2400, 1600))})
	if err != nil {
		t.Fatal(err)
	}
	if a.ProcessingState != nil || a.ThumbnailKey == nil || a.Placeholder == nil || len(jobs.jobs) != 0 {
		t.Fatalf("normal upload %+v, jobs %d", a, len(jobs.jobs))
	}
}
