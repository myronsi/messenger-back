package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type NotificationPreferences struct {
	Enabled  bool `json:"enabled"`
	Sound    bool `json:"sound"`
	Previews bool `json:"previews"`
}
type NotificationMute struct {
	ChatID string    `json:"chat_id"`
	Until  time.Time `json:"until"`
}
type NotificationDevice struct {
	ID          uuid.UUID `json:"id"`
	Platform    string    `json:"platform"`
	Destination string    `json:"destination"`
	P256dh      string    `json:"p256dh,omitempty"`
	Auth        string    `json:"auth,omitempty"`
	SessionID   uuid.UUID `json:"-"`
}
type NotificationJob struct {
	MessageID, ChatID int64
	Recipients        []int64
	CreatedAt         time.Time
	Attempts          int
}

// NotificationRepository contains preferences, session-bound destinations and the durable send queue.
type NotificationRepository interface {
	Preferences(context.Context, int64) (NotificationPreferences, error)
	SetPreferences(context.Context, int64, NotificationPreferences) error
	Mutes(context.Context, int64) ([]NotificationMute, error)
	Mute(context.Context, int64, int64, time.Time) error
	Register(context.Context, int64, uuid.UUID, NotificationDevice) error
	Unregister(context.Context, int64, uuid.UUID, uuid.UUID) error
	Activity(context.Context, int64, uuid.UUID, uuid.UUID, uuid.UUID, bool) error
	Enqueue(context.Context, int64, int64, []int64) error
	Claim(context.Context) (NotificationJob, error)
	Finish(context.Context, int64) error
	Devices(context.Context, int64, int64, int64) ([]NotificationDevice, error)
	Delivered(context.Context, int64, uuid.UUID) error
	RemoveDevice(context.Context, uuid.UUID) error
	DeviceActive(context.Context, int64, uuid.UUID, uuid.UUID) (bool, error)
}
type notificationRepo struct{ s *Store }

func (s *Store) Notifications() NotificationRepository { return notificationRepo{s} }

