package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

func sessionFrom(s sqlcdb.UserSession) Session {
	return Session{
		ID: s.ID, UserID: s.UserID, UserAgent: s.UserAgent, IP: s.IpAddress,
		CreatedAt: s.CreatedAt, LastActiveAt: s.LastActiveAt, ExpiresAt: s.ExpiresAt, RevokedAt: s.RevokedAt,
	}
}

type sessionRepo struct{ s *Store }

var _ SessionRepository = sessionRepo{}

func (r sessionRepo) Create(ctx context.Context, n NewSession) (Session, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.CreateSession(ctx, sqlcdb.CreateSessionParams{
		UserID: n.UserID, RefreshTokenHash: n.RefreshTokenHash, UserAgent: n.UserAgent, IpAddress: n.IP, ExpiresAt: n.ExpiresAt,
	})
	if err != nil {
		return Session{}, mapError(err)
	}
	return sessionFrom(row), nil
}

func (r sessionRepo) Rotate(ctx context.Context, req RotateRequest) (RotateResult, error) {
	var out RotateResult
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		out = RotateResult{} // the transaction can be retried
		now := time.Now()

		cur, err := q.LockSessionByRefreshHash(ctx, req.PresentedHash)
		if err == nil {
			if cur.RevokedAt != nil || !cur.ExpiresAt.After(now) {
				return nil
			}
			days := DefaultSessionDays
			if st, err := q.GetSecuritySettings(ctx, cur.UserID); err == nil {
				days = int(st.SessionDurationDays)
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			next, err := q.RotateSession(ctx, sqlcdb.RotateSessionParams{
				ID: cur.ID, NewHash: req.NewHash, ExpiresAt: req.NewExpiresAt(days), UserAgent: req.UserAgent, IpAddress: req.IP,
			})
			if err != nil {
				return err
			}
			// The replaced token stays on record for as long as the session lives.
			if err := q.RecordRotatedToken(ctx, sqlcdb.RecordRotatedTokenParams{TokenHash: req.PresentedHash, SessionID: cur.ID}); err != nil {
				return err
			}
			out = RotateResult{Outcome: RotateOK, Session: sessionFrom(next)}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		// Not a current token. Is it one that was rotated out?
		used, err := q.GetRotatedToken(ctx, req.PresentedHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		old, err := q.LockSession(ctx, used.SessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the session was purged or deleted in the meantime
		}
		if err != nil {
			return err
		}
		if old.RevokedAt != nil || !old.ExpiresAt.After(now) {
			return nil
		}
		if now.Sub(used.RotatedAt) < req.ReuseGrace {
			out = RotateResult{Outcome: RotateStale}
			return nil
		}
		if _, err := q.RevokeSessionByID(ctx, old.ID); err != nil {
			return err
		}
		out = RotateResult{Outcome: RotateReuse, Session: sessionFrom(old)}
		return nil
	})
	if err != nil {
		return RotateResult{}, err
	}
	return out, nil
}

func (r sessionRepo) Get(ctx context.Context, id uuid.UUID) (Session, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetSession(ctx, id)
	if err != nil {
		return Session{}, mapError(err)
	}
	return sessionFrom(row), nil
}

func (r sessionRepo) ByRefreshHash(ctx context.Context, hash string) (Session, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetSessionByRefreshHash(ctx, hash)
	if err != nil {
		return Session{}, mapError(err)
	}
	return sessionFrom(row), nil
}

func (r sessionRepo) ListActive(ctx context.Context, userID int64) ([]Session, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListActiveSessions(ctx, userID)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]Session, len(rows))
	for i, row := range rows {
		out[i] = sessionFrom(row)
	}
	return out, nil
}

func (r sessionRepo) Revoke(ctx context.Context, userID int64, id uuid.UUID) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.RevokeSession(ctx, sqlcdb.RevokeSessionParams{ID: id, UserID: userID})
	if err != nil {
		return mapError(err)
	}
	if len(ids) == 0 {
		return ErrNotFound
	}
	return nil
}

// purgeBatch bounds one DELETE so a large backlog does not hold locks for long.
const purgeBatch = 1000

func (r sessionRepo) PurgeEnded(ctx context.Context, retention time.Duration) (sessions, rotatedTokens int64, err error) {
	cutoff := time.Now().Add(-retention)
	for {
		n, err := r.purgeOnce(ctx, func(ctx context.Context) (int64, error) {
			return r.s.q.DeleteEndedSessions(ctx, sqlcdb.DeleteEndedSessionsParams{Cutoff: cutoff, Batch: purgeBatch})
		})
		sessions += n
		if err != nil {
			return sessions, rotatedTokens, err
		}
		if n < purgeBatch {
			break
		}
	}
	for {
		n, err := r.purgeOnce(ctx, func(ctx context.Context) (int64, error) {
			return r.s.q.DeleteOldRotatedTokens(ctx, sqlcdb.DeleteOldRotatedTokensParams{Cutoff: cutoff, Batch: purgeBatch})
		})
		rotatedTokens += n
		if err != nil {
			return sessions, rotatedTokens, err
		}
		if n < purgeBatch {
			return sessions, rotatedTokens, nil
		}
	}
}

