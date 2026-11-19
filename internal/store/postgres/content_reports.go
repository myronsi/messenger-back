package postgres

import (
	"context"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

const (
	ContentReportMessage = "message"
	ContentReportGroup   = "group"
)

type ContentReport struct {
	ID          int64
	ReporterID  int64
	Kind        string
	ChatID      int64
	MessageID   int64
	TargetID    *int64
	Reason      string
	Note        *string
	Content     []byte
	CreatedAt   time.Time
	UpdatedAt   time.Time
	WithdrawnAt *time.Time
}

type ContentReportRepository interface {
	Save(ctx context.Context, r ContentReport, rateSince time.Time, maxInRate int) (id int64, inserted bool, err error)
	Withdraw(ctx context.Context, reporterID, chatID, messageID int64, now time.Time) (bool, error)
	Get(ctx context.Context, reporterID, chatID, messageID int64) (ContentReport, error)
	Purge(ctx context.Context, before time.Time) (int64, error)
}

type contentReportRepo struct{ s *Store }

var _ ContentReportRepository = contentReportRepo{}

func (s *Store) ContentReports() ContentReportRepository { return contentReportRepo{s} }

func (r contentReportRepo) Save(ctx context.Context, rep ContentReport, rateSince time.Time, maxInRate int) (int64, bool, error) {
	var id int64
	var inserted bool
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		reporter := rep.ReporterID
		row, err := q.UpsertContentReport(ctx, sqlcdb.UpsertContentReportParams{
			ReporterID: &reporter, Kind: rep.Kind, ChatID: rep.ChatID, MessageID: rep.MessageID, TargetID: rep.TargetID,
			Reason: rep.Reason, Note: rep.Note, Content: rep.Content, Now: rep.CreatedAt,
		})
		if err != nil {
			return err
		}
		n, err := q.CountReportsBesideContentReport(ctx, sqlcdb.CountReportsBesideContentReportParams{
			ReporterID: rep.ReporterID, Since: rateSince, ChatID: rep.ChatID, MessageID: rep.MessageID,
		})
		if err != nil {
			return err
		}
		if int(n) >= maxInRate {
			return ErrLimit
		}
		id, inserted = row.ID, row.Inserted
		return nil
	})
	return id, inserted, mapError(err)
}

func (r contentReportRepo) Withdraw(ctx context.Context, reporterID, chatID, messageID int64, now time.Time) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.WithdrawContentReport(ctx, sqlcdb.WithdrawContentReportParams{ReporterID: &reporterID, ChatID: chatID, MessageID: messageID, Now: now})
	return n > 0, mapError(err)
}

func (r contentReportRepo) Get(ctx context.Context, reporterID, chatID, messageID int64) (ContentReport, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetContentReport(ctx, sqlcdb.GetContentReportParams{ReporterID: &reporterID, ChatID: chatID, MessageID: messageID})
	if err != nil {
		return ContentReport{}, mapError(err)
	}
	out := ContentReport{
		ID: row.ID, Kind: row.Kind, ChatID: row.ChatID, MessageID: row.MessageID, TargetID: row.TargetID,
		Reason: row.Reason, Note: row.Note, Content: row.Content, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		WithdrawnAt: row.WithdrawnAt,
	}
	if row.ReporterID != nil {
		out.ReporterID = *row.ReporterID
	}
	return out, nil
}

func (r contentReportRepo) Purge(ctx context.Context, before time.Time) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.DeleteContentReportsBefore(ctx, before)
	return n, mapError(err)
}
