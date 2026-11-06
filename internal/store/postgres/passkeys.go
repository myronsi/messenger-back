package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

type Passkey struct {
	ID               []byte
	UserID           int64
	Credential, Name string
	CreatedAt        time.Time
	LastUsedAt       *time.Time
}
type PasskeyLoginProof struct {
	ID                             []byte
	ExpectedCredential, Credential string
}
type WebAuthnChallenge struct {
	Hash, Purpose, Name string
	UserID              *int64
	SessionID           *uuid.UUID
	Payload             json.RawMessage
	ExpiresAt           time.Time
}
type PasskeyRepository interface {
	Handle(context.Context, int64, []byte) ([]byte, error)
	UserByHandle(context.Context, []byte) (int64, error)
	List(context.Context, int64) ([]Passkey, error)
	Get(context.Context, []byte) (Passkey, error)
	Challenge(context.Context, WebAuthnChallenge) error
	TakeChallenge(context.Context, string, string, *int64, *uuid.UUID) (WebAuthnChallenge, error)
	Add(context.Context, Passkey, uuid.UUID, RecoveryProof) error
	Rename(context.Context, int64, []byte, string) error
	Delete(context.Context, int64, []byte, string) error
}
type passkeyRepo struct{ s *Store }

func (s *Store) Passkeys() PasskeyRepository { return passkeyRepo{s} }

func (r passkeyRepo) Handle(ctx context.Context, uid int64, proposed []byte) ([]byte, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var handle []byte
	err := r.s.pool.QueryRow(ctx, `INSERT INTO user_webauthn_handles(user_id,handle) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET user_id=excluded.user_id RETURNING handle`, uid, proposed).Scan(&handle)
	return handle, mapError(err)
}
func (r passkeyRepo) UserByHandle(ctx context.Context, handle []byte) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var uid int64
	err := r.s.pool.QueryRow(ctx, `SELECT user_id FROM user_webauthn_handles WHERE handle=$1`, handle).Scan(&uid)
	return uid, mapError(err)
}
func scanPasskey(row pgx.Row) (Passkey, error) {
	var out Passkey
	err := row.Scan(&out.ID, &out.UserID, &out.Credential, &out.Name, &out.CreatedAt, &out.LastUsedAt)
	return out, mapError(err)
}
func (r passkeyRepo) List(ctx context.Context, uid int64) ([]Passkey, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `SELECT credential_id,user_id,credential,name,created_at,last_used_at FROM user_passkeys WHERE user_id=$1 ORDER BY created_at,credential_id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		key, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}
func (r passkeyRepo) Get(ctx context.Context, id []byte) (Passkey, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return scanPasskey(r.s.pool.QueryRow(ctx, `SELECT credential_id,user_id,credential,name,created_at,last_used_at FROM user_passkeys WHERE credential_id=$1`, id))
}
func (r passkeyRepo) Challenge(ctx context.Context, c WebAuthnChallenge) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `INSERT INTO webauthn_challenges(id_hash,user_id,session_id,purpose,payload,name,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, c.Hash, c.UserID, c.SessionID, c.Purpose, c.Payload, c.Name, c.ExpiresAt)
	return mapError(err)
}
func (r passkeyRepo) TakeChallenge(ctx context.Context, hash, purpose string, uid *int64, sid *uuid.UUID) (WebAuthnChallenge, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var out WebAuthnChallenge
	err := r.s.pool.QueryRow(ctx, `DELETE FROM webauthn_challenges WHERE id_hash=$1 AND purpose=$2 AND user_id IS NOT DISTINCT FROM $3::bigint AND session_id IS NOT DISTINCT FROM $4::uuid AND expires_at>now() RETURNING id_hash,user_id,session_id,purpose,payload,name,expires_at`, hash, purpose, uid, sid).Scan(&out.Hash, &out.UserID, &out.SessionID, &out.Purpose, &out.Payload, &out.Name, &out.ExpiresAt)
	return out, mapError(err)
}
func (r passkeyRepo) Add(ctx context.Context, key Passkey, sid uuid.UUID, proof RecoveryProof) error {
	return mapError(r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		locked, err := lockRecoveryState(ctx, tx, key.UserID, proof)
		if err != nil {
			return err
		}
		if locked != nil && locked.After(time.Now()) {
			return ErrRecoveryLocked
		}
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_sessions WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now())`, sid, key.UserID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return ErrNotFound
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM user_passkeys WHERE user_id=$1`, key.UserID).Scan(&count); err != nil {
			return err
		}
		if count >= 50 {
			return ErrConflict
		}
		if err = consumeRecoveryFactor(ctx, tx, key.UserID, proof.BackupCodeHash); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO user_passkeys(credential_id,user_id,credential,name) VALUES($1,$2,$3,$4)`, key.ID, key.UserID, key.Credential, key.Name)
		return err
	}))
}
func (r passkeyRepo) Rename(ctx context.Context, uid int64, id []byte, name string) error {
	return r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := recoveryChangesAllowed(ctx, sqlcdb.New(tx), uid); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE user_passkeys SET name=$3 WHERE credential_id=$1 AND user_id=$2`, id, uid, name)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	})
}
func (r passkeyRepo) Delete(ctx context.Context, uid int64, id []byte, passwordHash string) error {
	return r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcdb.New(tx)
		user, err := q.LockUser(ctx, uid)
		if err != nil {
			return err
		}
		if err := recoveryChangesAllowed(ctx, q, uid); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_passkeys WHERE user_id=$1`, uid).Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			if passwordHash == "" || passwordHash != user.PasswordHash {
				return ErrConflict
			}
		}
		tag, err := tx.Exec(ctx, `DELETE FROM user_passkeys WHERE credential_id=$1 AND user_id=$2`, id, uid)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	})
}
