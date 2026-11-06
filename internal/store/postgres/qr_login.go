package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/geoip"
	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

var (
	ErrQRLoginDecided = errors.New("qr login request already decided")
	ErrStaleApproval  = errors.New("approving session is revoked, expired or idle")
)

type QRLogin struct {
	IDHash, PollHash, Status string
	UserAgent                *string
	Location                 *geoip.Location
	ScannedBy, ApprovedBy    *int64
	ApprovedSession          *uuid.UUID
	PasswordFingerprint      *string
	CreatedAt, ExpiresAt     time.Time
	UsedAt                   *time.Time
}

func (q QRLogin) open(now time.Time) bool {
	return q.UsedAt == nil && q.ExpiresAt.After(now)
}

func (q QRLogin) visibleTo(uid int64, now time.Time) bool {
	return q.open(now) && (q.ScannedBy == nil || *q.ScannedBy == uid)
}

type QRLoginRepository interface {
	Create(context.Context, QRLogin) error
	Get(context.Context, string) (QRLogin, error)
	Scan(context.Context, string, int64) (QRLogin, error)
	ApproverActive(context.Context, int64, uuid.UUID, time.Duration) error
	Approve(context.Context, string, int64, uuid.UUID, RecoveryProof, time.Duration) (QRLogin, error)
	Deny(context.Context, string, int64) (QRLogin, error)
	Complete(context.Context, string, string, NewSession, time.Duration, time.Time) (Session, error)
}

type qrLoginRepo struct{ s *Store }

func (s *Store) QRLogins() QRLoginRepository { return qrLoginRepo{s} }

const qrLoginColumns = `id_hash,poll_hash,status,user_agent,location,scanned_by,approved_by,approved_session,password_fingerprint,created_at,expires_at,used_at`

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanQRLogin(row pgx.Row) (QRLogin, error) {
	var out QRLogin
	var location []byte
	err := row.Scan(&out.IDHash, &out.PollHash, &out.Status, &out.UserAgent, &location, &out.ScannedBy, &out.ApprovedBy, &out.ApprovedSession, &out.PasswordFingerprint, &out.CreatedAt, &out.ExpiresAt, &out.UsedAt)
	if err != nil {
		return out, mapError(err)
	}
	out.Location = decodeLocation(location)
	return out, nil
}

func lockQRLogin(ctx context.Context, tx pgx.Tx, hash string) (QRLogin, error) {
	return scanQRLogin(tx.QueryRow(ctx, `SELECT `+qrLoginColumns+` FROM qr_login_requests WHERE id_hash=$1 FOR UPDATE`, hash))
}

func (r qrLoginRepo) Create(ctx context.Context, q QRLogin) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `INSERT INTO qr_login_requests(id_hash,poll_hash,user_agent,location,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6)`,
		q.IDHash, q.PollHash, q.UserAgent, encodeLocation(q.Location), q.CreatedAt, q.ExpiresAt)
	return mapError(err)
}

func (r qrLoginRepo) Get(ctx context.Context, hash string) (QRLogin, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return scanQRLogin(r.s.pool.QueryRow(ctx, `SELECT `+qrLoginColumns+` FROM qr_login_requests WHERE id_hash=$1`, hash))
}

func (r qrLoginRepo) Scan(ctx context.Context, hash string, uid int64) (QRLogin, error) {
	var out QRLogin
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		row, err := lockQRLogin(ctx, tx, hash)
		if err != nil {
			return err
		}
		if !row.visibleTo(uid, time.Now()) {
			return ErrNotFound
		}
		if row.Status == "pending" {
			if _, err = tx.Exec(ctx, `UPDATE qr_login_requests SET status='scanned',scanned_by=$2 WHERE id_hash=$1`, hash, uid); err != nil {
				return err
			}
			row.Status, row.ScannedBy = "scanned", &uid
		}
		out = row
		return nil
	})
	return out, mapError(err)
}

