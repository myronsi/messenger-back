package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

type UploadSession struct {
	ID         uuid.UUID
	UploaderID int64
	Purpose    string
	Kind       string
	Filename   string
	Size       int64
	ChunkSize  int
	Received   int64
	Chunks     int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type NewUploadSession struct {
	UploaderID int64
	Purpose    string
	Kind       string
	Filename   string
	Size       int64
	ChunkSize  int
}

var ErrUploadOffset = errors.New("postgres: upload offset does not match")

type UploadSessionRepository interface {
	Create(ctx context.Context, n NewUploadSession) (UploadSession, error)
	Get(ctx context.Context, id uuid.UUID, uploaderID int64) (UploadSession, error)
	Advance(ctx context.Context, id uuid.UUID, uploaderID, offset, length int64) (UploadSession, error)
	Delete(ctx context.Context, id uuid.UUID) (UploadSession, error)
	Count(ctx context.Context, uploaderID int64) (int64, error)
	Stale(ctx context.Context, updatedBefore time.Time, limit int) ([]UploadSession, error)
}

type uploadSessionRepo struct{ s *Store }

var _ UploadSessionRepository = uploadSessionRepo{}

func (s *Store) UploadSessions() UploadSessionRepository { return uploadSessionRepo{s} }

func uploadSessionFrom(u sqlcdb.UploadSession) UploadSession {
	return UploadSession{
		ID: u.ID, UploaderID: u.UploaderID, Purpose: u.Purpose, Kind: u.Kind, Filename: u.Filename, Size: u.Size,
		ChunkSize: int(u.ChunkSize), Received: u.Received, Chunks: int(u.Chunks), CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}

func (r uploadSessionRepo) Create(ctx context.Context, n NewUploadSession) (UploadSession, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.CreateUploadSession(ctx, sqlcdb.CreateUploadSessionParams{
		UploaderID: n.UploaderID, Purpose: n.Purpose, Kind: n.Kind, Filename: n.Filename, Size: n.Size, ChunkSize: int32(n.ChunkSize), //nolint:gosec
	})
	if err != nil {
		return UploadSession{}, mapError(err)
	}
	return uploadSessionFrom(row), nil
}

func (r uploadSessionRepo) Get(ctx context.Context, id uuid.UUID, uploaderID int64) (UploadSession, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetUploadSession(ctx, sqlcdb.GetUploadSessionParams{ID: id, UploaderID: uploaderID})
	if err != nil {
		return UploadSession{}, mapError(err)
	}
	return uploadSessionFrom(row), nil
}

func (r uploadSessionRepo) Advance(ctx context.Context, id uuid.UUID, uploaderID, offset, length int64) (UploadSession, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.AdvanceUploadSession(ctx, sqlcdb.AdvanceUploadSessionParams{ID: id, UploaderID: uploaderID, OffsetBytes: offset, Length: length})
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadSession{}, ErrUploadOffset
	}
	if err != nil {
		return UploadSession{}, mapError(err)
	}
	return uploadSessionFrom(row), nil
}

func (r uploadSessionRepo) Delete(ctx context.Context, id uuid.UUID) (UploadSession, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.DeleteUploadSession(ctx, id)
	if err != nil {
		return UploadSession{}, mapError(err)
	}
	return uploadSessionFrom(row), nil
}

func (r uploadSessionRepo) Count(ctx context.Context, uploaderID int64) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.CountUploadSessions(ctx, uploaderID)
	return n, mapError(err)
}

func (r uploadSessionRepo) Stale(ctx context.Context, updatedBefore time.Time, limit int) ([]UploadSession, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListStaleUploadSessions(ctx, sqlcdb.ListStaleUploadSessionsParams{UpdatedBefore: updatedBefore, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]UploadSession, len(rows))
	for i, row := range rows {
		out[i] = uploadSessionFrom(row)
	}
	return out, nil
}
