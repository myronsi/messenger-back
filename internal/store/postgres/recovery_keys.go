package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

var ErrRecoveryLocked = errors.New("recovery security delay is active")

// RecoveryKey contains server-side metadata, never a plaintext recovery key.
type RecoveryKey struct {
	Hash        *string
	CreatedAt   *time.Time
	LockedUntil *time.Time
}

// RecoveryProof pins the authentication state checked before a transaction starts.
type RecoveryProof struct {
	PasswordHash     string
	TwoFactorEnabled bool
	TwoFactorSecret  *string
	BackupCodeHash   *string
}

type RecoveryKeyRepository interface {
	Register(context.Context, string, string, string, string) (User, error)
	Get(context.Context, int64) (RecoveryKey, error)
	Replace(context.Context, int64, RecoveryProof, string) error
	Reset(context.Context, int64, string, RecoveryProof, string, string) (PasswordReset, error)
}

type recoveryKeyRepo struct{ s *Store }

func (s *Store) RecoveryKeys() RecoveryKeyRepository { return recoveryKeyRepo{s} }

func (r recoveryKeyRepo) transaction(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	return r.s.inRawTx(ctx, fn)
}

func (r recoveryKeyRepo) Register(ctx context.Context, username, name, passwordHash, keyHash string) (User, error) {
	var out User
	err := r.transaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcdb.New(tx)
		u, err := q.CreateUser(ctx, sqlcdb.CreateUserParams{Username: username, DisplayName: name, PasswordHash: passwordHash})
		if err != nil {
			return err
		}
		if err = q.EnsureSecuritySettings(ctx, u.ID); err != nil {
			return err
		}
		if err = q.EnsurePrivacySettings(ctx, u.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO user_recovery_keys(user_id,key_hash) VALUES($1,$2)", u.ID, keyHash); err != nil {
			return err
		}
		out = userFrom(u)
		return nil
	})
	return out, err
}

func (r recoveryKeyRepo) Get(ctx context.Context, uid int64) (RecoveryKey, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var out RecoveryKey
	err := r.s.pool.QueryRow(ctx, `SELECT k.key_hash,k.created_at,s.recovery_locked_until FROM users u
 LEFT JOIN user_recovery_keys k ON k.user_id=u.id
 LEFT JOIN user_security_settings s ON s.user_id=u.id WHERE u.id=$1`, uid).Scan(&out.Hash, &out.CreatedAt, &out.LockedUntil)
	return out, mapError(err)
}

// lockRecoveryState follows the common user -> settings lock order. Every 2FA change
// takes the same locks, so a recovery cannot race a change to its second factor.
func lockRecoveryState(ctx context.Context, tx pgx.Tx, uid int64, proof RecoveryProof) (*time.Time, error) {
	var password string
	if err := tx.QueryRow(ctx, "SELECT password_hash FROM users WHERE id=$1 FOR UPDATE", uid).Scan(&password); err != nil {
		return nil, err
	}
	var enabled bool
	if err := sqlcdb.New(tx).EnsureSecuritySettings(ctx, uid); err != nil {
		return nil, err
	}
	var secret *string
	var locked *time.Time
	if err := tx.QueryRow(ctx, `SELECT two_factor_enabled,two_factor_secret,recovery_locked_until
 FROM user_security_settings WHERE user_id=$1 FOR UPDATE`, uid).Scan(&enabled, &secret, &locked); err != nil {
		return nil, err
	}
	sameSecret := secret == nil && proof.TwoFactorSecret == nil || secret != nil && proof.TwoFactorSecret != nil && *secret == *proof.TwoFactorSecret
	if proof.PasswordHash != "" && password != proof.PasswordHash || enabled != proof.TwoFactorEnabled || !sameSecret {
		return nil, ErrConflict
	}
	return locked, nil
}

func consumeRecoveryFactor(ctx context.Context, tx pgx.Tx, uid int64, hash *string) error {
	if hash == nil {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE user_2fa_recovery_codes SET used_at=now()
 WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL`, uid, *hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (r recoveryKeyRepo) Replace(ctx context.Context, uid int64, proof RecoveryProof, keyHash string) error {
	return r.transaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		locked, err := lockRecoveryState(ctx, tx, uid, proof)
		if err != nil {
			return err
		}
		if locked != nil && locked.After(time.Now()) {
			return ErrRecoveryLocked
		}
		if err = consumeRecoveryFactor(ctx, tx, uid, proof.BackupCodeHash); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO user_recovery_keys(user_id,key_hash) VALUES($1,$2)
  ON CONFLICT(user_id) DO UPDATE SET key_hash=excluded.key_hash,created_at=now()`, uid, keyHash)
		return err
	})
}

// Reset consumes the key and optional 2FA backup code, sets the password, rotates
// the key, revokes sessions and invalidates outstanding legacy tokens atomically.
func (r recoveryKeyRepo) Reset(ctx context.Context, uid int64, expectedHash string, proof RecoveryProof, passwordHash, newKeyHash string) (PasswordReset, error) {
	var out PasswordReset
	err := r.transaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockRecoveryState(ctx, tx, uid, proof); err != nil {
			return err
		}
		var actual string
		if err := tx.QueryRow(ctx, "SELECT key_hash FROM user_recovery_keys WHERE user_id=$1 FOR UPDATE", uid).Scan(&actual); err != nil {
			return err
		}
		if actual != expectedHash {
			return ErrNotFound
		}
		if err := consumeRecoveryFactor(ctx, tx, uid, proof.BackupCodeHash); err != nil {
			return err
		}
		var err error
		out, err = finishRecovery(ctx, tx, uid, passwordHash, newKeyHash, "recovery_key")
		return err
	})
	return out, err
}

// Called inside the existing 2FA transactions, before modifying any factor state.
func recoveryChangesAllowed(ctx context.Context, q *sqlcdb.Queries, uid int64) error {
	if _, err := q.LockUser(ctx, uid); err != nil {
		return err
	}
	if err := q.EnsureSecuritySettings(ctx, uid); err != nil {
		return err
	}
	st, err := q.GetSecuritySettings(ctx, uid)
	if err != nil {
		return err
	}
	if st.RecoveryLockedUntil != nil && st.RecoveryLockedUntil.After(time.Now()) {
		return ErrRecoveryLocked
	}
	return nil
}
