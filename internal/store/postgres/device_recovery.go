package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/geoip"
	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

type DeviceRecovery struct {
	Hash, ApprovalHash string
	Status, UserAgent  string
	Location           *geoip.Location
	UserID             int64
	Match              int
	Choices            []int
	ExpiresAt          time.Time
	ApprovedBy         *uuid.UUID
	Proof              RecoveryProof
}

type RecoveryRequestRepository interface {
	Create(context.Context, DeviceRecovery) (bool, error)
	Get(context.Context, string) (DeviceRecovery, error)
	GetForApproval(context.Context, string) (DeviceRecovery, error)
	Decide(context.Context, string, int64, uuid.UUID, int, RecoveryProof, time.Duration, bool) (string, error)
	Complete(context.Context, DeviceRecovery, string, string, time.Duration) (PasswordReset, error)
}

type recoveryRequestRepo struct{ s *Store }

func (s *Store) RecoveryRequests() RecoveryRequestRepository { return recoveryRequestRepo{s} }

func (r recoveryRequestRepo) Create(ctx context.Context, request DeviceRecovery) (bool, error) {
	created := false
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		created = false
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id=$1 FOR UPDATE`, request.UserID); err != nil {
			return err
		}
		var blocked bool
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT recovery_requests_blocked_until>now() FROM user_security_settings WHERE user_id=$1),false)`, request.UserID).Scan(&blocked); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM recovery_requests WHERE user_id=$1 AND status IN ('pending','approved') AND expires_at>now()`, request.UserID).Scan(&pending); err != nil {
			return err
		}
		genuine := request.UserID != 0 && !blocked && pending < 3
		owner := &request.UserID
		if !genuine {
			owner = nil
		}
		choices, err := json.Marshal(request.Choices)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO recovery_requests(id_hash,approval_hash,user_id,match_number,choices,user_agent,location,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, request.Hash, request.ApprovalHash, owner, request.Match, choices, request.UserAgent, encodeLocation(request.Location), request.ExpiresAt)
		created = err == nil && genuine
		return err
	})
	return created, mapError(err)
}

func scanDeviceRecovery(row pgx.Row) (DeviceRecovery, error) {
	var out DeviceRecovery
	var choices, proof, location []byte
	err := row.Scan(&out.Hash, &out.ApprovalHash, &out.UserID, &out.Match, &out.Status, &out.UserAgent, &location, &choices, &out.ExpiresAt, &out.ApprovedBy, &proof)
	if err != nil {
		return out, mapError(err)
	}
	out.Location = decodeLocation(location)
	if err = json.Unmarshal(choices, &out.Choices); err != nil {
		return out, err
	}
	if len(proof) > 0 {
		err = json.Unmarshal(proof, &out.Proof)
	}
	return out, err
}

const recoveryRequestColumns = `id_hash,COALESCE(approval_hash,''),COALESCE(user_id,0),match_number,status,user_agent,location,choices,expires_at,approved_by_session,approved_proof`

func (r recoveryRequestRepo) Get(ctx context.Context, hash string) (DeviceRecovery, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return scanDeviceRecovery(r.s.pool.QueryRow(ctx, `SELECT `+recoveryRequestColumns+` FROM recovery_requests WHERE id_hash=$1`, hash))
}

func (r recoveryRequestRepo) GetForApproval(ctx context.Context, hash string) (DeviceRecovery, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return scanDeviceRecovery(r.s.pool.QueryRow(ctx, `SELECT `+recoveryRequestColumns+` FROM recovery_requests WHERE approval_hash=$1`, hash))
}

func recentRecoverySession(ctx context.Context, tx rowQuerier, uid int64, sid uuid.UUID, maxAge time.Duration) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_sessions WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now() AND last_active_at>now()-($3*interval '1 second'))`, sid, uid, maxAge.Seconds()).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return ErrNotFound
	}
	return nil
}

