package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

type attachmentRepo struct{ s *Store }

var _ AttachmentRepository = attachmentRepo{}

func attachmentFrom(a sqlcdb.Attachment) Attachment {
	return Attachment{
		ID:           a.ID,
		UploaderID:   a.UploaderID,
		ChatID:       a.ChatID,
		Purpose:      a.Purpose,
		Kind:         a.Kind,
		Filename:     a.Filename,
		StorageKey:   a.StorageKey,
		ThumbnailKey: a.ThumbnailKey,
		MimeType:     a.MimeType,
		Size:         a.Size,
		Width:        a.Width,
		Height:       a.Height,
		Duration:     a.Duration,
		Waveform:     a.Waveform,
		CreatedAt:    a.CreatedAt,
	}
}

func keysOf(file string, thumb *string) []string {
	keys := []string{file}
	if thumb != nil {
		keys = append(keys, *thumb)
	}
	return keys
}

func (r attachmentRepo) Create(ctx context.Context, a NewAttachment) (Attachment, error) {
	if a.Purpose == "" {
		a.Purpose = PurposeMessage
	}
	if a.Kind == "" {
		a.Kind = "file"
	}
	if a.Filename == "" {
		a.Filename = "file"
	}
	var out Attachment
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		// User before chat, like DeleteAccount: the insert would otherwise take the user lock while holding the chat.
		if a.UploaderID != nil {
			if _, err := q.LockUserShared(ctx, *a.UploaderID); err != nil {
				return err
			}
		}
		row, err := q.CreateAttachment(ctx, sqlcdb.CreateAttachmentParams{
			UploaderID: a.UploaderID, ChatID: a.ChatID, StorageKey: a.StorageKey, ThumbnailKey: a.ThumbnailKey,
			MimeType: a.MimeType, Size: a.Size, Width: a.Width, Height: a.Height, Duration: a.Duration,
			Waveform: a.Waveform, Purpose: a.Purpose, Kind: a.Kind, Filename: a.Filename,
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
	rows, err := r.s.q.ListChatAttachments(ctx, sqlcdb.ListChatAttachmentsParams{ChatID: &chatID, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]Attachment, len(rows))
	for i, a := range rows {
		out[i] = attachmentFrom(a)
	}
	return out, nil
}

func (r attachmentRepo) Delete(ctx context.Context, id uuid.UUID) ([]string, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.DeleteAttachment(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	return keysOf(row.StorageKey, row.ThumbnailKey), nil
}

func (r attachmentRepo) Link(ctx context.Context, attachmentID uuid.UUID, chatID, messageID int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.LinkAttachment(ctx, sqlcdb.LinkAttachmentParams{AttachmentID: attachmentID, ChatID: chatID, MessageID: messageID}))
}

func (r attachmentRepo) LinksForViewer(ctx context.Context, attachmentID uuid.UUID, viewerID int64) ([]AttachmentLink, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.AttachmentLinksForViewer(ctx, sqlcdb.AttachmentLinksForViewerParams{AttachmentID: attachmentID, ViewerID: viewerID})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]AttachmentLink, len(rows))
	for i, l := range rows {
		out[i] = AttachmentLink{ChatID: l.ChatID, MessageID: l.MessageID}
	}
	return out, nil
}

func (r attachmentRepo) ListLinked(ctx context.Context, chatID int64, kinds []string, before time.Time, limit int) ([]LinkedAttachment, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var b *time.Time
	if !before.IsZero() {
		b = &before
	}
	rows, err := r.s.q.ListLinkedAttachments(ctx, sqlcdb.ListLinkedAttachmentsParams{ChatID: chatID, Kinds: kinds, Before: b, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]LinkedAttachment, len(rows))
	for i, row := range rows {
		out[i] = LinkedAttachment{
			Attachment: attachmentFrom(sqlcdb.Attachment{
				ID: row.ID, UploaderID: row.UploaderID, ChatID: row.ChatID, StorageKey: row.StorageKey, MimeType: row.MimeType,
				Size: row.Size, Width: row.Width, Height: row.Height, Duration: row.Duration, Waveform: row.Waveform,
				CreatedAt: row.CreatedAt, Purpose: row.Purpose, Kind: row.Kind, Filename: row.Filename, ThumbnailKey: row.ThumbnailKey,
			}),
			MessageID: row.MessageID,
			LinkedAt:  row.LinkedAt,
		}
	}
	return out, nil
}

func (r attachmentRepo) Unreferenced(ctx context.Context, olderThan time.Time, limit int) ([]UnreferencedAttachment, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListUnreferencedAttachments(ctx, sqlcdb.ListUnreferencedAttachmentsParams{OlderThan: olderThan, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]UnreferencedAttachment, len(rows))
	for i, row := range rows {
		out[i] = UnreferencedAttachment{ID: row.ID, StorageKey: row.StorageKey, ThumbnailKey: row.ThumbnailKey}
	}
	return out, nil
}

func (r attachmentRepo) DeleteIfUnreferenced(ctx context.Context, id uuid.UUID) ([]string, bool, error) {
	var keys []string
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		keys = nil // the transaction can be retried
		// A single DELETE ... WHERE NOT EXISTS would wait for a linking transaction and then delete anyway: its
		// snapshot predates the link. The lock first, then the check in a statement of its own.
		if _, err := q.LockAttachment(ctx, id); err != nil {
			return err
		}
		row, err := q.DeleteAttachmentIfUnreferenced(ctx, id)
		if err != nil {
			return err
		}
		keys = keysOf(row.StorageKey, row.ThumbnailKey)
		return nil
	})
	if err != nil {
		if err = mapError(err); errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return keys, true, nil
}

func (r attachmentRepo) SetUserAvatar(ctx context.Context, userID int64, attachmentID *uuid.UUID) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		n, err := q.SetUserAvatar(ctx, sqlcdb.SetUserAvatarParams{UserID: userID, AttachmentID: attachmentID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if err := q.ClearCurrentAvatar(ctx, userID); err != nil {
			return err
		}
		if attachmentID == nil {
			return nil
		}
		return q.AddAvatarHistory(ctx, sqlcdb.AddAvatarHistoryParams{UserID: userID, AttachmentID: attachmentID})
	})
}

func (r attachmentRepo) AvatarHistory(ctx context.Context, userID, beforeID int64, limit int) ([]AvatarVersion, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var before *int64
	if beforeID > 0 {
		before = &beforeID
	}
	rows, err := r.s.q.ListAvatarHistory(ctx, sqlcdb.ListAvatarHistoryParams{UserID: userID, BeforeID: before, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]AvatarVersion, 0, len(rows))
	for _, row := range rows {
		if row.AttachmentID == nil {
			continue
		}
		out = append(out, AvatarVersion{ID: row.ID, AttachmentID: *row.AttachmentID, IsCurrent: row.IsCurrent, CreatedAt: row.CreatedAt})
	}
	return out, nil
}

func (r attachmentRepo) SetChatAvatar(ctx context.Context, chatID int64, attachmentID *uuid.UUID) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.SetChatAvatar(ctx, sqlcdb.SetChatAvatarParams{ChatID: chatID, AttachmentID: attachmentID})
	return affected(n, err)
}

func (r attachmentRepo) ChatsWithAvatar(ctx context.Context, attachmentID uuid.UUID) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.ChatsWithAvatar(ctx, &attachmentID)
	return ids, mapError(err)
}
