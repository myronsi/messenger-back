package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type NotificationPreferences struct {
	Enabled           bool `json:"enabled"`
	Sound             bool `json:"sound"`
	Previews          bool `json:"previews"`
	BirthdayReminders bool `json:"birthday_reminders"`
}
type NotificationMute struct {
	ChatID string    `json:"chat_id"`
	Until  time.Time `json:"until"`
}
type NotificationDevice struct {
	ID          uuid.UUID  `json:"id"`
	Platform    string     `json:"platform"`
	Destination string     `json:"destination"`
	P256dh      string     `json:"p256dh,omitempty"`
	Auth        string     `json:"auth,omitempty"`
	SessionID   uuid.UUID  `json:"-"`
	UserID      int64      `json:"-"`
	ActiveUntil *time.Time `json:"-"`
}

type NotificationRecipient struct {
	UserID            int64
	JoinedAt          time.Time
	LastReadMessageID *int64
	Blocked           bool
	Preferences       NotificationPreferences
}

const DeviceFailureLimit = 3

const notificationJobsChannel = "notification_jobs"

type NotificationJob struct {
	MessageID, ChatID int64
	Recipients        []int64
	CreatedAt         time.Time
	Attempts          int
	Deferred          bool
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
	Ready(ctx context.Context, messageID int64) error
	ListenJobs(ctx context.Context, wake chan<- struct{}) error
	Claim(context.Context) (NotificationJob, error)
	Finish(context.Context, int64) error
	Defer(context.Context, int64, time.Time) error
	PruneActivity(context.Context) error
	Recipients(ctx context.Context, chatID, senderID int64, userIDs []int64) ([]NotificationRecipient, error)
	Devices(ctx context.Context, chatID, messageID int64, userIDs []int64) ([]NotificationDevice, error)
	UserDevices(context.Context, int64) ([]NotificationDevice, error)
	Delivered(context.Context, int64, uuid.UUID) error
	Rejected(ctx context.Context, messageID int64, deviceID uuid.UUID, counts bool) (bool, error)
	RemoveDevice(context.Context, uuid.UUID) error
	DeviceActive(context.Context, int64, uuid.UUID, uuid.UUID) (bool, error)
}
type notificationRepo struct{ s *Store }

func (s *Store) Notifications() NotificationRepository { return notificationRepo{s} }

func (r notificationRepo) Preferences(ctx context.Context, uid int64) (NotificationPreferences, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p := NotificationPreferences{Enabled: true, BirthdayReminders: true}
	err := r.s.pool.QueryRow(ctx, `SELECT enabled,sound,previews,birthday_reminders FROM notification_preferences WHERE user_id=$1`, uid).Scan(&p.Enabled, &p.Sound, &p.Previews, &p.BirthdayReminders)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return p, err
}
func (r notificationRepo) SetPreferences(ctx context.Context, uid int64, p NotificationPreferences) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `INSERT INTO notification_preferences(user_id,enabled,sound,previews,birthday_reminders) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(user_id) DO UPDATE SET enabled=$2,sound=$3,previews=$4,birthday_reminders=$5`, uid, p.Enabled, p.Sound, p.Previews, p.BirthdayReminders)
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
func (r notificationRepo) Ready(ctx context.Context, mid int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `WITH ready AS (
 UPDATE notification_jobs SET next_at=now() WHERE message_id=$1 AND attempts=0 AND next_at>now() RETURNING message_id)
 SELECT pg_notify('`+notificationJobsChannel+`', message_id::text) FROM ready`, mid)
	return err
}

func (r notificationRepo) ListenJobs(ctx context.Context, wake chan<- struct{}) error {
	pooled, err := r.s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	conn := pooled.Hijack()
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN "+notificationJobsChannel); err != nil {
		return err
	}
	select {
	case wake <- struct{}{}:
	default:
	}
	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return err
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (r notificationRepo) Claim(ctx context.Context) (NotificationJob, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var j NotificationJob
	// A two minute lease permits another worker to recover after a crash; one batch has a shorter deadline.
	err := r.s.pool.QueryRow(ctx, `UPDATE notification_jobs SET next_at=now()+interval '2 minutes',attempts=attempts+1
 WHERE message_id=(SELECT message_id FROM notification_jobs WHERE next_at<=now() ORDER BY next_at LIMIT 1 FOR UPDATE SKIP LOCKED)
 RETURNING chat_id,message_id,recipients,created_at,attempts,deferred`).Scan(&j.ChatID, &j.MessageID, &j.Recipients, &j.CreatedAt, &j.Attempts, &j.Deferred)
	return j, mapError(err)
}
func (r notificationRepo) Finish(ctx context.Context, mid int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `DELETE FROM notification_jobs WHERE message_id=$1`, mid)
	return err
}

