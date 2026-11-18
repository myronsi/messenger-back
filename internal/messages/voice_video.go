package messages

import (
	"context"
	"errors"
	"fmt"

	"github.com/gocql/gocql"
	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

type FieldForbiddenError struct {
	Field   string
	Message string
}

func (e *FieldForbiddenError) Error() string { return ErrForbidden.Error() + ": " + e.Message }

func (e *FieldForbiddenError) Unwrap() error { return ErrForbidden }

var ErrVoiceVideoNotAllowed error = &FieldForbiddenError{
	Field:   postgres.SettingVoiceVideo,
	Message: "the recipient does not accept voice and video messages from you",
}

func (s *Service) voiceOrVideo(ctx context.Context, typ string, attachmentIDs []gocql.UUID) (bool, error) {
	switch typ {
	case scylla.TypeVoice:
		return true, nil
	case scylla.TypeFile:
	default:
		return false, nil
	}
	if len(attachmentIDs) == 0 {
		return false, nil
	}
	ids := make([]uuid.UUID, 0, len(attachmentIDs))
	for _, id := range attachmentIDs {
		ids = append(ids, uuid.UUID(id))
	}
	found, err := s.d.Store.Attachments().GetMany(ctx, ids)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	for _, a := range found {
		if a.Kind == "video" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) checkVoiceVideo(ctx context.Context, sender int64, chat postgres.Chat, members []int64, typ string, attachmentIDs []gocql.UUID) error {
	if chat.Type != postgres.ChatDirect {
		return nil
	}
	recipients := without(members, sender)
	if len(recipients) == 0 {
		return nil
	}
	media, err := s.voiceOrVideo(ctx, typ, attachmentIDs)
	if err != nil || !media {
		return err
	}
	for _, recipient := range recipients {
		allowed, err := users.AllowedViewers(ctx, s.d.Store.Social(), postgres.SettingVoiceVideo, recipient, []int64{sender})
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if !allowed[sender] {
			return ErrVoiceVideoNotAllowed
		}
	}
	return nil
}

func (s *Service) chatAndMembers(ctx context.Context, chatID int64) (postgres.Chat, []int64, error) {
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.Chat{}, nil, ErrNotFound
	}
	if err != nil {
		return postgres.Chat{}, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	members, err := s.Members(ctx, chatID)
	if err != nil {
		return postgres.Chat{}, nil, err
	}
	return chat, members, nil
}
