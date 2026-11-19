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
	DecidedBy          string
	DecidedAt          *time.Time
	ReasonCode         *string
	ReasonText         *string
	Cause              string
	StaffRef           *string
}

type ModerationEvent struct {
	ID         int64
	UserID     int64
	FromStatus string
	ToStatus   string
	Score      float64
	Cause      string
	ReasonCode *string
	ReasonText *string
	StaffRef   *string
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
	DismissReports(ctx context.Context, targetID int64, reporterIDs []int64, now time.Time) (dismissed int, outcomes []ReportOutcome, err error)
	NotifyUserReporters(ctx context.Context, targetID int64, outcome string, now time.Time) ([]ReportOutcome, error)
	HidePack(ctx context.Context, kind PackKind, packID int64, now time.Time) (bool, []ReportOutcome, error)
	UnhidePack(ctx context.Context, kind PackKind, packID int64, now time.Time) (bool, []ReportOutcome, error)
	ReportOutcomes(ctx context.Context, reporterID int64, since time.Time, limit int) ([]ReportOutcome, error)
	MarkReportOutcomeRead(ctx context.Context, reporterID, id int64, now time.Time) error
	PurgeReportOutcomes(ctx context.Context, before time.Time) (int64, error)
}

const (
	ContextTargetInitiated = "target_initiated"
	ContextSharedChat      = "shared_chat"
	ContextNone            = "none"
	StatusGood             = "good"
	DecidedAutomatic       = "automatic"
	DecidedStaff           = "staff"
	CauseManual            = "manual"
	OutcomeActionTaken     = "action_taken"
	OutcomeNoViolation     = "no_violation"
)

type PackKind string

const (
	StickerPackKind PackKind = "sticker_pack"
	EmojiPackKind   PackKind = "emoji_pack"
)

type ReportOutcome struct {
	ID          int64
	ReporterID  int64
	SubjectType string
	SubjectID   int64
	Outcome     string
	CreatedAt   time.Time
	ReadAt      *time.Time
}

func outcomesFrom(rows []sqlcdb.ReportOutcome) []ReportOutcome {
	out := make([]ReportOutcome, len(rows))
	for i, r := range rows {
		out[i] = ReportOutcome{ID: r.ID, ReporterID: r.ReporterID, SubjectType: r.SubjectType, SubjectID: r.SubjectID, Outcome: r.Outcome, CreatedAt: r.CreatedAt, ReadAt: r.ReadAt}
	}
	return out
}

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

type moderationRow struct {
	UserID             int64
	Status             string
	Score              float64
	StatusSince        time.Time
	ProbationUntil     *time.Time
	AppealBlockedUntil *time.Time
	LastAppealAt       *time.Time
	DecidedBy          string
	DecidedAt          *time.Time
	ReasonCode         *string
	ReasonText         *string
}

func stateFrom(row moderationRow) ModerationState {
	since := row.StatusSince
	if since.Unix() <= 0 {
		since = time.Time{}
	}
	return ModerationState{
		UserID: row.UserID, Status: row.Status, Score: row.Score, Since: since,
		ProbationUntil: row.ProbationUntil, AppealBlockedUntil: row.AppealBlockedUntil, LastAppealAt: row.LastAppealAt,
		DecidedBy: row.DecidedBy, DecidedAt: row.DecidedAt, ReasonCode: row.ReasonCode, ReasonText: row.ReasonText,
	}
}

func (r moderationRepo) State(ctx context.Context, userID int64) (ModerationState, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetUserModeration(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModerationState{UserID: userID, Status: StatusGood, DecidedBy: DecidedAutomatic}, nil
	}
	if err != nil {
		return ModerationState{}, mapError(err)
	}
	return stateFrom(moderationRow(row)), nil
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
		before = stateFrom(moderationRow(row))
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
		if after.DecidedBy == "" {
			after.DecidedBy = DecidedAutomatic
		}
		since := after.Since
		if since.IsZero() {
			since = time.Unix(0, 0)
		}
		if err := q.UpdateUserModeration(ctx, sqlcdb.UpdateUserModerationParams{
			UserID: userID, Status: after.Status, Score: after.Score, StatusSince: since,
			ProbationUntil: after.ProbationUntil, AppealBlockedUntil: after.AppealBlockedUntil, LastAppealAt: after.LastAppealAt,
			DecidedBy: after.DecidedBy, DecidedAt: after.DecidedAt, ReasonCode: after.ReasonCode, ReasonText: after.ReasonText,
		}); err != nil {
			return err
		}
		if after.Status == before.Status && after.Cause != CauseManual {
			return nil
		}
		return q.InsertUserModerationEvent(ctx, sqlcdb.InsertUserModerationEventParams{
			UserID: userID, FromStatus: before.Status, ToStatus: after.Status, Score: after.Score, Cause: after.Cause,
			ReasonCode: after.ReasonCode, ReasonText: after.ReasonText, StaffRef: after.StaffRef, Now: now,
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
		out[i] = ModerationEvent{
			ID: row.ID, UserID: row.UserID, FromStatus: row.FromStatus, ToStatus: row.ToStatus, Score: row.Score, Cause: row.Cause,
			ReasonCode: row.ReasonCode, ReasonText: row.ReasonText, StaffRef: row.StaffRef, CreatedAt: row.CreatedAt,
		}
	}
	return out, nil
}

func (r moderationRepo) DismissReports(ctx context.Context, targetID int64, reporterIDs []int64, now time.Time) (int, []ReportOutcome, error) {
	var dismissed int
	var outcomes []ReportOutcome
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		ids, err := q.DismissUserReports(ctx, sqlcdb.DismissUserReportsParams{Now: now, TargetID: targetID, ReporterIds: reporterIDs})
		if err != nil {
			return err
		}
		dismissed = len(ids)
		outcomes = nil
		if len(ids) == 0 {
			return nil
		}
		rows, err := q.InsertDismissedReportOutcomes(ctx, sqlcdb.InsertDismissedReportOutcomesParams{Now: now, TargetID: targetID, ReporterIds: ids})
		outcomes = outcomesFrom(rows)
		return err
	})
	return dismissed, outcomes, mapError(err)
}

