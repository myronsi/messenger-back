package postgres

import (
	"context"
	"time"
)

type MessageCount struct {
	Day         time.Time
	Type        string
	Messages    int64
	Attachments int64
}

type MessageCountRepository interface {
	Add(ctx context.Context, counts []MessageCount) error
	On(ctx context.Context, day time.Time) ([]MessageCount, error)
}

type messageCountRepo struct{ s *Store }

var _ MessageCountRepository = messageCountRepo{}

func (s *Store) MessageCounts() MessageCountRepository { return messageCountRepo{s} }

func (r messageCountRepo) Add(ctx context.Context, counts []MessageCount) error {
	if len(counts) == 0 {
		return nil
	}
	days := make([]string, len(counts))
	types := make([]string, len(counts))
	messages := make([]int64, len(counts))
	attachments := make([]int64, len(counts))
	for i, c := range counts {
		days[i] = dayString(c.Day)
		types[i], messages[i], attachments[i] = c.Type, c.Messages, c.Attachments
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO message_day_counts (day, type, messages, attachments)
		SELECT d::date, t, sum(m)::bigint, sum(a)::bigint
		FROM unnest($1::text[], $2::text[], $3::bigint[], $4::bigint[]) AS u(d, t, m, a)
		GROUP BY 1, 2 ORDER BY 1, 2
		ON CONFLICT (day, type) DO UPDATE
		SET messages = message_day_counts.messages + EXCLUDED.messages,
		    attachments = message_day_counts.attachments + EXCLUDED.attachments`,
		days, types, messages, attachments)
	return err
}

func (r messageCountRepo) On(ctx context.Context, day time.Time) ([]MessageCount, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `
		SELECT day, type, messages, attachments FROM message_day_counts
		WHERE day = $1::date ORDER BY type`, dayString(day))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MessageCount{}
	for rows.Next() {
		var c MessageCount
		if err := rows.Scan(&c.Day, &c.Type, &c.Messages, &c.Attachments); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func dayString(t time.Time) string { return t.UTC().Format(time.DateOnly) }
