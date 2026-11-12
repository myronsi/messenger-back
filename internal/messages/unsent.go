package messages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

// Send stores a message and delivers it to the members of the chat. A send rejected for a reason that may pass
// later is kept for the sender's devices; a successful send with the same client_temp_id removes it again.
func (s *Service) Send(ctx context.Context, r SendRequest) (Sent, error) {
	sent, err := s.send(ctx, r)
	if err != nil {
		s.KeepUnsent(ctx, r, err)
		return sent, err
	}
	if _, derr := s.d.Store.Unsent().Delete(context.WithoutCancel(ctx), r.SenderID, r.ChatID, r.ClientTempID); derr != nil {
		s.d.Log.WarnContext(ctx, "forget unsent message", "error", derr, "chat_id", r.ChatID)
	}
	return sent, nil
}

// UnsentCode is the contract's error code of a send that is kept for a later retry, or "" when the error is final
// (an invalid request, a chat that is gone).
func UnsentCode(err error) string {
	switch {
	case errors.Is(err, ErrBlocked):
		return "blocked_by_user"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, ErrUnavailable):
		return "internal_error"
	}
	return ""
}

// KeepUnsent stores a rejected send of a chat member when the cause may pass later. Failures are only logged.
func (s *Service) KeepUnsent(ctx context.Context, r SendRequest, cause error) {
	code := UnsentCode(cause)
	if code == "" || s.validateSend(r) != nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if errors.Is(cause, ErrRateLimited) || errors.Is(cause, ErrUnavailable) {
		if err := s.requireMember(ctx, r.ChatID, r.SenderID); err != nil {
			return
		}
	}
	ids := r.AlbumIDs
	if r.AttachmentID != nil {
		ids = []uuid.UUID{*r.AttachmentID}
	}
	ids = s.ownUploads(ctx, r.SenderID, ids)
	var drawings []byte
	if len(r.Drawings) > 0 {
		byID := make(map[string]json.RawMessage, len(r.Drawings))
		for id, raw := range r.Drawings {
			byID[id.String()] = raw
		}
		drawings, _ = json.Marshal(byID)
	}
	err := s.d.Store.Unsent().Save(ctx, postgres.UnsentMessage{
		UserID: r.SenderID, ChatID: r.ChatID, ClientTempID: r.ClientTempID, Type: r.Type, Content: r.Content,
		ReplyTo: r.ReplyTo, AttachmentIDs: ids, Drawings: drawings, StickerID: r.StickerID, ErrorCode: code,
	})
	if err != nil {
		s.d.Log.WarnContext(ctx, "keep unsent message", "error", err, "chat_id", r.ChatID)
	}
}

// ownUploads keeps the attachments the user uploaded, so a kept send cannot pin or reveal someone else's file.
func (s *Service) ownUploads(ctx context.Context, userID int64, ids []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		a, err := s.d.Store.Attachments().Get(ctx, id)
		if err == nil && a.UploaderID != nil && *a.UploaderID == userID {
			out = append(out, id)
		}
	}
	return out
}

// ListUnsent returns the user's kept sends of a chat, oldest first.
func (s *Service) ListUnsent(ctx context.Context, userID, chatID int64) ([]postgres.UnsentMessage, error) {
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return nil, err
	}
	out, err := s.d.Store.Unsent().List(ctx, userID, chatID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return out, nil
}

// DeleteUnsent discards a kept send.
func (s *Service) DeleteUnsent(ctx context.Context, userID, chatID int64, clientTempID string) error {
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return err
	}
	deleted, err := s.d.Store.Unsent().Delete(ctx, userID, chatID, clientTempID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !deleted {
		return ErrNotFound
	}
	return nil
}
