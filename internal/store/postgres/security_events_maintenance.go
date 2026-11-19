package postgres

import (
	"context"
	"time"
)

const SecurityEventRetention = 365 * 24 * time.Hour

func (s *Store) PurgeSecurityEvents(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention)
	var total int64
	for {
		callCtx, cancel := s.call(ctx)
		tag, err := s.pool.Exec(callCtx, `DELETE FROM user_security_events WHERE id IN (SELECT id FROM user_security_events WHERE created_at < $1 ORDER BY created_at LIMIT $2)`, cutoff, purgeBatch)
		cancel()
		if err != nil {
			return total, mapError(err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < purgeBatch {
			return total, nil
		}
	}
}