func (r recoveryRequestRepo) Decide(ctx context.Context, hash string, uid int64, sid uuid.UUID, match int, proof RecoveryProof, maxAge time.Duration, deny bool) (string, error) {
	status := ""
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockRecoveryState(ctx, tx, uid, proof); err != nil {
			return err
		}
		if err := recentRecoverySession(ctx, tx, uid, sid, maxAge); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrStaleApproval
			}
			return err
		}
		request, err := scanDeviceRecovery(tx.QueryRow(ctx, `SELECT `+recoveryRequestColumns+` FROM recovery_requests WHERE id_hash=$1 AND user_id=$2 FOR UPDATE`, hash, uid))
		if err != nil {
			return err
		}
		if request.Status != "pending" || !request.ExpiresAt.After(time.Now()) {
			return ErrNotFound
		}
		if deny || request.Match != match {
			status = "denied"
			if _, err = tx.Exec(ctx, `UPDATE user_security_settings SET recovery_requests_blocked_until=now()+interval '15 minutes' WHERE user_id=$1`, uid); err != nil {
				return err
			}
		} else {
			if err = consumeRecoveryFactor(ctx, tx, uid, proof.BackupCodeHash); err != nil {
				if errors.Is(err, ErrNotFound) {
					return ErrInvalid
				}
				return err
			}
			status = "approved"
		}
		pinned := proof
		pinned.BackupCodeHash = nil
		encoded, err := json.Marshal(pinned)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE recovery_requests SET status=$2,approved_by_session=$3,approved_proof=$4 WHERE id_hash=$1`, hash, status, sid, encoded)
		return err
	})
	return status, mapError(err)
}

// finishRecovery is called after a method's proof and one-time capability are
// locked and verified in the same transaction. Every method gets the same delay
// and session revocation; no method can bypass second-factor checks.
func finishRecovery(ctx context.Context, tx pgx.Tx, uid int64, password, key, method string) (PasswordReset, error) {
	var out PasswordReset
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1`, uid, password); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_recovery_keys(user_id,key_hash) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET key_hash=excluded.key_hash,created_at=now()`, uid, key); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE user_security_settings SET recovery_locked_until=now()+interval '24 hours' WHERE user_id=$1`, uid); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM email_tokens WHERE user_id=$1`, uid); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE recovery_requests SET status='denied' WHERE user_id=$1 AND status IN ('pending','approved')`, uid); err != nil {
		return out, err
	}
	passkeys, err := tx.Exec(ctx, `DELETE FROM user_passkeys WHERE user_id=$1`, uid)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM webauthn_challenges WHERE user_id=$1`, uid); err != nil {
		return out, err
	}
	var email int64
	if method != "email" {
		tag, err := tx.Exec(ctx, `DELETE FROM recovery_emails WHERE user_id=$1`, uid)
		if err != nil {
			return out, err
		}
		email = tag.RowsAffected()
	}
	ids, err := sqlcdb.New(tx).RevokeAllSessions(ctx, uid)
	if err != nil {
		return out, err
	}
	details, err := json.Marshal(map[string]any{"method": method, "passkeys_removed": passkeys.RowsAffected(), "recovery_email_removed": email > 0})
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO user_security_events(user_id,event_type,details) VALUES($1,'password_reset',$2)`, uid, details)
	return PasswordReset{UserID: uid, RevokedSessions: ids}, err
}

func (r recoveryRequestRepo) Complete(ctx context.Context, request DeviceRecovery, password, key string, maxAge time.Duration) (PasswordReset, error) {
	var out PasswordReset
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockRecoveryState(ctx, tx, request.UserID, request.Proof); err != nil {
			return err
		}
		current, err := scanDeviceRecovery(tx.QueryRow(ctx, `SELECT `+recoveryRequestColumns+` FROM recovery_requests WHERE id_hash=$1 FOR UPDATE`, request.Hash))
		if err != nil {
			return err
		}
		if current.UserID != request.UserID || current.Status != "approved" || !current.ExpiresAt.After(time.Now()) || current.ApprovedBy == nil {
			return ErrNotFound
		}
		if err = recentRecoverySession(ctx, tx, current.UserID, *current.ApprovedBy, maxAge); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE recovery_requests SET status='used' WHERE id_hash=$1`, request.Hash); err != nil {
			return err
		}
		out, err = finishRecovery(ctx, tx, current.UserID, password, key, "device_approval")
		return err
	})
	return out, mapError(err)
}
