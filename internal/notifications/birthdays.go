package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/myronsi/messenger-back/internal/emoji"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/users"
)

const (
	BirthdayHour      = 9
	BirthdayKind      = "birthday"
	birthdayBatch     = 500
	birthdayMaxRounds = 20
	birthdayLifetime  = 12 * time.Hour
)

type BirthdayStore interface {
	BirthdayCandidates(ctx context.Context, now time.Time, fromHour, limit int) ([]postgres.BirthdayCandidate, error)
	MarkBirthdayReminder(ctx context.Context, userID, receiverID int64, year int) (bool, error)
}

type BirthdayPrivacy interface {
	Privacy(ctx context.Context, userIDs []int64) (map[int64]postgres.PrivacySettings, error)
	ExceptionsOf(ctx context.Context, ownerID int64, viewerIDs []int64) (map[int64]map[string]string, error)
}

type Birthdays struct {
	Store   BirthdayStore
	Privacy BirthdayPrivacy
	Devices postgres.NotificationRepository
	Sender  Delivery
	Tokens  *ActionTokens
	Log     *slog.Logger
	Now     func() time.Time
}

func (b *Birthdays) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Birthdays) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if _, err := b.RunOnce(ctx); err != nil && ctx.Err() == nil {
			b.Log.WarnContext(ctx, "birthday reminders", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Birthdays) RunOnce(ctx context.Context) (int, error) {
	sent := 0
	for range birthdayMaxRounds {
		candidates, err := b.Store.BirthdayCandidates(ctx, b.now(), BirthdayHour, birthdayBatch)
		if err != nil {
			return sent, err
		}
		allowed, err := b.visible(ctx, candidates)
		if err != nil {
			return sent, err
		}
		progress := 0
		for _, c := range candidates {
			marked, err := b.Store.MarkBirthdayReminder(ctx, c.UserID, c.ReceiverID, c.Year)
			if err != nil {
				return sent, err
			}
			if !marked {
				continue
			}
			progress++
			if !allowed[[2]int64{c.UserID, c.ReceiverID}] {
				continue
			}
			if err := b.deliver(ctx, c); err != nil {
				b.Log.WarnContext(ctx, "birthday reminder not delivered", "receiver_id", c.ReceiverID, "error", err)
				continue
			}
			sent++
		}
		if len(candidates) < birthdayBatch || progress == 0 {
			break
		}
	}
	return sent, nil
}

func (b *Birthdays) visible(ctx context.Context, candidates []postgres.BirthdayCandidate) (map[[2]int64]bool, error) {
	receivers := map[int64][]int64{}
	for _, c := range candidates {
		receivers[c.UserID] = append(receivers[c.UserID], c.ReceiverID)
	}
	if len(receivers) == 0 {
		return nil, nil
	}
	subjects := make([]int64, 0, len(receivers))
	for id := range receivers {
		subjects = append(subjects, id)
	}
	settings, err := b.Privacy.Privacy(ctx, subjects)
	if err != nil {
		return nil, err
	}
	exceptions := make(map[int64]map[int64]map[string]string, len(receivers))
	for subject, viewers := range receivers {
		exc, err := b.Privacy.ExceptionsOf(ctx, subject, viewers)
		if err != nil {
			return nil, err
		}
		exceptions[subject] = exc
	}
	out := make(map[[2]int64]bool, len(candidates))
	for _, c := range candidates {
		if users.Visible(settings[c.UserID].BirthdayVisibility, exceptions[c.UserID][c.ReceiverID][postgres.SettingBirthday], true) {
			out[[2]int64{c.UserID, c.ReceiverID}] = true
		}
	}
	return out, nil
}

func (b *Birthdays) deliver(ctx context.Context, c postgres.BirthdayCandidate) error {
	prefs, err := b.Devices.Preferences(ctx, c.ReceiverID)
	if err != nil {
		return err
	}
	if !prefs.Enabled || !prefs.BirthdayReminders {
		return nil
	}
	devices, err := b.Devices.UserDevices(ctx, c.ReceiverID)
	if err != nil {
		return err
	}
	p := Payload{
		Title: "Messenger", Body: "Someone you chat with has a birthday today", ChatID: fmt.Sprint(c.ChatID),
		UserID: fmt.Sprint(c.ReceiverID), Sound: prefs.Sound, ExpiresAt: b.now().Add(birthdayLifetime).Unix(), Kind: BirthdayKind,
		ChatType: string(postgres.ChatDirect),
	}
	if prefs.Previews {
		p.Preview, p.SenderID = true, fmt.Sprint(c.UserID)
		name := emoji.Plain(c.DisplayName)
		p.Title, p.Body = name, name+" has a birthday today"
	}
	var failed error
	for _, d := range devices {
		dp := p
		if dp.Preview && b.Tokens != nil {
			dp.AvatarToken = b.Tokens.Avatar(c.ReceiverID, d.ID, d.SessionID, c.UserID)
		}
		err := b.Sender.Send(ctx, d, dp)
		if errors.Is(err, ErrUnregistered) {
			err = b.Devices.RemoveDevice(ctx, d.ID)
		}
		if err != nil {
			failed = err
		}
	}
	return failed
}