func (r moderationRepo) NotifyUserReporters(ctx context.Context, targetID int64, outcome string, now time.Time) ([]ReportOutcome, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.InsertUserReportOutcomes(ctx, sqlcdb.InsertUserReportOutcomesParams{Outcome: outcome, Now: now, TargetID: targetID})
	if err != nil {
		return nil, mapError(err)
	}
	return outcomesFrom(rows), nil
}

func lockPack(ctx context.Context, q *sqlcdb.Queries, kind PackKind, packID int64) error {
	switch kind {
	case StickerPackKind:
		p, err := q.LockStickerPack(ctx, packID)
		if err != nil {
			return err
		}
		if p.DeletedAt != nil {
			return ErrNotFound
		}
		if p.BuiltinKey != nil {
			return ErrForbidden
		}
	case EmojiPackKind:
		p, err := q.LockEmojiPack(ctx, packID)
		if err != nil {
			return err
		}
		if p.DeletedAt != nil {
			return ErrNotFound
		}
	default:
		return ErrNotFound
	}
	return nil
}

func packOutcomes(ctx context.Context, q *sqlcdb.Queries, kind PackKind, packID int64, outcome string, now time.Time) ([]ReportOutcome, error) {
	var rows []sqlcdb.ReportOutcome
	var err error
	if kind == StickerPackKind {
		rows, err = q.InsertStickerPackReportOutcomes(ctx, sqlcdb.InsertStickerPackReportOutcomesParams{Outcome: outcome, Now: now, PackID: packID})
	} else {
		rows, err = q.InsertEmojiPackReportOutcomes(ctx, sqlcdb.InsertEmojiPackReportOutcomesParams{Outcome: outcome, Now: now, PackID: packID})
	}
	return outcomesFrom(rows), err
}

func (r moderationRepo) HidePack(ctx context.Context, kind PackKind, packID int64, now time.Time) (bool, []ReportOutcome, error) {
	var changed bool
	var outcomes []ReportOutcome
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := lockPack(ctx, q, kind, packID); err != nil {
			return err
		}
		var n int64
		var err error
		if kind == StickerPackKind {
			n, err = q.HideStickerPackByStaff(ctx, sqlcdb.HideStickerPackByStaffParams{ID: packID, Now: now})
		} else {
			n, err = q.HideEmojiPackByStaff(ctx, sqlcdb.HideEmojiPackByStaffParams{ID: packID, Now: now})
		}
		if err != nil {
			return err
		}
		changed = n > 0
		outcomes, err = packOutcomes(ctx, q, kind, packID, OutcomeActionTaken, now)
		return err
	})
	return changed, outcomes, mapError(err)
}

func (r moderationRepo) UnhidePack(ctx context.Context, kind PackKind, packID int64, now time.Time) (bool, []ReportOutcome, error) {
	var changed bool
	var outcomes []ReportOutcome
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := lockPack(ctx, q, kind, packID); err != nil {
			return err
		}
		var err error
		if outcomes, err = packOutcomes(ctx, q, kind, packID, OutcomeNoViolation, now); err != nil {
			return err
		}
		var n int64
		if kind == StickerPackKind {
			if n, err = q.UnhideStickerPack(ctx, packID); err == nil {
				err = q.ClearStickerPackReports(ctx, packID)
			}
		} else {
			if n, err = q.UnhideEmojiPack(ctx, packID); err == nil {
				err = q.ClearEmojiPackReports(ctx, packID)
			}
		}
		changed = n > 0
		return err
	})
	return changed, outcomes, mapError(err)
}

func (r moderationRepo) ReportOutcomes(ctx context.Context, reporterID int64, since time.Time, limit int) ([]ReportOutcome, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListReportOutcomes(ctx, sqlcdb.ListReportOutcomesParams{ReporterID: reporterID, Since: since, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	return outcomesFrom(rows), nil
}

func (r moderationRepo) MarkReportOutcomeRead(ctx context.Context, reporterID, id int64, now time.Time) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.MarkReportOutcomeRead(ctx, sqlcdb.MarkReportOutcomeReadParams{ID: id, ReporterID: reporterID, Now: now})
	if err != nil {
		return mapError(err)
	}
	if n > 0 {
		return nil
	}
	exists, err := r.s.q.ReportOutcomeExists(ctx, sqlcdb.ReportOutcomeExistsParams{ID: id, ReporterID: reporterID})
	if err != nil {
		return mapError(err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func (r moderationRepo) PurgeReportOutcomes(ctx context.Context, before time.Time) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.DeleteReportOutcomesBefore(ctx, before)
	return n, mapError(err)
}
