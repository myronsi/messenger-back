package calls

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	maxWebhookBody = 1 << 20
	webhookMemory  = 24 * time.Hour
)

type webhookEvent struct {
	ID    string `json:"id"`
	Event string `json:"event"`
	Room  *struct {
		Name string `json:"name"`
	} `json:"room"`
	Participant *struct {
		Identity string `json:"identity"`
	} `json:"participant"`
}

func eventLabel(e string) string {
	switch e {
	case "participant_joined", "participant_left", "participant_connection_aborted", "room_finished", "room_started",
		"track_published", "track_unpublished", "egress_started", "egress_updated", "egress_ended", "ingress_started", "ingress_ended":
		return e
	}
	return "other"
}

func (s *Service) WebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
		if err != nil || len(body) > maxWebhookBody {
			s.d.Metrics.CallWebhook("other", "rejected")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		if s.d.Grants == nil || s.d.Grants.VerifyWebhook(r.Header.Get("Authorization"), body) != nil {
			s.d.Metrics.CallWebhook("other", "rejected")
			s.d.Log.WarnContext(r.Context(), "media webhook with an invalid signature")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var ev webhookEvent
		if err := json.Unmarshal(body, &ev); err != nil || ev.Event == "" {
			s.d.Metrics.CallWebhook("other", "rejected")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		label := eventLabel(ev.Event)
		outcome, status := s.webhook(r.Context(), ev)
		s.d.Metrics.CallWebhook(label, outcome)
		w.WriteHeader(status)
	})
}

func (s *Service) webhook(ctx context.Context, ev webhookEvent) (string, int) {
	if ev.ID != "" && s.d.Webhooks != nil {
		fresh, err := s.d.Webhooks.Claim(ctx, ev.ID, webhookMemory)
		if err != nil {
			s.d.Log.WarnContext(ctx, "media webhook dedup", "error", err)
			return "failed", http.StatusServiceUnavailable
		}
		if !fresh {
			return "duplicate", http.StatusOK
		}
	}
	handled, err := s.handleEvent(ctx, ev)
	if err != nil {
		s.d.Log.WarnContext(ctx, "media webhook", "event", eventLabel(ev.Event), "error", err)
		if ev.ID != "" && s.d.Webhooks != nil {
			if err := s.d.Webhooks.Release(context.WithoutCancel(ctx), ev.ID); err != nil {
				s.d.Log.WarnContext(ctx, "media webhook dedup release", "error", err)
			}
		}
		return "failed", http.StatusServiceUnavailable
	}
	if !handled {
		return "ignored", http.StatusOK
	}
	return "handled", http.StatusOK
}

func (s *Service) handleEvent(ctx context.Context, ev webhookEvent) (bool, error) {
	if ev.Room == nil {
		return false, nil
	}
	callID, ok := roomCall(ev.Room.Name)
	if !ok {
		return false, nil
	}
	var fn func(c *postgres.Call, now time.Time, fx *effects) error
	switch ev.Event {
	case "room_finished":
		fn = func(c *postgres.Call, now time.Time, fx *effects) error {
			roomFinished(c, now, fx)
			return nil
		}
	case "participant_joined", "participant_left", "participant_connection_aborted":
		if ev.Participant == nil {
			return false, nil
		}
		identity := ev.Participant.Identity
		uid, session, ok := parseIdentity(identity)
		joined := ev.Event == "participant_joined"
		if !ok && !joined {
			return false, nil
		}
		fn = func(c *postgres.Call, now time.Time, fx *effects) error {
			if !ok {
				fx.remove = append(fx.remove, identity)
				return nil
			}
			if joined {
				mediaJoined(c, uid, session, fx)
			} else {
				mediaLeft(c, uid, session, now, fx)
			}
			return nil
		}
	default:
		return false, nil
	}
	_, err := s.apply(ctx, callID, fn)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