func (r notificationRepo) Preferences(ctx context.Context, uid int64) (NotificationPreferences, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p := NotificationPreferences{Enabled: true}
	err := r.s.pool.QueryRow(ctx, `SELECT enabled,sound,previews FROM notification_preferences WHERE user_id=$1`, uid).Scan(&p.Enabled, &p.Sound, &p.Previews)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return p, err
}
func (r notificationRepo) SetPreferences(ctx context.Context, uid int64, p NotificationPreferences) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `INSERT INTO notification_preferences(user_id,enabled,sound,previews) VALUES($1,$2,$3,$4)
 ON CONFLICT(user_id) DO UPDATE SET enabled=$2,sound=$3,previews=$4`, uid, p.Enabled, p.Sound, p.Previews)
	return err
}
func (r notificationRepo) Mutes(ctx context.Context, uid int64) ([]NotificationMute, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `SELECT chat_id::text,until_at FROM notification_mutes WHERE user_id=$1 AND until_at>now()`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationMute{}
	for rows.Next() {
		var m NotificationMute
		if err := rows.Scan(&m.ChatID, &m.Until); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (r notificationRepo) Mute(ctx context.Context, uid, cid int64, until time.Time) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	// The insert checks membership as part of the same statement.
	tag, err := r.s.pool.Exec(ctx, `INSERT INTO notification_mutes(user_id,chat_id,until_at)
 SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM participants WHERE user_id=$1 AND chat_id=$2)
 ON CONFLICT(user_id,chat_id) DO UPDATE SET until_at=$3`, uid, cid, until)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
func (r notificationRepo) Register(ctx context.Context, uid int64, sid uuid.UUID, d NotificationDevice) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var owner int64
		if err := tx.QueryRow(ctx, `SELECT user_id FROM user_sessions WHERE id=$1 AND revoked_at IS NULL AND expires_at>now() FOR UPDATE`, sid).Scan(&owner); err != nil {
			return mapError(err)
		}
		if owner != uid {
			return ErrNotFound
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM notification_devices WHERE session_id=$1 AND destination<>$2`, sid, d.Destination).Scan(&count); err != nil {
			return err
		}
		if count >= 16 {
			return ErrConflict
		}
		_, err := tx.Exec(ctx, `INSERT INTO notification_devices(id,user_id,session_id,platform,destination,p256dh,auth)
	 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(destination) DO UPDATE SET user_id=$2,session_id=$3,platform=$4,p256dh=$6,auth=$7,updated_at=now()`, d.ID, uid, sid, d.Platform, d.Destination, d.P256dh, d.Auth)
		return err
	})
}
func (r notificationRepo) Unregister(ctx context.Context, uid int64, sid, id uuid.UUID) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `DELETE FROM notification_devices WHERE id=$1 AND user_id=$2 AND session_id=$3`, id, uid, sid)
	return err
}
func (r notificationRepo) Enqueue(ctx context.Context, cid, mid int64, recipients []int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `INSERT INTO notification_jobs(chat_id,message_id,recipients) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, cid, mid, recipients)
	return err
}
func (r notificationRepo) Claim(ctx context.Context) (NotificationJob, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	if _, err := r.s.pool.Exec(ctx, `DELETE FROM notification_activity WHERE active_until<now()`); err != nil {
		return NotificationJob{}, err
	}
	var j NotificationJob
	// A two minute lease permits another worker to recover after a crash; one batch has a shorter deadline.
	err := r.s.pool.QueryRow(ctx, `UPDATE notification_jobs SET next_at=now()+interval '2 minutes',attempts=attempts+1
 WHERE message_id=(SELECT message_id FROM notification_jobs WHERE next_at<=now() ORDER BY next_at LIMIT 1 FOR UPDATE SKIP LOCKED)
 RETURNING chat_id,message_id,recipients,created_at,attempts`).Scan(&j.ChatID, &j.MessageID, &j.Recipients, &j.CreatedAt, &j.Attempts)
	return j, mapError(err)
}
func (r notificationRepo) Finish(ctx context.Context, mid int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `DELETE FROM notification_jobs WHERE message_id=$1`, mid)
	return err
}
func (r notificationRepo) Devices(ctx context.Context, uid, cid, mid int64) ([]NotificationDevice, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `SELECT d.id,d.platform,d.destination,d.p256dh,d.auth,d.session_id FROM notification_devices d
 JOIN user_sessions s ON s.id=d.session_id
 JOIN participants p ON p.user_id=d.user_id AND p.chat_id=$2
 LEFT JOIN notification_preferences n ON n.user_id=d.user_id
 WHERE d.user_id=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND COALESCE(n.enabled,true)
 AND NOT EXISTS(SELECT 1 FROM notification_activity a WHERE a.device_id=d.id AND a.session_id=d.session_id AND a.active_until>now())
 AND (p.last_read_message_id IS NULL OR p.last_read_message_id<$3)
 AND NOT EXISTS(SELECT 1 FROM notification_mutes m WHERE m.user_id=$1 AND m.chat_id=$2 AND m.until_at>now())
 AND NOT EXISTS(SELECT 1 FROM notification_deliveries x WHERE x.message_id=$3 AND x.device_id=d.id)
 `, uid, cid, mid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationDevice{}
	for rows.Next() {
		var d NotificationDevice
		if err := rows.Scan(&d.ID, &d.Platform, &d.Destination, &d.P256dh, &d.Auth, &d.SessionID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (r notificationRepo) Delivered(ctx context.Context, mid int64, id uuid.UUID) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `INSERT INTO notification_deliveries(message_id,device_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, mid, id)
	return err
}
func (r notificationRepo) RemoveDevice(ctx context.Context, id uuid.UUID) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `DELETE FROM notification_devices WHERE id=$1`, id)
	return err
}
func (r notificationRepo) Activity(ctx context.Context, uid int64, sid, id, clientID uuid.UUID, active bool) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	if !active {
		_, err := r.s.pool.Exec(ctx, `DELETE FROM notification_activity a USING notification_devices d
	 WHERE a.device_id=d.id AND d.id=$1 AND d.user_id=$2 AND d.session_id=$3 AND a.client_id=$4`, id, uid, sid, clientID)
		return err
	}
	_, err := r.s.pool.Exec(ctx, `INSERT INTO notification_activity(device_id,session_id,client_id,active_until)
	 SELECT id,session_id,$4,now()+interval '25 seconds' FROM notification_devices WHERE id=$1 AND user_id=$2 AND session_id=$3
	 ON CONFLICT(device_id,client_id) DO UPDATE SET session_id=$3,active_until=now()+interval '25 seconds'`, id, uid, sid, clientID)
	return err
}

func (r notificationRepo) DeviceActive(ctx context.Context, uid int64, id, session uuid.UUID) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var ok bool
	err := r.s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_devices d JOIN user_sessions s ON s.id=d.session_id
 WHERE d.id=$1 AND d.user_id=$2 AND d.session_id=$3 AND s.revoked_at IS NULL AND s.expires_at>now())`, id, uid, session).Scan(&ok)
	return ok, err
}
