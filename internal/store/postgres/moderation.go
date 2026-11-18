package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

type UserReport struct {
	ReporterID int64
	TargetID   int64
	Reason     string
	Note       *string
	Context    string
	Weight     float64
	Snapshot   []byte
	Now        time.Time
}

type ReportRules struct {
	RateSince  time.Time
	MaxInRate  int
	Burst      bool
	BurstSince time.Time
}

type ReportFact struct {
	ReporterID int64
	Weight     float64
	CreatedAt  time.Time
}

type ModerationState struct {
	UserID             int64
	Status             string
	Score              float64
	Since              time.Time
	ProbationUntil     *time.Time
	AppealBlockedUntil *time.Time
	LastAppealAt       *time.Time
	Cause              string
}

type ModerationEvent struct {
	ID         int64
	UserID     int64
	FromStatus string
	ToStatus   string
	Score      float64
	Cause      string
	CreatedAt  time.Time
}

type ModerationRepository interface {
	SaveReport(ctx context.Context, r UserReport, rules ReportRules) (inserted bool, err error)
	WithdrawReport(ctx context.Context, reporterID, targetID int64, now time.Time) (bool, error)
	ReportedUsersSince(ctx context.Context, reporterID, exceptTarget int64, since time.Time) (int, error)
	UnconnectedReportsSince(ctx context.Context, targetID, exceptReporter int64, since time.Time) (int, error)
	PairContext(ctx context.Context, reporterID, targetID int64) (string, error)
	DirectChatsStartedSince(ctx context.Context, userID int64, since time.Time) (int, error)
	State(ctx context.Context, userID int64) (ModerationState, error)
	Statuses(ctx context.Context, userIDs []int64) (map[int64]string, error)
	Transition(ctx context.Context, userID int64, factsSince time.Time, now time.Time, next func(ModerationState, []ReportFact) ModerationState) (before, after ModerationState, err error)
	Due(ctx context.Context, after int64, limit int) ([]int64, error)
	PurgeReports(ctx context.Context, before time.Time) (int64, error)
	Events(ctx context.Context, userID int64) ([]ModerationEvent, error)
}

const (
	ContextTargetInitiated = "target_initiated"
	ContextSharedChat      = "shared_chat"
	ContextNone            = "none"
	StatusGood             = "good"
)

type moderationRepo struct{ s *Store }

var _ ModerationRepository = moderationRepo{}

func (s *Store) Moderation() ModerationRepository { return moderationRepo{s} }

func (r moderationRepo) SaveReport(ctx context.Context, rep UserReport, rules ReportRules) (bool, error) {
	var inserted bool
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		inserted = false
		row, err := q.UpsertUserReport(ctx, sqlcdb.UpsertUserReportParams{
			ReporterID: rep.ReporterID, TargetID: rep.TargetID, Reason: rep.Reason, Note: rep.Note,
			Context: rep.Context, Weight: rep.Weight, Snapshot: rep.Snapshot, Now: rep.Now,
		})
		if err != nil {
			return err
		}
		n, err := q.CountUserReportsByReporterSince(ctx, sqlcdb.CountUserReportsByReporterSinceParams{
			ReporterID: rep.ReporterID, ExceptTarget: rep.TargetID, Since: rules.RateSince,
		})
		if err != nil {
			return err
		}
		if int(n) >= rules.MaxInRate {
			return ErrLimit
		}
		if rules.Burst {
			if _, err := q.ZeroUnconnectedReportsSince(ctx, sqlcdb.ZeroUnconnectedReportsSinceParams{
				TargetID: rep.TargetID, Since: rules.BurstSince,
			}); err != nil {
				return err
			}
		}
		inserted = row.Inserted
		return nil
	})
	return inserted, mapError(err)
}

func (r moderationRepo) WithdrawReport(ctx context.Context, reporterID, targetID int64, now time.Time) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.WithdrawUserReport(ctx, sqlcdb.WithdrawUserReportParams{ReporterID: reporterID, TargetID: targetID, Now: now})
	return n > 0, mapError(err)
}

func (r moderationRepo) ReportedUsersSince(ctx context.Context, reporterID, exceptTarget int64, since time.Time) (int, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.CountReportedUsersSince(ctx, sqlcdb.CountReportedUsersSinceParams{ReporterID: reporterID, ExceptTarget: exceptTarget, Since: since})
	return int(n), mapError(err)
}

func (r moderationRepo) UnconnectedReportsSince(ctx context.Context, targetID, exceptReporter int64, since time.Time) (int, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.CountUnconnectedReportsSince(ctx, sqlcdb.CountUnconnectedReportsSinceParams{TargetID: targetID, ExceptReporter: exceptReporter, Since: since})
	return int(n), mapError(err)
}

