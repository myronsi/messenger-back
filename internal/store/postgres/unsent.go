package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

// MaxUnsentPerUser bounds the rejected sends kept for one user; older ones are dropped first.
const MaxUnsentPerUser = 500

// UnsentMessage is a send the server rejected for a reason that may pass later.
type UnsentMessage struct {
	UserID        int64
	ChatID        int64
	ClientTempID  string
	Type          string
	Content       *string
	ReplyTo       *int64
	AttachmentIDs []uuid.UUID
	// Drawings is a JSON object from attachment id to its drawing.
	Drawings  []byte
	StickerID *int64
	ErrorCode string
	CreatedAt time.Time
}

// UnsentRepository keeps rejected sends for the sender's devices.
type UnsentRepository interface {
	// Save stores the send, or updates its error when the client_temp_id is already kept.
	Save(ctx context.Context, m UnsentMessage) error
	List(ctx context.Context, userID, chatID int64) ([]UnsentMessage, error)
	// Delete reports whether a row was removed.
	Delete(ctx context.Context, userID, chatID int64, clientTempID string) (bool, error)
}

type unsentRepo struct{ s *Store }

var _ UnsentRepository = unsentRepo{}

func (s *Store) Unsent() UnsentRepository { return unsentRepo{s} }

func (r unsentRepo) Save(ctx context.Context, m UnsentMessage) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids := m.AttachmentIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	if err := r.s.q.UpsertUnsentMessage(ctx, sqlcdb.UpsertUnsentMessageParams{
		UserID: m.UserID, ChatID: m.ChatID, ClientTempID: m.ClientTempID, Type: m.Type, Content: m.Content, ReplyTo: m.ReplyTo,
		AttachmentIds: ids, Drawings: m.Drawings, StickerID: m.StickerID, ErrorCode: m.ErrorCode,
	}); err != nil {
		return mapError(err)
	}
	return mapError(r.s.q.TrimUnsentMessages(ctx, sqlcdb.TrimUnsentMessagesParams{UserID: m.UserID, Keep: MaxUnsentPerUser}))
}

func (r unsentRepo) List(ctx context.Context, userID, chatID int64) ([]UnsentMessage, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListUnsentMessages(ctx, sqlcdb.ListUnsentMessagesParams{UserID: userID, ChatID: chatID})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]UnsentMessage, 0, len(rows))
	for _, row := range rows {
		out = append(out, UnsentMessage{
			UserID: row.UserID, ChatID: row.ChatID, ClientTempID: row.ClientTempID, Type: row.Type, Content: row.Content,
			ReplyTo: row.ReplyTo, AttachmentIDs: row.AttachmentIds, Drawings: row.Drawings, StickerID: row.StickerID,
			ErrorCode: row.ErrorCode, CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

func (r unsentRepo) Delete(ctx context.Context, userID, chatID int64, clientTempID string) (bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.DeleteUnsentMessage(ctx, sqlcdb.DeleteUnsentMessageParams{UserID: userID, ChatID: chatID, ClientTempID: clientTempID})
	return n > 0, mapError(err)
}