func (r notificationRepo) Defer(ctx context.Context, mid int64, until time.Time) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `UPDATE notification_jobs SET next_at=$2,attempts=GREATEST(attempts-1,0),deferred=true WHERE message_id=$1`, mid, until)
	return err
}
func (r notificationRepo) PruneActivity(ctx context.Context) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `DELETE FROM notification_activity WHERE active_until<now()`)
	return err
}
func (r notificationRepo) Recipients(ctx context.Context, cid, sender int64, ids []int64) ([]NotificationRecipient, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `SELECT p.user_id,p.joined_at,p.last_read_message_id,
 COALESCE(n.enabled,true),COALESCE(n.sound,false),COALESCE(n.previews,false),COALESCE(n.birthday_reminders,true),
 EXISTS(SELECT 1 FROM user_blocks b WHERE (b.blocker_id=p.user_id AND b.blocked_id=$2) OR (b.blocker_id=$2 AND b.blocked_id=p.user_id))
 FROM participants p LEFT JOIN notification_preferences n ON n.user_id=p.user_id
 WHERE p.chat_id=$1 AND p.user_id=ANY($3) AND p.user_id<>$2
 ORDER BY p.user_id`, cid, sender, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationRecipient{}
	for rows.Next() {
		var x NotificationRecipient
		p := &x.Preferences
		if err := rows.Scan(&x.UserID, &x.JoinedAt, &x.LastReadMessageID, &p.Enabled, &p.Sound, &p.Previews, &p.BirthdayReminders, &x.Blocked); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (r notificationRepo) Devices(ctx context.Context, cid, mid int64, uids []int64) ([]NotificationDevice, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `SELECT d.id,d.platform,d.destination,d.p256dh,d.auth,d.session_id,d.user_id,
 (SELECT max(a.active_until) FROM notification_activity a WHERE a.device_id=d.id AND a.session_id=d.session_id AND a.active_until>now())
 FROM notification_devices d
 JOIN user_sessions s ON s.id=d.session_id
 JOIN participants p ON p.user_id=d.user_id AND p.chat_id=$1
 LEFT JOIN notification_preferences n ON n.user_id=d.user_id
 WHERE d.user_id=ANY($3) AND s.revoked_at IS NULL AND s.expires_at>now() AND COALESCE(n.enabled,true)
 AND (p.last_read_message_id IS NULL OR p.last_read_message_id<$2)
 AND NOT EXISTS(SELECT 1 FROM notification_mutes m WHERE m.user_id=d.user_id AND m.chat_id=$1 AND m.until_at>now())
 AND NOT EXISTS(SELECT 1 FROM notification_deliveries x WHERE x.message_id=$2 AND x.device_id=d.id)
 ORDER BY d.user_id,d.id`, cid, mid, uids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationDevice{}
	for rows.Next() {
		var d NotificationDevice
		if err := rows.Scan(&d.ID, &d.Platform, &d.Destination, &d.P256dh, &d.Auth, &d.SessionID, &d.UserID, &d.ActiveUntil); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (r notificationRepo) UserDevices(ctx context.Context, uid int64) ([]NotificationDevice, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `SELECT d.id,d.platform,d.destination,d.p256dh,d.auth,d.session_id,d.user_id FROM notification_devices d
 JOIN user_sessions s ON s.id=d.session_id
 LEFT JOIN notification_preferences n ON n.user_id=d.user_id
 WHERE d.user_id=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND COALESCE(n.enabled,true)`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationDevice{}
	for rows.Next() {
		var d NotificationDevice
		if err := rows.Scan(&d.ID, &d.Platform, &d.Destination, &d.P256dh, &d.Auth, &d.SessionID, &d.UserID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r notificationRepo) Delivered(ctx context.Context, mid int64, id uuid.UUID) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO notification_deliveries(message_id,device_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, mid, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE notification_devices SET failures=0 WHERE id=$1 AND failures>0`, id)
		return err
	})
}

func (r notificationRepo) Rejected(ctx context.Context, mid int64, id uuid.UUID, counts bool) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	removed := false
	err := r.s.inRawTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO notification_deliveries(message_id,device_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, mid, id); err != nil {
			return err
		}
		if !counts {
			return nil
		}
		var failures int
		err := tx.QueryRow(ctx, `UPDATE notification_devices SET failures=failures+1 WHERE id=$1 RETURNING failures`, id).Scan(&failures)
		if err != nil || failures < DeviceFailureLimit {
			return err
		}
		removed = true
		_, err = tx.Exec(ctx, `DELETE FROM notification_devices WHERE id=$1`, id)
		return err
	})
	return removed, err
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
