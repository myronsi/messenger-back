package calls

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func (s *Service) SessionsEnded(ctx context.Context, sessions []uuid.UUID) {
	if len(sessions) == 0 {
		return
	}
	if err := s.d.Store.Calls().DeleteIdentities(ctx, sessions); err != nil {
		s.d.Log.WarnContext(ctx, "delete call identities of ended sessions", "error", err)
	}
	ids, err := s.d.Store.Calls().CallsOfSessions(ctx, sessions)
	if err != nil {
		s.d.Log.WarnContext(ctx, "calls of ended sessions", "error", err)
		return
	}
	ended := make(map[uuid.UUID]bool, len(sessions))
	for _, id := range sessions {
		ended[id] = true
	}
	for _, id := range ids {
		if _, err := s.apply(ctx, id, func(c *postgres.Call, now time.Time, fx *effects) error {
			removeSessions(c, ended, now, fx)
			return nil
		}); err != nil && !errors.Is(err, ErrNotFound) {
			s.d.Log.WarnContext(ctx, "remove ended session from call", "call_id", id, "error", err)
		}
	}
}

func (s *Service) MembersRemoved(ctx context.Context, chatID int64, userIDs []int64) {
	if len(userIDs) == 0 {
		return
	}
	c, err := s.d.Store.Calls().OpenInChat(ctx, chatID)
	if errors.Is(err, postgres.ErrNotFound) {
		return
	}
	if err != nil {
		s.d.Log.WarnContext(ctx, "call of a changed group", "chat_id", chatID, "error", err)
		return
	}
	if _, err := s.apply(ctx, c.ID, func(c *postgres.Call, now time.Time, fx *effects) error {
		for _, uid := range userIDs {
			removeUser(c, uid, now, fx)
		}
		return nil
	}); err != nil && !errors.Is(err, ErrNotFound) {
		s.d.Log.WarnContext(ctx, "remove member from call", "call_id", c.ID, "error", err)
	}
}

func (s *Service) ChatsDeleted(ctx context.Context) {
	s.background(ctx, func(ctx context.Context) { s.Sweep(ctx) })
}

func (s *Service) Blocked(ctx context.Context, blockerID, blockedID int64) {
	chat, err := s.d.Store.Chats().DirectBetween(ctx, blockerID, blockedID)
	if errors.Is(err, postgres.ErrNotFound) {
		return
	}
	if err != nil {
		s.d.Log.WarnContext(ctx, "direct chat of a block", "error", err)
		return
	}
	c, err := s.d.Store.Calls().OpenInChat(ctx, chat.ID)
	if errors.Is(err, postgres.ErrNotFound) {
		return
	}
	if err != nil {
		s.d.Log.WarnContext(ctx, "call of a blocked pair", "error", err)
		return
	}
	if _, err := s.apply(ctx, c.ID, func(c *postgres.Call, now time.Time, fx *effects) error {
		endCall(c, outcome(c), now, fx)
		return nil
	}); err != nil && !errors.Is(err, ErrNotFound) {
		s.d.Log.WarnContext(ctx, "end call of a blocked pair", "call_id", c.ID, "error", err)
	}
}

type repairPlan struct {
	blocked  bool
	removed  []int64
	sessions map[uuid.UUID]bool
}

func (s *Service) repair(ctx context.Context) {
	rows, err := s.d.Store.Calls().Repairs(ctx, 4*sweepBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.d.Log.WarnContext(ctx, "calls to repair", "error", err)
		}
		return
	}
	plans := map[int64]*repairPlan{}
	var order []int64
	for _, r := range rows {
		p := plans[r.CallID]
		if p == nil {
			p = &repairPlan{sessions: map[uuid.UUID]bool{}}
			plans[r.CallID] = p
			order = append(order, r.CallID)
		}
		switch {
		case r.Blocked:
			p.blocked = true
		case r.Removed:
			p.removed = append(p.removed, r.UserID)
		case r.SessionEnded && r.SessionID != nil:
			p.sessions[*r.SessionID] = true
		}
	}
	for _, id := range order {
		p := plans[id]
		if _, err := s.apply(ctx, id, func(c *postgres.Call, now time.Time, fx *effects) error {
			if p.blocked && c.Kind == postgres.ChatDirect {
				endCall(c, outcome(c), now, fx)
				return nil
			}
			for _, uid := range p.removed {
				removeUser(c, uid, now, fx)
			}
			removeSessions(c, p.sessions, now, fx)
			return nil
		}); err != nil && !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
			s.d.Log.WarnContext(ctx, "repair call", "call_id", id, "error", err)
		}
	}
}
