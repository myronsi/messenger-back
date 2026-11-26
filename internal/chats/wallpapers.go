package chats

import (
	"context"
	"fmt"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func (s *Service) WallpaperOwners(ctx context.Context, userID, chatID int64, forBoth bool) ([]int64, error) {
	e, err := s.d.Store.Chats().Entry(ctx, userID, chatID)
	if err != nil {
		return nil, notFoundOr(err)
	}
	if !forBoth {
		return []int64{userID}, nil
	}
	if e.Chat.Type != postgres.ChatDirect {
		return nil, fmt.Errorf("%w: a background for both is only for direct chats", ErrForbidden)
	}
	ps, err := s.d.Store.Chats().Participants(ctx, chatID)
	if err != nil {
		return nil, unavailable(err)
	}
	var other int64
	for _, p := range ps {
		if p.UserID != userID {
			other = p.UserID
		}
	}
	if other == 0 {
		return nil, fmt.Errorf("%w: Saved Messages have only you", ErrForbidden)
	}
	blocked, err := s.d.Store.Social().BlockedEither(ctx, userID, other)
	if err != nil {
		return nil, unavailable(err)
	}
	if blocked {
		return nil, ErrBlocked
	}
	return []int64{userID, other}, nil
}

func (s *Service) WallpaperSetForBoth(ctx context.Context, chatID, actor int64) {
	s.logGroup(ctx, chatID, actor, messages.SystemEvent{Kind: messages.SystemChatWallpaper})
}
