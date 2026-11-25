package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SyncKind string

const (
	SyncMessageEdited   SyncKind = "message_edited"
	SyncMessageDeleted  SyncKind = "message_deleted"
	SyncMessageHidden   SyncKind = "message_hidden"
	SyncReactions       SyncKind = "reactions"
	SyncPins            SyncKind = "pins"
	SyncHistoryCleared  SyncKind = "history_cleared"
	SyncChatHidden      SyncKind = "chat_hidden"
	SyncHistoryImported SyncKind = "history_imported"
)

const SyncChangeRetention = 30 * 24 * time.Hour

type SyncChange struct {
	Seq       int64
	ChatID    int64
	UserID    int64
	Kind      SyncKind
	MessageID int64
	CreatedAt time.Time
}

type SyncRepository interface {
	Record(ctx context.Context, c SyncChange) error
	Since(ctx context.Context, userID, after int64, settle time.Duration, limit int) ([]SyncChange, error)
	Head(ctx context.Context, settle time.Duration) (int64, error)
	Oldest(ctx context.Context) (int64, error)
	Purge(ctx context.Context, retention time.Duration) (int64, error)
	StorageKey(ctx context.Context, sessionID uuid.UUID) (string, error)
	SaveStorageKey(ctx context.Context, sessionID uuid.UUID, sealed string) (string, error)
}

type syncRepo struct{ s *Store }

var _ SyncRepository = syncRepo{}

func (s *Store) Sync() SyncRepository { return syncRepo{s} }

func nullable(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

func (r syncRepo) Record(ctx context.Context, c SyncChange) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.pool.Exec(ctx, `INSERT INTO sync_changes (chat_id, user_id, kind, message_id) VALUES ($1, $2, $3, $4)`,
		c.ChatID, nullable(c.UserID), string(c.Kind), nullable(c.MessageID))
	return mapError(err)
}

func (r syncRepo) Since(ctx context.Context, userID, after int64, settle time.Duration, limit int) ([]SyncChange, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.pool.Query(ctx, `
		SELECT c.seq, c.chat_id, coalesce(c.user_id, 0), c.kind, coalesce(c.message_id, 0), c.created_at
		FROM sync_changes c
		WHERE c.seq > $2
		  AND c.created_at < clock_timestamp() - $3::interval
		  AND (c.user_id = $1 OR (c.user_id IS NULL AND EXISTS (
		      SELECT 1 FROM participants p WHERE p.chat_id = c.chat_id AND p.user_id = $1)))
		ORDER BY c.seq
		LIMIT $4`, userID, after, settle, limit)
	if err != nil {
		return nil, mapError(err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (SyncChange, error) {
		var c SyncChange
		var kind string
		err := row.Scan(&c.Seq, &c.ChatID, &c.UserID, &kind, &c.MessageID, &c.CreatedAt)
		c.Kind = SyncKind(kind)
		return c, err
	})
}

const nextSyncSeq = `(SELECT CASE WHEN is_called THEN last_value + 1 ELSE last_value END FROM sync_changes_seq_seq)`

func (r syncRepo) Head(ctx context.Context, settle time.Duration) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var head int64
	err := r.s.pool.QueryRow(ctx, `
		SELECT coalesce(
		    (SELECT max(seq) FROM sync_changes WHERE created_at < clock_timestamp() - $1::interval),
		    coalesce((SELECT min(seq) FROM sync_changes), `+nextSyncSeq+`) - 1)`, settle).Scan(&head)
	return head, mapError(err)
}

func (r syncRepo) Oldest(ctx context.Context) (int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var oldest int64
	err := r.s.pool.QueryRow(ctx, `SELECT coalesce((SELECT min(seq) FROM sync_changes), `+nextSyncSeq+`)`).Scan(&oldest)
	return oldest, mapError(err)
}

func (r syncRepo) Purge(ctx context.Context, retention time.Duration) (int64, error) {
	keyCtx, cancelKeys := r.s.call(ctx)
	_, err := r.s.pool.Exec(keyCtx, `
		DELETE FROM session_storage_keys k USING user_sessions s
		WHERE s.id = k.session_id AND (s.revoked_at IS NOT NULL OR s.expires_at <= now())`)
	cancelKeys()
	if err != nil {
		return 0, mapError(err)
	}
	cutoff := time.Now().Add(-retention)
	var total int64
	for {
		callCtx, cancel := r.s.call(ctx)
		tag, err := r.s.pool.Exec(callCtx, `DELETE FROM sync_changes WHERE seq IN (SELECT seq FROM sync_changes WHERE created_at < $1 ORDER BY seq LIMIT $2)`, cutoff, purgeBatch)
		cancel()
		if err != nil {
			return total, mapError(err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < purgeBatch {
			return total, nil
		}
	}
}

func (r syncRepo) StorageKey(ctx context.Context, sessionID uuid.UUID) (string, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var sealed string
	err := r.s.pool.QueryRow(ctx, `
		SELECT k.sealed_key FROM session_storage_keys k
		JOIN user_sessions s ON s.id = k.session_id
		WHERE k.session_id = $1 AND s.revoked_at IS NULL AND s.expires_at > now()`, sessionID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return sealed, mapError(err)
}

func (r syncRepo) SaveStorageKey(ctx context.Context, sessionID uuid.UUID, sealed string) (string, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var stored string
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO session_storage_keys (session_id, sealed_key)
		SELECT id, $2 FROM user_sessions WHERE id = $1 AND revoked_at IS NULL AND expires_at > now()
		ON CONFLICT (session_id) DO UPDATE SET session_id = EXCLUDED.session_id
		RETURNING sealed_key`, sessionID, sealed).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return stored, mapError(err)
}
