package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	ReviewClosedDecided    = "decided"
	ReviewClosedStatusGood = "status_good"
)

type NewReviewRequest struct {
	UserID int64
	Status string
	Text   string
	Now    time.Time
}

type ReviewRequestState struct {
	OpenSince *time.Time
	Recent    int
}

type ReviewRequestRepository interface {
	Create(ctx context.Context, r NewReviewRequest, since time.Time, max int) error
	State(ctx context.Context, userID int64, since time.Time) (ReviewRequestState, error)
	Close(ctx context.Context, userID int64, reason string, now time.Time) (bool, error)
}

type reviewRequestRepo struct{ s *Store }

var _ ReviewRequestRepository = reviewRequestRepo{}

func (s *Store) ReviewRequests() ReviewRequestRepository { return reviewRequestRepo{s} }

func (r reviewRequestRepo) Create(ctx context.Context, req NewReviewRequest, since time.Time, max int) error {
	return mapError(r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('account_review_requests', $1))`, req.UserID); err != nil {
			return err
		}
		var open bool
		var recent int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE closed_at IS NULL) > 0, count(*) FILTER (WHERE created_at > $2) FROM account_review_requests WHERE user_id=$1`, req.UserID, since).Scan(&open, &recent); err != nil {
			return err
		}
		if open {
			return ErrConflict
		}
		if recent >= max {
			return ErrLimit
		}
		_, err := tx.Exec(ctx, `INSERT INTO account_review_requests(user_id,status,text,created_at) VALUES($1,$2,$3,$4)`, req.UserID, req.Status, req.Text, req.Now)
		return err
	}))
}

func (r reviewRequestRepo) State(ctx context.Context, userID int64, since time.Time) (ReviewRequestState, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var out ReviewRequestState
	err := r.s.pool.QueryRow(ctx, `SELECT max(created_at) FILTER (WHERE closed_at IS NULL), count(*) FILTER (WHERE created_at > $2) FROM account_review_requests WHERE user_id=$1`, userID, since).Scan(&out.OpenSince, &out.Recent)
	return out, mapError(err)
}

func (r reviewRequestRepo) Close(ctx context.Context, userID int64, reason string, now time.Time) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	tag, err := r.s.pool.Exec(ctx, `UPDATE account_review_requests SET closed_at=$3, close_reason=$2 WHERE user_id=$1 AND closed_at IS NULL`, userID, reason, now)
	return tag.RowsAffected() > 0, mapError(err)
}
