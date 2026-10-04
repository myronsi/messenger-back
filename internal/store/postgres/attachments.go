package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

type attachmentRepo struct{ s *Store }

var _ AttachmentRepository = attachmentRepo{}

func attachmentFrom(a sqlcdb.Attachment) Attachment {
	return Attachment{
		ID:         a.ID,
		UploaderID: a.UploaderID,
		ChatID:     a.ChatID,
		StorageKey: a.StorageKey,
		MimeType:   a.MimeType,
		Size:       a.Size,
		Width:      a.Width,
		Height:     a.Height,
		Duration:   a.Duration,
		Waveform:   a.Waveform,
		CreatedAt:  a.CreatedAt,
	}
}

func (r attachmentRepo) Create(ctx context.Context, a NewAttachment) (Attachment, error) {
	var out Attachment
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		// User before chat, like DeleteAccount: the insert would otherwise take the user lock while holding the chat.
		if a.UploaderID != nil {
			if _, err := q.LockUserShared(ctx, *a.UploaderID); err != nil {
				return err
			}
		}
		row, err := q.CreateAttachment(ctx, sqlcdb.CreateAttachmentParams{
			UploaderID: a.UploaderID, ChatID: a.ChatID, StorageKey: a.StorageKey, MimeType: a.MimeType,
			Size: a.Size, Width: a.Width, Height: a.Height, Duration: a.Duration, Waveform: a.Waveform,
		})
		out = attachmentFrom(row)
		return err
	})
	if err != nil {
		return Attachment{}, err
	}
	return out, nil
}

func (r attachmentRepo) Get(ctx context.Context, id uuid.UUID) (Attachment, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetAttachment(ctx, id)
	if err != nil {
		return Attachment{}, mapError(err)
	}
	return attachmentFrom(row), nil
}

func (r attachmentRepo) ListByChat(ctx context.Context, chatID int64, limit int) ([]Attachment, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListChatAttachments(ctx, sqlcdb.ListChatAttachmentsParams{ChatID: chatID, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]Attachment, len(rows))
	for i, a := range rows {
		out[i] = attachmentFrom(a)
	}
	return out, nil
}

func (r attachmentRepo) Delete(ctx context.Context, id uuid.UUID) (string, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	key, err := r.s.q.DeleteAttachment(ctx, id)
	if err != nil {
		return "", mapError(err)
	}
	return key, nil
}