func (r qrLoginRepo) ApproverActive(ctx context.Context, uid int64, sid uuid.UUID, maxAge time.Duration) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	err := recentRecoverySession(ctx, r.s.pool, uid, sid, maxAge)
	if errors.Is(err, ErrNotFound) {
		return ErrStaleApproval
	}
	return mapError(err)
}

func passwordFingerprint(hash string) string {
	sum := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(sum[:])
}

func decidableQRLogin(ctx context.Context, tx pgx.Tx, hash string, uid int64) (QRLogin, error) {
	row, err := lockQRLogin(ctx, tx, hash)
	if err != nil {
		return row, err
	}
	if !row.visibleTo(uid, time.Now()) {
		return row, ErrNotFound
	}
	if row.Status != "pending" && row.Status != "scanned" {
		return row, ErrQRLoginDecided
	}
	return row, nil
}

func (r qrLoginRepo) Approve(ctx context.Context, hash string, uid int64, sid uuid.UUID, proof RecoveryProof, maxAge time.Duration) (QRLogin, error) {
	var out QRLogin
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		row, err := decidableQRLogin(ctx, tx, hash, uid)
		if err != nil {
			return err
		}
		if _, err = lockRecoveryState(ctx, tx, uid, proof); err != nil {
			return err
		}
		if err = recentRecoverySession(ctx, tx, uid, sid, maxAge); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrStaleApproval
			}
			return err
		}
		if err = consumeRecoveryFactor(ctx, tx, uid, proof.BackupCodeHash); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrInvalid
			}
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE qr_login_requests SET status='approved',scanned_by=$2,approved_by=$2,approved_session=$3,password_fingerprint=$4 WHERE id_hash=$1`,
			hash, uid, sid, passwordFingerprint(proof.PasswordHash))
		row.Status = "approved"
		out = row
		return err
	})
	return out, mapError(err)
}

func (r qrLoginRepo) Deny(ctx context.Context, hash string, uid int64) (QRLogin, error) {
	var out QRLogin
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		row, err := decidableQRLogin(ctx, tx, hash, uid)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE qr_login_requests SET status='denied',scanned_by=$2 WHERE id_hash=$1`, hash, uid)
		row.Status = "denied"
		out = row
		return err
	})
	return out, mapError(err)
}

func (r qrLoginRepo) Complete(ctx context.Context, hash, pollHash string, n NewSession, maxAge time.Duration, now time.Time) (Session, error) {
	var out Session
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		out = Session{}
		row, err := lockQRLogin(ctx, tx, hash)
		if err != nil {
			return err
		}
		if !hmac.Equal([]byte(row.PollHash), []byte(pollHash)) || row.Status != "approved" || !row.open(time.Now()) ||
			row.ApprovedBy == nil || row.ApprovedSession == nil || row.PasswordFingerprint == nil {
			return ErrNotFound
		}
		uid := *row.ApprovedBy
		if err = recentRecoverySession(ctx, tx, uid, *row.ApprovedSession, maxAge); err != nil {
			return err
		}
		q := sqlcdb.New(tx)
		user, err := q.LockUser(ctx, uid)
		if err != nil {
			return err
		}
		if passwordFingerprint(user.PasswordHash) != *row.PasswordFingerprint {
			return ErrNotFound
		}
		days := DefaultSessionDays
		if st, err := q.GetSecuritySettings(ctx, uid); err == nil {
			days = int(st.SessionDurationDays)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE qr_login_requests SET used_at=now() WHERE id_hash=$1`, hash); err != nil {
			return err
		}
		created, err := q.CreateSession(ctx, sqlcdb.CreateSessionParams{UserID: uid, RefreshTokenHash: n.RefreshTokenHash, UserAgent: n.UserAgent, IpAddress: n.IP, Location: encodeLocation(n.Location), ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour)})
		if err != nil {
			return err
		}
		out = sessionFrom(created)
		return nil
	})
	return out, mapError(err)
}
