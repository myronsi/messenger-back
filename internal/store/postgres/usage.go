package postgres

import (
	"context"
	"time"
)

type UsageFigures struct {
	ActiveDay    int64
	Active30d    int64
	StorageBytes int64
}

func (s *Store) Usage(ctx context.Context, dayStart, dayEnd time.Time) (UsageFigures, error) {
	ctx, cancel := s.call(ctx)
	defer cancel()
	var u UsageFigures
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(DISTINCT user_id) FROM user_sessions WHERE last_active_at >= $1),
		(SELECT count(DISTINCT user_id) FROM user_sessions WHERE last_active_at >= $2::timestamptz - interval '30 days'),
		(SELECT coalesce(sum(size), 0)::bigint FROM attachments)
			+ (SELECT coalesce(sum((v.value->>'size')::bigint), 0)::bigint FROM attachments a, jsonb_each(a.variants) v)`,
		dayStart, dayEnd).Scan(&u.ActiveDay, &u.Active30d, &u.StorageBytes)
	return u, err
}