func (r sessionRepo) purgeOnce(ctx context.Context, del func(context.Context) (int64, error)) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := del(ctx)
	return n, mapError(err)
}

func (r sessionRepo) RevokeByID(ctx context.Context, id uuid.UUID) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.RevokeSessionByID(ctx, id)
	return len(ids) > 0, mapError(err)
}

func (r sessionRepo) RevokeOthers(ctx context.Context, userID int64, keepID uuid.UUID) ([]uuid.UUID, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.RevokeOtherSessions(ctx, sqlcdb.RevokeOtherSessionsParams{UserID: userID, KeepID: keepID})
	return ids, mapError(err)
}

func (r sessionRepo) RevokeAll(ctx context.Context, userID int64) ([]uuid.UUID, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.RevokeAllSessions(ctx, userID)
	return ids, mapError(err)
}

func (r sessionRepo) Touch(ctx context.Context, id uuid.UUID) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.TouchSession(ctx, id)
	return affected(n, err)
}

type settingsRepo struct{ s *Store }

var _ SecuritySettingsRepository = settingsRepo{}

func (r settingsRepo) Get(ctx context.Context, userID int64) (SecuritySettings, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetSecuritySettings(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SecuritySettings{SessionDurationDays: DefaultSessionDays}, nil
	}
	if err != nil {
		return SecuritySettings{}, mapError(err)
	}
	return SecuritySettings{
		SessionDurationDays:    int(row.SessionDurationDays),
		TwoFactorEnabled:       row.TwoFactorEnabled,
		TwoFactorSecret:        row.TwoFactorSecret,
		PendingTwoFactorSecret: row.PendingTwoFactorSecret,
	}, nil
}

func (r settingsRepo) SetSessionDuration(ctx context.Context, userID int64, days int) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := q.EnsureSecuritySettings(ctx, userID); err != nil {
			return err
		}
		n, err := q.SetSessionDuration(ctx, sqlcdb.SetSessionDurationParams{UserID: userID, Days: int32(days)}) //nolint:gosec // validated by the caller, bounded by a CHECK
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (r settingsRepo) BeginTwoFactor(ctx context.Context, userID int64, sealedSecret string) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := q.EnsureSecuritySettings(ctx, userID); err != nil {
			return err
		}
		n, err := q.SetPendingTwoFactorSecret(ctx, sqlcdb.SetPendingTwoFactorSecretParams{UserID: userID, Secret: &sealedSecret})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrConflict
		}
		return nil
	})
}

func (r settingsRepo) ConfirmTwoFactor(ctx context.Context, userID int64, sealedPending string, hashes []string) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		n, err := q.EnableTwoFactor(ctx, sqlcdb.EnableTwoFactorParams{UserID: userID, PendingSecret: &sealedPending})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrConflict
		}
		if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
			return err
		}
		return q.InsertRecoveryCodes(ctx, sqlcdb.InsertRecoveryCodesParams{UserID: userID, CodeHashes: hashes})
	})
}

func (r settingsRepo) DisableTwoFactor(ctx context.Context, userID int64) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if _, err := q.DisableTwoFactor(ctx, userID); err != nil {
			return err
		}
		return q.DeleteRecoveryCodes(ctx, userID)
	})
}

func (r settingsRepo) UseRecoveryCode(ctx context.Context, userID int64, codeHash string) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.UseRecoveryCode(ctx, sqlcdb.UseRecoveryCodeParams{UserID: userID, CodeHash: codeHash})
	return n == 1, mapError(err)
}

func (r settingsRepo) CountRecoveryCodes(ctx context.Context, userID int64) (int, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.CountUnusedRecoveryCodes(ctx, userID)
	return int(n), mapError(err)
}

type recoveryRepo struct{ s *Store }

var _ RecoveryTokenRepository = recoveryRepo{}

func (r recoveryRepo) Issue(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.CreateRecoveryToken(ctx, sqlcdb.CreateRecoveryTokenParams{JtiHash: tokenHash, UserID: userID, ExpiresAt: expiresAt}))
}

func (r recoveryRepo) ResetPassword(ctx context.Context, tokenHash, newPasswordHash string) (PasswordReset, error) {
	var out PasswordReset
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		out = PasswordReset{} // the transaction can be retried
		uid, err := q.ConsumeRecoveryToken(ctx, tokenHash)
		if err != nil {
			return err
		}
		if _, err := q.LockUser(ctx, uid); err != nil {
			return err
		}
		if _, err := q.SetPasswordHash(ctx, sqlcdb.SetPasswordHashParams{ID: uid, PasswordHash: newPasswordHash}); err != nil {
			return err
		}
		ids, err := q.RevokeAllSessions(ctx, uid)
		if err != nil {
			return err
		}
		out = PasswordReset{UserID: uid, RevokedSessions: ids}
		return nil
	})
	if err != nil {
		return PasswordReset{}, err
	}
	return out, nil
}

func (r recoveryRepo) DeleteStale(ctx context.Context) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.DeleteStaleRecoveryTokens(ctx)
	return n, mapError(err)
}
