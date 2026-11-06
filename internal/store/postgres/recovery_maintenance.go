package postgres

import "context"

// PurgeRecovery removes expired capabilities and abandoned encrypted mail jobs. Each
// delete is bounded so cleanup cannot hold a large transaction open during login.
func (s *Store) PurgeRecovery(ctx context.Context) (int64, error) {
	queries := []string{
		`DELETE FROM recovery_requests WHERE id_hash IN (SELECT id_hash FROM recovery_requests WHERE expires_at <= now() ORDER BY expires_at LIMIT 1000)`,
		`DELETE FROM qr_login_requests WHERE id_hash IN (SELECT id_hash FROM qr_login_requests WHERE expires_at <= now() ORDER BY expires_at LIMIT 1000)`,
		`DELETE FROM webauthn_challenges WHERE id_hash IN (SELECT id_hash FROM webauthn_challenges WHERE expires_at <= now() ORDER BY expires_at LIMIT 1000)`,
		`DELETE FROM email_tokens WHERE token_hash IN (SELECT token_hash FROM email_tokens WHERE expires_at <= now() ORDER BY expires_at LIMIT 1000)`,
		`DELETE FROM recovery_mail_queue WHERE id IN (SELECT id FROM recovery_mail_queue WHERE created_at <= now()-interval '1 day' ORDER BY created_at LIMIT 1000)`,
	}
	var total int64
	for _, query := range queries {
		for {
			callCtx, cancel := s.call(ctx)
			tag, err := s.pool.Exec(callCtx, query)
			cancel()
			if err != nil {
				return total, mapError(err)
			}
			total += tag.RowsAffected()
			if tag.RowsAffected() < purgeBatch {
				break
			}
		}
	}
	return total, nil
}
