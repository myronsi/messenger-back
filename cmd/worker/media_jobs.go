package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	mediaJobPoll       = 5 * time.Second
	mediaJobTimeout    = 15 * time.Minute
	mediaJobStuckAfter = 30 * time.Minute
	mediaJobKeepDone   = 7 * 24 * time.Hour
)

type mediaJobRunner struct {
	svc     *media.Service
	jobs    postgres.MediaJobRepository
	notify  func(ctx context.Context, a postgres.Attachment)
	metrics func(kind, outcome string, d time.Duration, saved int64)
	log     *slog.Logger
}

func (r *mediaJobRunner) run(ctx context.Context, workers int) {
	var wg sync.WaitGroup
	for range max(1, workers) {
		wg.Go(func() { r.loop(ctx) })
	}
	wg.Go(func() { r.maintain(ctx) })
	wg.Wait()
}

func (r *mediaJobRunner) loop(ctx context.Context) {
	t := time.NewTicker(mediaJobPoll)
	defer t.Stop()
	for {
		for r.next(ctx) {
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *mediaJobRunner) next(ctx context.Context) bool {
	job, ok, err := r.jobs.Claim(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.ErrorContext(ctx, "claim media job", "error", err)
		}
		return false
	}
	if !ok {
		return false
	}
	start := time.Now()
	jctx, cancel := context.WithTimeout(ctx, mediaJobTimeout)
	a, err := r.svc.RunJob(jctx, job)
	cancel()
	elapsed := time.Since(start)
	switch {
	case err == nil:
		if ferr := r.jobs.Finish(ctx, job.ID); ferr != nil {
			r.log.ErrorContext(ctx, "finish media job", "error", ferr)
		}
		outcome, saved := "skipped", int64(0)
		if sv, ok := a.Variants.Get(media.VariantNameOptimized); ok {
			outcome, saved = "done", a.Size-sv.Size
		} else if sv, ok := a.Variants.Get(media.VariantNameAnimation); ok {
			outcome, saved = "done", a.Size-sv.Size
		}
		r.metrics(job.Kind, outcome, elapsed, saved)
		r.notify(ctx, a)
	case errors.Is(err, postgres.ErrNotFound), errors.Is(err, media.ErrNotFound):
		_ = r.jobs.Finish(ctx, job.ID)
		r.metrics(job.Kind, "skipped", elapsed, 0)
	case job.Attempts >= media.MaxJobAttempts:
		r.log.WarnContext(ctx, "media job failed", "kind", job.Kind, "attachment_id", job.AttachmentID.String(), "error", err)
		if ferr := r.jobs.Fail(context.WithoutCancel(ctx), job.ID, err.Error()); ferr != nil {
			r.log.ErrorContext(ctx, "fail media job", "error", ferr)
		}
		r.svc.MarkJobFailed(context.WithoutCancel(ctx), job)
		r.metrics(job.Kind, "failed", elapsed, 0)
		if failed, gerr := r.svc.Attachment(context.WithoutCancel(ctx), job.AttachmentID); gerr == nil {
			r.notify(ctx, failed)
		}
	default:
		after := time.Now().Add(time.Duration(job.Attempts*job.Attempts) * time.Minute)
		if rerr := r.jobs.Retry(context.WithoutCancel(ctx), job.ID, err.Error(), after); rerr != nil {
			r.log.ErrorContext(ctx, "retry media job", "error", rerr)
		}
		r.metrics(job.Kind, "retry", elapsed, 0)
	}
	return true
}

func (r *mediaJobRunner) maintain(ctx context.Context) {
	t := time.NewTicker(maintenanceEvery)
	defer t.Stop()
	for {
		if n, err := r.jobs.RequeueStuck(ctx, time.Now().Add(-mediaJobStuckAfter)); err != nil {
			r.log.ErrorContext(ctx, "requeue stuck media jobs", "error", err)
		} else if n > 0 {
			r.log.WarnContext(ctx, "requeued stuck media jobs", "count", n)
		}
		if _, err := r.jobs.DeleteFinished(ctx, time.Now().Add(-mediaJobKeepDone)); err != nil {
			r.log.ErrorContext(ctx, "delete finished media jobs", "error", err)
		}
		if n, err := r.svc.CollectSessions(ctx, 200); err != nil {
			r.log.ErrorContext(ctx, "collect upload sessions", "error", err)
		} else if n > 0 {
			r.log.InfoContext(ctx, "deleted stale upload sessions", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
