package calls

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	sweepEvery      = 2 * time.Second
	reconcileEvery  = 30 * time.Second
	sweepBatch      = 100
	roomRetryWindow = time.Hour
)

func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Go(func() { every(ctx, reconcileEvery, s.Reconcile) })
	sweep := time.NewTicker(sweepEvery)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			s.Sweep(ctx)
		}
	}
}

func (s *Service) RunDisabled(ctx context.Context) {
	every(ctx, reconcileEvery, s.CloseAll)
}

func every(ctx context.Context, period time.Duration, fn func(ctx context.Context)) {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		run, cancel := context.WithTimeout(ctx, period)
		fn(run)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) CloseAll(ctx context.Context) {
	for ctx.Err() == nil {
		ids, err := s.d.Store.Calls().OpenIDs(ctx, sweepBatch)
		if err != nil {
			if ctx.Err() == nil {
				s.d.Log.WarnContext(ctx, "open calls", "error", err)
			}
			break
		}
		for _, id := range ids {
			if _, err := s.apply(ctx, id, func(c *postgres.Call, now time.Time, fx *effects) error {
				endCall(c, reasonFailed, now, fx)
				return nil
			}); err != nil && !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
				s.d.Log.WarnContext(ctx, "end call while calls are off", "call_id", id, "error", err)
				return
			}
		}
		if len(ids) < sweepBatch {
			break
		}
	}
	s.retrySummaries(ctx)
	if _, err := s.d.Store.Calls().DeleteOrphans(ctx); err != nil && ctx.Err() == nil {
		s.d.Log.WarnContext(ctx, "delete calls of deleted chats", "error", err)
	}
}

func (s *Service) Sweep(ctx context.Context) {
	s.sweeping.Lock()
	defer s.sweeping.Unlock()
	ids, err := s.d.Store.Calls().Due(ctx, s.now(), s.cfg.EmptyGrace, staleAfter, sweepBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.d.Log.WarnContext(ctx, "due calls", "error", err)
		}
		return
	}
	for _, id := range ids {
		if _, err := s.apply(ctx, id, func(c *postgres.Call, now time.Time, fx *effects) error {
			sweep(c, now, s.cfg.EmptyGrace, fx)
			return nil
		}); err != nil && !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
			s.d.Log.WarnContext(ctx, "sweep call", "call_id", id, "error", err)
		}
	}
	s.repair(ctx)
	if _, err := s.d.Store.Calls().DeleteOrphans(ctx); err != nil && ctx.Err() == nil {
		s.d.Log.WarnContext(ctx, "delete calls of deleted chats", "error", err)
	}
}

func (s *Service) Reconcile(ctx context.Context) {
	if n, err := s.d.Store.Calls().JoinedCount(ctx); err == nil {
		s.d.Metrics.CallParticipants(n)
	}
	s.retrySummaries(ctx)
	if s.d.Media == nil {
		return
	}
	s.retryRoomDeletions(ctx)
	ids, err := s.d.Store.Calls().OpenIDs(ctx, 500)
	if err != nil {
		if ctx.Err() == nil {
			s.d.Log.WarnContext(ctx, "open calls", "error", err)
		}
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		s.reconcileRoom(ctx, id)
	}
}

func (s *Service) retrySummaries(ctx context.Context) {
	now := s.now()
	ids, err := s.d.Store.Calls().PendingSummaries(ctx, now.Add(-time.Hour), now.Add(-10*time.Second), sweepBatch)
	if err != nil {
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		s.postSummary(ctx, id)
	}
}

func (s *Service) retryRoomDeletions(ctx context.Context) {
	now := s.now()
	ids, err := s.d.Store.Calls().PendingRooms(ctx, now.Add(-roomRetryWindow), now.Add(-10*time.Second), sweepBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.d.Log.WarnContext(ctx, "media rooms of ended calls", "error", err)
		}
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil || !s.deleteRoom(ctx, id) {
			return
		}
	}
}

func (s *Service) reconcileRoom(ctx context.Context, callID int64) {
	c, err := s.d.Store.Calls().Get(ctx, callID)
	if err != nil || joinedCount(&c) == 0 {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	identities, err := s.d.Media.ListParticipants(lctx, RoomName(callID))
	cancel()
	if err != nil {
		return
	}
	present := make(map[string]bool, len(identities))
	for _, id := range identities {
		present[id] = true
	}
	if _, err := s.apply(ctx, callID, func(c *postgres.Call, now time.Time, fx *effects) error {
		reconcile(c, present, now, s.cfg.EmptyGrace, fx)
		return nil
	}); err != nil && !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
		s.d.Log.WarnContext(ctx, "reconcile call with the media room", "call_id", callID, "error", err)
	}
}

func (s *Service) postSummary(ctx context.Context, callID int64) {
	if s.d.System == nil {
		return
	}
	candidate, err := s.d.System.SystemMessageID()
	if err != nil {
		s.d.Log.WarnContext(ctx, "call system message id", "call_id", callID, "error", err)
		return
	}
	messageID, claimed, err := s.d.Store.Calls().ClaimSummary(ctx, callID, candidate)
	if err != nil || !claimed {
		return
	}
	c, err := s.d.Store.Calls().Get(ctx, callID)
	if err != nil || c.ChatID == 0 {
		return
	}
	name := ""
	if u, err := s.d.Store.Users().Get(ctx, c.InitiatorID); err == nil {
		name = u.DisplayName
		if name == "" {
			name = u.Username
		}
	}
	summary := &messages.CallSummary{CallID: c.ID, Kind: string(c.Kind), Outcome: c.EndReason}
	if c.StartedAt != nil && c.EndedAt != nil && c.EndReason != reasonCancelled {
		d := int(max(c.EndedAt.Sub(*c.StartedAt), 0) / time.Second)
		summary.DurationSeconds = &d
	}
	initiator := c.InitiatorID
	e := messages.SystemEvent{Kind: messages.SystemCall, ActorID: &initiator, ActorName: name, Call: summary}
	if _, err := s.d.System.SendSystemAs(ctx, c.ChatID, messageID, e); err != nil {
		s.d.Log.WarnContext(ctx, "call system message", "call_id", c.ID, "error", err)
		if err := s.d.Store.Calls().ReleaseSummary(context.WithoutCancel(ctx), c.ID); err != nil {
			s.d.Log.WarnContext(ctx, "release call system message", "call_id", c.ID, "error", err)
		}
		return
	}
	if err := s.d.Store.Calls().FinishSummary(context.WithoutCancel(ctx), c.ID); err != nil {
		s.d.Log.WarnContext(ctx, "mark call system message as posted", "call_id", c.ID, "error", err)
	}
}