func (r moderationRepo) PairContext(ctx context.Context, reporterID, targetID int64) (string, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	initiated, err := r.s.q.TargetInitiatedContact(ctx, sqlcdb.TargetInitiatedContactParams{ReporterID: reporterID, TargetID: targetID})
	if err != nil {
		return "", mapError(err)
	}
	if initiated {
		return ContextTargetInitiated, nil
	}
	shared, err := r.s.q.ShareAnyChat(ctx, sqlcdb.ShareAnyChatParams{ReporterID: reporterID, TargetID: targetID})
	if err != nil {
		return "", mapError(err)
	}
	if shared {
		return ContextSharedChat, nil
	}
	return ContextNone, nil
}

func (r moderationRepo) DirectChatsStartedSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.CountDirectChatsCreatedSince(ctx, sqlcdb.CountDirectChatsCreatedSinceParams{
		UserID: &userID, Since: since, SelfKey: directKey(userID, userID),
	})
	return int(n), mapError(err)
}

func stateFrom(userID int64, status string, score float64, since time.Time, probation, blocked, appeal *time.Time) ModerationState {
	if since.Unix() <= 0 {
		since = time.Time{}
	}
	return ModerationState{
		UserID: userID, Status: status, Score: score, Since: since,
		ProbationUntil: probation, AppealBlockedUntil: blocked, LastAppealAt: appeal,
	}
}

func (r moderationRepo) State(ctx context.Context, userID int64) (ModerationState, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetUserModeration(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModerationState{UserID: userID, Status: StatusGood}, nil
	}
	if err != nil {
		return ModerationState{}, mapError(err)
	}
	return stateFrom(row.UserID, row.Status, row.Score, row.StatusSince, row.ProbationUntil, row.AppealBlockedUntil, row.LastAppealAt), nil
}

func (r moderationRepo) Statuses(ctx context.Context, userIDs []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListModerationStatuses(ctx, userIDs)
	if err != nil {
		return nil, mapError(err)
	}
	for _, row := range rows {
		out[row.UserID] = row.Status
	}
	return out, nil
}

func (r moderationRepo) Transition(ctx context.Context, userID int64, factsSince, now time.Time, next func(ModerationState, []ReportFact) ModerationState) (ModerationState, ModerationState, error) {
	var before, after ModerationState
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := q.EnsureUserModeration(ctx, userID); err != nil {
			return err
		}
		row, err := q.LockUserModeration(ctx, userID)
		if err != nil {
			return err
		}
		before = stateFrom(row.UserID, row.Status, row.Score, row.StatusSince, row.ProbationUntil, row.AppealBlockedUntil, row.LastAppealAt)
		rows, err := q.ListReportFacts(ctx, sqlcdb.ListReportFactsParams{TargetID: userID, Since: factsSince})
		if err != nil {
			return err
		}
		facts := make([]ReportFact, len(rows))
		for i, f := range rows {
			facts[i] = ReportFact{ReporterID: f.ReporterID, Weight: f.Weight, CreatedAt: f.CreatedAt}
		}
		after = next(before, facts)
		after.UserID = userID
		since := after.Since
		if since.IsZero() {
			since = time.Unix(0, 0)
		}
		if err := q.UpdateUserModeration(ctx, sqlcdb.UpdateUserModerationParams{
			UserID: userID, Status: after.Status, Score: after.Score, StatusSince: since,
			ProbationUntil: after.ProbationUntil, AppealBlockedUntil: after.AppealBlockedUntil, LastAppealAt: after.LastAppealAt,
		}); err != nil {
			return err
		}
		if after.Status == before.Status {
			return nil
		}
		return q.InsertUserModerationEvent(ctx, sqlcdb.InsertUserModerationEventParams{
			UserID: userID, FromStatus: before.Status, ToStatus: after.Status, Score: after.Score, Cause: after.Cause, Now: now,
		})
	})
	return before, after, mapError(err)
}

func (r moderationRepo) Due(ctx context.Context, after int64, limit int) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.ListModerationDue(ctx, sqlcdb.ListModerationDueParams{After: after, MaxRows: clampLimit(limit)})
	return ids, mapError(err)
}

func (r moderationRepo) PurgeReports(ctx context.Context, before time.Time) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.DeleteUserReportsBefore(ctx, before)
	return n, mapError(err)
}

func (r moderationRepo) Events(ctx context.Context, userID int64) ([]ModerationEvent, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListUserModerationEvents(ctx, userID)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]ModerationEvent, len(rows))
	for i, row := range rows {
		out[i] = ModerationEvent{ID: row.ID, UserID: row.UserID, FromStatus: row.FromStatus, ToStatus: row.ToStatus, Score: row.Score, Cause: row.Cause, CreatedAt: row.CreatedAt}
	}
	return out, nil
}
