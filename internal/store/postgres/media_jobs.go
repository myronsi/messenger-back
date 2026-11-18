package postgres

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

type MediaJob struct {
	ID           int64
	AttachmentID uuid.UUID
	Kind         string
	Attempts     int
}

type MediaJobRepository interface {
	Enqueue(ctx context.Context, attachmentID uuid.UUID, kind string) (bool, error)
	Claim(ctx context.Context) (MediaJob, bool, error)
	Finish(ctx context.Context, id int64) error
	Retry(ctx context.Context, id int64, reason string, after time.Time) error
	Fail(ctx context.Context, id int64, reason string) error
	RequeueStuck(ctx context.Context, startedBefore time.Time) (int64, error)
	Counts(ctx context.Context) (map[string]int64, error)
	DeleteFinished(ctx context.Context, finishedBefore time.Time) (int64, error)
}

type mediaJobRepo struct{ s *Store }

var _ MediaJobRepository = mediaJobRepo{}

func (s *Store) MediaJobs() MediaJobRepository { return mediaJobRepo{s} }

func shortReason(reason string) *string {
	for len(reason) > 500 {
		_, size := utf8.DecodeLastRuneInString(reason)
		reason = reason[:len(reason)-size]
	}
	return &reason
}

func (r mediaJobRepo) Enqueue(ctx context.Context, attachmentID uuid.UUID, kind string) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.EnqueueMediaJob(ctx, sqlcdb.EnqueueMediaJobParams{AttachmentID: attachmentID, Kind: kind})
	return n > 0, mapError(err)
}

func (r mediaJobRepo) Claim(ctx context.Context) (MediaJob, bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.ClaimMediaJob(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return MediaJob{}, false, nil
	}
	if err != nil {
		return MediaJob{}, false, mapError(err)
	}
	return MediaJob{ID: row.ID, AttachmentID: row.AttachmentID, Kind: row.Kind, Attempts: int(row.Attempts)}, true, nil
}

func (r mediaJobRepo) Finish(ctx context.Context, id int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.FinishMediaJob(ctx, id))
}

func (r mediaJobRepo) Retry(ctx context.Context, id int64, reason string, after time.Time) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.RetryMediaJob(ctx, sqlcdb.RetryMediaJobParams{ID: id, Error: shortReason(reason), RunAfter: after}))
}

func (r mediaJobRepo) Fail(ctx context.Context, id int64, reason string) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.FailMediaJob(ctx, sqlcdb.FailMediaJobParams{ID: id, Error: shortReason(reason)}))
}

func (r mediaJobRepo) RequeueStuck(ctx context.Context, startedBefore time.Time) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.RequeueStuckMediaJobs(ctx, &startedBefore)
	return n, mapError(err)
}

func (r mediaJobRepo) Counts(ctx context.Context) (map[string]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.CountMediaJobs(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := map[string]int64{"queued": 0, "running": 0, "done": 0, "failed": 0}
	for _, row := range rows {
		out[row.State] = row.N
	}
	return out, nil
}

func (r mediaJobRepo) DeleteFinished(ctx context.Context, finishedBefore time.Time) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.DeleteFinishedMediaJobs(ctx, &finishedBefore)
	return n, mapError(err)
}
