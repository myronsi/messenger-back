package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type Delivery interface {
	Send(context.Context, postgres.NotificationDevice, Payload) error
}
type Worker struct {
	Queue    postgres.NotificationRepository
	Chats    postgres.ChatRepository
	Social   postgres.SocialRepository
	Users    postgres.UserRepository
	Messages scylla.MessageRepository
	Sender   Delivery
	Tokens   *ActionTokens
	Log      *slog.Logger
	OnResult func(string)
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for range 20 {
				j, err := w.Queue.Claim(ctx)
				if errors.Is(err, postgres.ErrNotFound) {
					break
				}
				if err != nil {
					w.result("retry")
					w.Log.ErrorContext(ctx, "claim notification job")
					break
				}
				batch, cancel := context.WithTimeout(ctx, 75*time.Second)
				err = w.Handle(batch, j)
				cancel()
				if err != nil {
					w.Log.WarnContext(ctx, "notification delivery deferred", "message_id", j.MessageID, "attempt", j.Attempts, "error", err)
					w.result("retry")
				}
			}
		}
	}
}
func (w *Worker) Handle(ctx context.Context, j postgres.NotificationJob) error {
	if time.Since(j.CreatedAt) > 15*time.Minute || j.Attempts > 8 {
		w.result("expired")
		return w.Queue.Finish(ctx, j.MessageID)
	}
	m, err := w.Messages.Get(ctx, j.ChatID, j.MessageID)
	// The intent may precede a slow Scylla write; leave it for the next attempt.
	if err != nil {
		return errors.New("notification message not available")
	}
	if m.Deleted || m.SenderID == nil || m.Type == scylla.TypeSystem {
		return w.Queue.Finish(ctx, j.MessageID)
	}
	var pending error
	for _, uid := range j.Recipients {
		if uid == *m.SenderID {
			continue
		}
		part, err := w.Chats.Participant(ctx, j.ChatID, uid)
		if errors.Is(err, postgres.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if part.JoinedAt.After(m.CreatedAt) || part.LastReadMessageID != nil && *part.LastReadMessageID >= m.ID {
			continue
		}
		blocked, err := w.Social.BlockedEither(ctx, uid, *m.SenderID)
		if err != nil {
			return err
		}
		if blocked {
			continue
		}
		hidden, err := w.Messages.Hidden(ctx, uid, j.ChatID, j.MessageID)
		if err != nil {
			return err
		}
		if hidden {
			continue
		}
		prefs, err := w.Queue.Preferences(ctx, uid)
		if err != nil {
			return err
		}
		if !prefs.Enabled {
			continue
		}
		devices, err := w.Queue.Devices(ctx, uid, j.ChatID, j.MessageID)
		if err != nil {
			return err
		}
		p := Payload{Title: "Messenger", Body: "New message", ChatID: fmt.Sprint(j.ChatID), MessageID: fmt.Sprint(j.MessageID), UserID: fmt.Sprint(uid), Sound: prefs.Sound, ExpiresAt: m.CreatedAt.Add(15 * time.Minute).Unix()}
		if w.Tokens != nil {
			p.ReadToken = w.Tokens.Read(uid, j.ChatID, j.MessageID)
		}
		if prefs.Previews {
			if w.Tokens != nil {
				p.AvatarToken = w.Tokens.Avatar(uid, *m.SenderID)
			}
			sender, err := w.Users.Get(ctx, *m.SenderID)
			if err != nil {
				return err
			}
			p.Title = sender.DisplayName
			if m.Type == scylla.TypeText && m.Content != nil {
				r := []rune(*m.Content)
				if len(r) > 160 {
					r = r[:160]
				}
				p.Body = string(r)
			}
		}
		for _, d := range devices {
			err := w.Sender.Send(ctx, d, p)
			if errors.Is(err, ErrUnregistered) {
				w.result("unregistered")
				err = w.Queue.RemoveDevice(ctx, d.ID)
			} else if err == nil {
				w.result("delivered")
				err = w.Queue.Delivered(ctx, j.MessageID, d.ID)
			}
			if err != nil {
				pending = err
			}
		}
	}
	if pending != nil {
		return pending
	}
	return w.Queue.Finish(ctx, j.MessageID)
}
func (w *Worker) result(outcome string) {
	if w.OnResult != nil {
		w.OnResult(outcome)
	}
}
