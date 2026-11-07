package calls

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/myronsi/messenger-back/internal/notifications"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	actionRing      = "ring"
	actionCancel    = "cancel"
	cancelTTL       = 30 * time.Second
	pushConcurrency = 8
)

func (s *Service) push(ctx context.Context, c postgres.Call, users []int64, action string, reasons map[int64]string) {
	if s.d.Push == nil || len(users) == 0 {
		return
	}
	devices, err := s.d.Store.Calls().PushDevices(ctx, users)
	if err != nil {
		s.d.Log.WarnContext(ctx, "call push destinations", "call_id", c.ID, "error", err)
		return
	}
	ttl := c.RingExpiresAt.Sub(s.now())
	if action == actionRing && ttl < time.Second {
		return
	}
	if action == actionCancel {
		ttl = max(ttl, cancelTTL)
	}
	title := ""
	if action == actionRing && slices.ContainsFunc(devices, func(d postgres.CallPushDevice) bool { return d.Enabled && d.Previews }) {
		title = s.pushTitle(ctx, c)
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	slots := make(chan struct{}, pushConcurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	for _, d := range devices {
		if !d.Enabled {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Go(func() {
			defer func() { <-slots }()
			if !s.pushOne(ctx, c, d, action, title, ttl, reasons[d.UserID]) {
				stop()
			}
		})
	}
}

func (s *Service) pushOne(ctx context.Context, c postgres.Call, d postgres.CallPushDevice, action, title string, ttl time.Duration, reason string) bool {
	p := notifications.CallPayload{
		Type: "call", Action: action, CallID: strconv.FormatInt(c.ID, 10), CallChatID: strconv.FormatInt(c.ChatID, 10),
		UserID: strconv.FormatInt(d.UserID, 10), Kind: string(c.Kind), Title: "Messenger", ExpiresAt: c.RingExpiresAt.Unix(),
	}
	if action == actionCancel {
		p.Reason = cmp.Or(reason, stopEnded)
	}
	if action == actionRing && d.Previews {
		p.Preview, p.Title, p.SenderID = true, title, strconv.FormatInt(c.InitiatorID, 10)
		if s.d.Tokens != nil {
			p.AvatarToken = s.d.Tokens.Avatar(d.UserID, d.ID, d.SessionID, c.InitiatorID)
		}
	}
	err := s.d.Push.SendCall(ctx, d.NotificationDevice, p, ttl)
	switch {
	case errors.Is(err, notifications.ErrUnregistered):
		if err := s.d.Store.Notifications().RemoveDevice(ctx, d.ID); err != nil {
			s.d.Log.WarnContext(ctx, "remove expired push destination", "error", err)
		}
	case errors.Is(err, notifications.ErrUnavailable):
		return false
	case err != nil && ctx.Err() == nil:
		s.d.Log.WarnContext(ctx, "call push failed", "call_id", c.ID, "action", action, "error", err)
	}
	return true
}

func (s *Service) pushTitle(ctx context.Context, c postgres.Call) string {
	if c.Kind == postgres.ChatDirect {
		if u, err := s.d.Store.Users().Get(ctx, c.InitiatorID); err == nil && u.DisplayName != "" {
			return u.DisplayName
		}
		return "Messenger"
	}
	if chat, err := s.d.Store.Chats().Get(ctx, c.ChatID); err == nil && chat.Name != nil && *chat.Name != "" {
		return *chat.Name
	}
	return "Messenger"
}
