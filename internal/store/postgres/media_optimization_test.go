package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAttachmentVariantsAreStoredMergedAndDeleted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "variants")
	title := "Song"
	a, err := s.Attachments().Create(ctx, NewAttachment{
		UploaderID: &u.ID, Purpose: PurposeMessage, Kind: "audio", Filename: "a.flac", StorageKey: "attachments/a",
		MimeType: "audio/flac", Size: 10, AudioTitle: &title,
		Variants: Variants{"display": {Key: "attachments/a.display.webp", ContentType: "image/webp", Size: 3, Width: 1600, Height: 900}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := a.Variants.Get("display"); !ok || v.Width != 1600 || a.AudioTitle == nil || *a.AudioTitle != "Song" {
		t.Fatalf("created %+v", a)
	}
	done := "done"
	merged, err := s.Attachments().MergeVariants(ctx, a.ID, AttachmentUpdate{
		Variants:        Variants{"optimized": {Key: "attachments/a.optimized.m4a", ContentType: "audio/mp4", Size: 4}},
		ProcessingState: &done,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := merged.Variants.Get("display"); !ok {
		t.Fatal("merge dropped an existing variant")
	}
	if _, ok := merged.Variants.Get("optimized"); !ok || merged.ProcessingState == nil || *merged.ProcessingState != "done" || *merged.AudioTitle != "Song" {
		t.Fatalf("merged %+v", merged)
	}
	if err := s.Attachments().SetProcessing(ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	keys, deleted, err := s.Attachments().DeleteIfUnreferenced(ctx, a.ID)
	if err != nil || !deleted {
		t.Fatalf("delete %v %v", deleted, err)
	}
	for _, want := range []string{"attachments/a", "attachments/a.display.webp", "attachments/a.optimized.m4a"} {
		if !slices.Contains(keys, want) {
			t.Fatalf("keys %v lack %s", keys, want)
		}
	}
}

func TestBackfillListsAttachmentsWithoutTheVariant(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "backfill")
	plain, err := s.Attachments().Create(ctx, NewAttachment{UploaderID: &u.ID, Purpose: PurposeMessage, Kind: "video", StorageKey: "attachments/v1", MimeType: "video/mp4", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attachments().Create(ctx, NewAttachment{UploaderID: &u.ID, Purpose: PurposeMessage, Kind: "video", StorageKey: "attachments/v2", MimeType: "video/mp4", Size: 1,
		Variants: Variants{"optimized": {Key: "attachments/v2.optimized.mp4", ContentType: "video/mp4", Size: 1}}}); err != nil {
		t.Fatal(err)
	}
	list, err := s.Attachments().ListForBackfill(ctx, BackfillFilter{Kinds: []string{"video"}, Purposes: []string{PurposeMessage}, MissingVariant: "optimized", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != plain.ID {
		t.Fatalf("listed %d", len(list))
	}
	audience, err := s.Attachments().Audience(ctx, plain.ID)
	if err != nil || len(audience) != 1 || audience[0] != u.ID {
		t.Fatalf("audience %v %v", audience, err)
	}
}

func TestMediaJobQueue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "jobs")
	a, err := s.Attachments().Create(ctx, NewAttachment{UploaderID: &u.ID, Purpose: PurposeMessage, Kind: "video", StorageKey: "attachments/j", MimeType: "video/mp4", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	jobs := s.MediaJobs()
	if ok, err := jobs.Enqueue(ctx, a.ID, "video"); err != nil || !ok {
		t.Fatalf("enqueue %v %v", ok, err)
	}
	if ok, err := jobs.Enqueue(ctx, a.ID, "video"); err != nil || ok {
		t.Fatalf("second enqueue %v %v", ok, err)
	}
	job, ok, err := jobs.Claim(ctx)
	if err != nil || !ok || job.AttachmentID != a.ID || job.Attempts != 1 {
		t.Fatalf("claim %+v %v %v", job, ok, err)
	}
	if _, ok, _ := jobs.Claim(ctx); ok {
		t.Fatal("a running job was claimed twice")
	}
	if err := jobs.Retry(ctx, job.ID, "boom", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := jobs.Claim(ctx); ok {
		t.Fatal("a job was claimed before run_after")
	}
	if err := jobs.Retry(ctx, job.ID, "boom", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	again, ok, err := jobs.Claim(ctx)
	if err != nil || !ok || again.Attempts != 2 {
		t.Fatalf("reclaim %+v %v %v", again, ok, err)
	}
	if n, err := jobs.RequeueStuck(ctx, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("requeue %d %v", n, err)
	}
	third, _, _ := jobs.Claim(ctx)
	if err := jobs.Finish(ctx, third.ID); err != nil {
		t.Fatal(err)
	}
	counts, err := jobs.Counts(ctx)
	if err != nil || counts["done"] != 1 || counts["queued"] != 0 {
		t.Fatalf("counts %v %v", counts, err)
	}
	if n, err := jobs.DeleteFinished(ctx, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("delete finished %d %v", n, err)
	}
}

func TestRunningJobKeepsUploadFromCollection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "collect")
	a, err := s.Attachments().Create(ctx, NewAttachment{UploaderID: &u.ID, Purpose: PurposeMessage, Kind: "video", StorageKey: "attachments/c", MimeType: "video/mp4", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MediaJobs().Enqueue(ctx, a.ID, "video"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MediaJobs().Claim(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := s.Attachments().Unreferenced(ctx, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.ID == a.ID {
			t.Fatal("an upload with a running job was listed for collection")
		}
	}
}

func TestUploadSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "sessions")
	other := mustUser(t, s, "sessionsother")
	repo := s.UploadSessions()
	sess, err := repo.Create(ctx, NewUploadSession{UploaderID: u.ID, Purpose: PurposeMessage, Filename: "big.mp4", Size: 20, ChunkSize: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, sess.ID, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user got the session: %v", err)
	}
	next, err := repo.Advance(ctx, sess.ID, u.ID, 0, 8)
	if err != nil || next.Received != 8 || next.Chunks != 1 {
		t.Fatalf("advance %+v %v", next, err)
	}
	if _, err := repo.Advance(ctx, sess.ID, u.ID, 0, 8); !errors.Is(err, ErrUploadOffset) {
		t.Fatalf("repeated chunk: %v", err)
	}
	if _, err := repo.Advance(ctx, sess.ID, u.ID, 8, 13); !errors.Is(err, ErrUploadOffset) {
		t.Fatalf("chunk past the end: %v", err)
	}
	if n, err := repo.Count(ctx, u.ID); err != nil || n != 1 {
		t.Fatalf("count %d %v", n, err)
	}
	stale, err := repo.Stale(ctx, time.Now().Add(time.Minute), 10)
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale %d %v", len(stale), err)
	}
	if _, err := repo.Delete(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, sess.ID, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session found: %v", err)
	}
	_ = uuid.Nil
}

func TestImportImageJobsYieldToLiveJobsAndRunOneAtATime(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "importjobs")
	jobs := s.MediaJobs()
	attach := func(key string) uuid.UUID {
		a, err := s.Attachments().Create(ctx, NewAttachment{UploaderID: &u.ID, Purpose: PurposeMessage, Kind: "image", StorageKey: "attachments/" + key, MimeType: "image/jpeg", Size: 1})
		if err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	first, second, live := attach("i1"), attach("i2"), attach("v1")
	for _, q := range []struct {
		id   uuid.UUID
		kind string
	}{{first, "import_image"}, {second, "import_sticker"}, {live, "video"}} {
		if ok, err := jobs.Enqueue(ctx, q.id, q.kind); err != nil || !ok {
			t.Fatalf("enqueue %s: %v %v", q.kind, ok, err)
		}
	}
	got, ok, err := jobs.Claim(ctx)
	if err != nil || !ok || got.Kind != "video" {
		t.Fatalf("the live job goes first: %+v %v %v", got, ok, err)
	}
	importing, ok, err := jobs.Claim(ctx)
	if err != nil || !ok || importing.AttachmentID != first {
		t.Fatalf("then the oldest import job: %+v %v %v", importing, ok, err)
	}
	if extra, ok, _ := jobs.Claim(ctx); ok {
		t.Fatalf("a second import job ran at the same time: %+v", extra)
	}
	if err := jobs.Finish(ctx, importing.ID); err != nil {
		t.Fatal(err)
	}
	next, ok, err := jobs.Claim(ctx)
	if err != nil || !ok || next.AttachmentID != second {
		t.Fatalf("the next import job after the first finished: %+v %v %v", next, ok, err)
	}
}
