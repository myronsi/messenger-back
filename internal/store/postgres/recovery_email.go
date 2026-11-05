package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

type RecoveryEmail struct {
	UserID     int64
	Address    string
	VerifiedAt *time.Time
}
type RecoveryMailJob struct {
	ID            int64
	Kind, Payload string
	Attempts      int
}
type RecoveryEmailRepository interface {
	Get(context.Context, int64) (RecoveryEmail, error)
	Set(context.Context, int64, RecoveryProof, string, string) error
	Remove(context.Context, int64, RecoveryProof) error
	Queue(context.Context, string, string) error
	Claim(context.Context) (RecoveryMailJob, error)
	FinishJob(context.Context, int64, bool, time.Duration) error
	FindVerified(context.Context, string) (RecoveryEmail, error)
	IssueToken(context.Context, RecoveryEmail, string, string, time.Time) error
	Verify(context.Context, string) error
	Token(context.Context, string, string) (RecoveryEmail, error)
	Reset(context.Context, int64, string, RecoveryProof, string, string) (PasswordReset, error)
}
type recoveryEmailRepo struct{ s *Store }

func (s *Store) RecoveryEmails() RecoveryEmailRepository { return recoveryEmailRepo{s} }

func (r recoveryEmailRepo) Get(ctx context.Context, uid int64) (RecoveryEmail, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	out := RecoveryEmail{UserID: uid}
	err := r.s.pool.QueryRow(ctx, `SELECT address,verified_at FROM recovery_emails WHERE user_id=$1`, uid).Scan(&out.Address, &out.VerifiedAt)
	return out, mapError(err)
}
func (r recoveryEmailRepo) Set(ctx context.Context, uid int64, proof RecoveryProof, address, payload string) error {
	return mapError(r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
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
		if _, err = tx.Exec(ctx, `INSERT INTO recovery_emails(user_id,address) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET address=excluded.address,verified_at=NULL,created_at=now()`, uid, address); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM email_tokens WHERE user_id=$1`, uid); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO recovery_mail_queue(kind,payload_sealed) VALUES('verify_email',$1)`, payload)
		return err
	}))
}
func (r recoveryEmailRepo) Remove(ctx context.Context, uid int64, proof RecoveryProof) error {
	return r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
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
		if _, err = tx.Exec(ctx, `DELETE FROM email_tokens WHERE user_id=$1`, uid); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM recovery_emails WHERE user_id=$1`, uid)
		return err
	})
}
func (r recoveryEmailRepo) Queue(ctx context.Context, kind, payload string) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `INSERT INTO recovery_mail_queue(kind,payload_sealed) VALUES($1,$2)`, kind, payload)
	return err
}
func (r recoveryEmailRepo) Claim(ctx context.Context) (RecoveryMailJob, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var out RecoveryMailJob
	err := r.s.pool.QueryRow(ctx, `UPDATE recovery_mail_queue SET attempts=attempts+1,available_at=now()+interval '2 minutes' WHERE id=(SELECT id FROM recovery_mail_queue WHERE available_at<=now() AND created_at>now()-interval '1 day' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,kind,payload_sealed,attempts`).Scan(&out.ID, &out.Kind, &out.Payload, &out.Attempts)
	return out, mapError(err)
}
func (r recoveryEmailRepo) FinishJob(ctx context.Context, id int64, done bool, delay time.Duration) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var err error
	if done {
		_, err = r.s.pool.Exec(ctx, `DELETE FROM recovery_mail_queue WHERE id=$1`, id)
	} else {
		_, err = r.s.pool.Exec(ctx, `UPDATE recovery_mail_queue SET available_at=now()+($2*interval '1 second') WHERE id=$1`, id, delay.Seconds())
	}
	return err
}
func (r recoveryEmailRepo) FindVerified(ctx context.Context, identifier string) (RecoveryEmail, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var out RecoveryEmail
	err := r.s.pool.QueryRow(ctx, `SELECT e.user_id,e.address,e.verified_at FROM recovery_emails e JOIN users u ON u.id=e.user_id WHERE e.verified_at IS NOT NULL AND (lower(e.address)=lower($1) OR lower(u.username)=lower($1))`, identifier).Scan(&out.UserID, &out.Address, &out.VerifiedAt)
	return out, mapError(err)
}
func (r recoveryEmailRepo) IssueToken(ctx context.Context, email RecoveryEmail, hash, purpose string, expires time.Time) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	tag, err := r.s.pool.Exec(ctx, `INSERT INTO email_tokens(token_hash,user_id,address,purpose,expires_at) SELECT $1,user_id,address,$3,$4 FROM recovery_emails WHERE user_id=$2 AND address=$5 AND ($3='verify_email' OR verified_at IS NOT NULL)`, hash, email.UserID, purpose, expires, email.Address)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}
func (r recoveryEmailRepo) Verify(ctx context.Context, hash string) error {
	return mapError(r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var uid int64
		if err := tx.QueryRow(ctx, `SELECT user_id FROM email_tokens WHERE token_hash=$1 AND purpose='verify_email' AND used_at IS NULL AND expires_at>now()`, hash).Scan(&uid); err != nil {
			return err
		}
		if err := recoveryChangesAllowed(ctx, sqlcdb.New(tx), uid); err != nil {
			return err
		}
		var address string
		if err := tx.QueryRow(ctx, `UPDATE email_tokens SET used_at=now() WHERE token_hash=$1 AND used_at IS NULL AND expires_at>now() RETURNING address`, hash).Scan(&address); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE recovery_emails SET verified_at=now() WHERE user_id=$1 AND address=$2`, uid, address)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	}))
}
func (r recoveryEmailRepo) Token(ctx context.Context, hash, purpose string) (RecoveryEmail, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var out RecoveryEmail
	err := r.s.pool.QueryRow(ctx, `SELECT t.user_id,t.address,e.verified_at FROM email_tokens t JOIN recovery_emails e ON e.user_id=t.user_id AND e.address=t.address WHERE t.token_hash=$1 AND t.purpose=$2 AND t.used_at IS NULL AND t.expires_at>now() AND e.verified_at IS NOT NULL`, hash, purpose).Scan(&out.UserID, &out.Address, &out.VerifiedAt)
	return out, mapError(err)
}
func (r recoveryEmailRepo) Reset(ctx context.Context, uid int64, hash string, proof RecoveryProof, password, key string) (PasswordReset, error) {
	var out PasswordReset
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockRecoveryState(ctx, tx, uid, proof); err != nil {
			return err
		}
		var address string
		if err := tx.QueryRow(ctx, `UPDATE email_tokens SET used_at=now() WHERE token_hash=$1 AND user_id=$2 AND purpose='password_reset' AND used_at IS NULL AND expires_at>now() RETURNING address`, hash, uid).Scan(&address); err != nil {
			return err
		}
		var verified bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_emails WHERE user_id=$1 AND address=$2 AND verified_at IS NOT NULL)`, uid, address).Scan(&verified); err != nil {
			return err
		}
		if !verified {
			return ErrNotFound
		}
		if err := consumeRecoveryFactor(ctx, tx, uid, proof.BackupCodeHash); err != nil {
			return err
		}
		var err error
		out, err = finishRecovery(ctx, tx, uid, password, key, "email")
		return err
	})
	return out, mapError(err)
}
