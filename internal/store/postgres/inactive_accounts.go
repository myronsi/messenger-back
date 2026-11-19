package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type InactivityRule struct {
	ActiveBefore time.Time
	WarnBefore   time.Time
	Now          time.Time
	WarnFirst    bool
}

type InactiveAccount struct {
	UserID  int64
	Address string
}

type InactivityWarning struct {
	Username  string
	Address   string
	DeletesAt time.Time
}

type InactiveAccountRepository interface {
	ClearRevived(ctx context.Context) (int64, error)
	ToWarn(ctx context.Context, rule InactivityRule, after int64, limit int) ([]InactiveAccount, error)
	Warn(ctx context.Context, rule InactivityRule, userID int64, deletesAt time.Time, payload string) (bool, error)
	ToDelete(ctx context.Context, rule InactivityRule, after int64, limit int) ([]int64, error)
	Count(ctx context.Context, rule InactivityRule) (warn, del int64, err error)
	PendingWarning(ctx context.Context, userID int64) (InactivityWarning, error)
	Delete(ctx context.Context, rule InactivityRule, userID int64) (DeletedAccount, error)
}

type inactiveAccountRepo struct{ s *Store }

func (s *Store) InactiveAccounts() InactiveAccountRepository { return inactiveAccountRepo{s} }

const (
	inactiveSince = `u.created_at < $1 AND u.last_seen_at < $1
	AND NOT EXISTS (SELECT 1 FROM user_sessions s WHERE s.user_id = u.id AND s.last_active_at >= $1)`

	quietSinceWarning = `u.last_seen_at < w.warned_at
	AND NOT EXISTS (SELECT 1 FROM user_sessions s WHERE s.user_id = u.id AND s.last_active_at >= w.warned_at)`

	warnable = `FROM users u
	JOIN recovery_emails e ON e.user_id = u.id AND e.verified_at IS NOT NULL
	WHERE ` + inactiveSince + `
	AND NOT EXISTS (SELECT 1 FROM inactive_account_warnings w WHERE w.user_id = u.id)`

	deletable = `FROM users u
	LEFT JOIN recovery_emails e ON e.user_id = u.id AND e.verified_at IS NOT NULL
	LEFT JOIN inactive_account_warnings w ON w.user_id = u.id
	WHERE ` + inactiveSince + `
	AND (NOT $2::bool OR e.user_id IS NULL OR (w.deletes_at <= $3 AND ` + quietSinceWarning + `))`
)

func (r inactiveAccountRepo) ClearRevived(ctx context.Context) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM inactive_account_warnings w USING users u
		WHERE u.id = w.user_id AND NOT (`+quietSinceWarning+`)`)
	if err != nil {
		return 0, mapError(err)
	}
	return tag.RowsAffected(), nil
}

func (r inactiveAccountRepo) ToWarn(ctx context.Context, rule InactivityRule, after int64, limit int) ([]InactiveAccount, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `SELECT u.id, e.address `+warnable+` AND u.id > $2 ORDER BY u.id LIMIT $3`,
		rule.WarnBefore, after, limit)
	if err != nil {
		return nil, mapError(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (InactiveAccount, error) {
		var a InactiveAccount
		return a, row.Scan(&a.UserID, &a.Address)
	})
	return out, mapError(err)
}

func (r inactiveAccountRepo) Warn(ctx context.Context, rule InactivityRule, userID int64, deletesAt time.Time, payload string) (bool, error) {
	var warned bool
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		warned = false
		tag, err := tx.Exec(ctx, `INSERT INTO inactive_account_warnings (user_id, warned_at, deletes_at)
			SELECT u.id, $3, $4 `+warnable+` AND u.id = $2
			ON CONFLICT (user_id) DO NOTHING`, rule.WarnBefore, userID, rule.Now, deletesAt)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_mail_queue (kind, payload_sealed) VALUES ('inactive_account', $1)`, payload); err != nil {
			return err
		}
		warned = true
		return nil
	})
	return warned, mapError(err)
}

func (r inactiveAccountRepo) ToDelete(ctx context.Context, rule InactivityRule, after int64, limit int) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `SELECT u.id `+deletable+` AND u.id > $4 ORDER BY u.id LIMIT $5`,
		rule.ActiveBefore, rule.WarnFirst, rule.Now, after, limit)
	if err != nil {
		return nil, mapError(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	return out, mapError(err)
}

func (r inactiveAccountRepo) Count(ctx context.Context, rule InactivityRule) (warn, del int64, err error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	if rule.WarnFirst {
		if err := r.s.pool.QueryRow(ctx, `SELECT count(*) `+warnable, rule.WarnBefore).Scan(&warn); err != nil {
			return 0, 0, mapError(err)
		}
	}
	err = r.s.pool.QueryRow(ctx, `SELECT count(*) `+deletable, rule.ActiveBefore, rule.WarnFirst, rule.Now).Scan(&del)
	return warn, del, mapError(err)
}

func (r inactiveAccountRepo) PendingWarning(ctx context.Context, userID int64) (InactivityWarning, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var out InactivityWarning
	err := r.s.pool.QueryRow(ctx, `SELECT u.username, e.address, w.deletes_at
		FROM inactive_account_warnings w
		JOIN users u ON u.id = w.user_id
		JOIN recovery_emails e ON e.user_id = u.id AND e.verified_at IS NOT NULL
		WHERE w.user_id = $1 AND `+quietSinceWarning, userID).Scan(&out.Username, &out.Address, &out.DeletesAt)
	return out, mapError(err)
}

func (r inactiveAccountRepo) Delete(ctx context.Context, rule InactivityRule, userID int64) (DeletedAccount, error) {
	return r.s.deleteAccount(ctx, userID, func(ctx context.Context, tx pgx.Tx) error {
		var due bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 `+deletable+` AND u.id = $4)`,
			rule.ActiveBefore, rule.WarnFirst, rule.Now, userID).Scan(&due)
		if err != nil {
			return err
		}
		if !due {
			return ErrNotFound
		}
		return nil
	})
}
