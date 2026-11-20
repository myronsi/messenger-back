package notifications

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type birthdayStoreFake struct {
	candidates []postgres.BirthdayCandidate
	marked     map[[3]int64]bool
	calls      int
	limit      int
}

func (b *birthdayStoreFake) BirthdayCandidates(context.Context, time.Time, int, int) ([]postgres.BirthdayCandidate, error) {
	b.calls++
	out := []postgres.BirthdayCandidate{}
	for _, c := range b.candidates {
		if b.limit > 0 && len(out) == b.limit {
			break
		}
		if !b.marked[[3]int64{c.UserID, c.ReceiverID, int64(c.Year)}] {
			out = append(out, c)
		}
	}
	return out, nil
}

func (b *birthdayStoreFake) MarkBirthdayReminder(_ context.Context, userID, receiverID int64, year int) (bool, error) {
	k := [3]int64{userID, receiverID, int64(year)}
	if b.marked[k] {
		return false, nil
	}
	b.marked[k] = true
	return true, nil
}

type birthdayPrivacyFake struct {
	settings   map[int64]postgres.PrivacySettings
	exceptions map[int64]map[int64]map[string]string
}

func (p birthdayPrivacyFake) Privacy(_ context.Context, ids []int64) (map[int64]postgres.PrivacySettings, error) {
	out := map[int64]postgres.PrivacySettings{}
	for _, id := range ids {
		s, ok := p.settings[id]
		if !ok {
			s = postgres.DefaultPrivacy(id)
		}
		out[id] = s
	}
	return out, nil
}

func (p birthdayPrivacyFake) ExceptionsOf(_ context.Context, owner int64, _ []int64) (map[int64]map[string]string, error) {
	return p.exceptions[owner], nil
}

type birthdayDevicesFake struct {
	postgres.NotificationRepository
	prefs   map[int64]postgres.NotificationPreferences
	devices map[int64][]postgres.NotificationDevice
}

func (d birthdayDevicesFake) Preferences(_ context.Context, uid int64) (postgres.NotificationPreferences, error) {
	if p, ok := d.prefs[uid]; ok {
		return p, nil
	}
	return postgres.NotificationPreferences{Enabled: true, BirthdayReminders: true}, nil
}

func (d birthdayDevicesFake) UserDevices(_ context.Context, uid int64) ([]postgres.NotificationDevice, error) {
	return d.devices[uid], nil
}

func birthdayFixture() (*Birthdays, *birthdayStoreFake, *senderFake, birthdayPrivacyFake, birthdayDevicesFake) {
	store := &birthdayStoreFake{marked: map[[3]int64]bool{}, candidates: []postgres.BirthdayCandidate{
		{ReceiverID: 10, UserID: 1, DisplayName: "Anna", ChatID: 100, Year: 2026},
		{ReceiverID: 11, UserID: 1, DisplayName: "Anna", ChatID: 101, Year: 2026},
		{ReceiverID: 12, UserID: 2, DisplayName: "Ben", ChatID: 102, Year: 2026},
	}}
	privacy := birthdayPrivacyFake{settings: map[int64]postgres.PrivacySettings{}, exceptions: map[int64]map[int64]map[string]string{}}
	devices := birthdayDevicesFake{
		prefs: map[int64]postgres.NotificationPreferences{},
		devices: map[int64][]postgres.NotificationDevice{
			10: {{ID: uuid.New(), UserID: 10}}, 11: {{ID: uuid.New(), UserID: 11}}, 12: {{ID: uuid.New(), UserID: 12}},
		},
	}
	sender := &senderFake{}
	b := &Birthdays{Store: store, Privacy: privacy, Devices: devices, Sender: sender, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC) }}
	return b, store, sender, privacy, devices
}

func TestBirthdayRemindersRespectPrivacy(t *testing.T) {
	b, _, sender, privacy, _ := birthdayFixture()
	s := postgres.DefaultPrivacy(2)
	s.BirthdayVisibility = postgres.VisibleNobody
	privacy.settings[2] = s
	privacy.exceptions[1] = map[int64]map[string]string{11: {postgres.SettingBirthday: postgres.EffectDeny}}

	sent, err := b.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 || len(sender.sent) != 1 || sender.sent[0].UserID != "10" || sender.sent[0].ChatID != "100" || sender.sent[0].Kind != BirthdayKind {
		t.Fatalf("only receiver 10 may see Anna's birthday: %d %+v", sent, sender.sent)
	}
	if sender.sent[0].Preview || sender.sent[0].Title != "Messenger" || sender.sent[0].SenderID != "" {
		t.Fatalf("without previews the name stays hidden: %+v", sender.sent[0])
	}
}

func TestBirthdayRemindersAreSentOnce(t *testing.T) {
	b, _, sender, _, devices := birthdayFixture()
	devices.prefs[10] = postgres.NotificationPreferences{Enabled: true, Previews: true, BirthdayReminders: true}
	devices.prefs[11] = postgres.NotificationPreferences{Enabled: true, BirthdayReminders: false}
	if _, err := b.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 2 {
		t.Fatalf("receivers 10 and 12, once each: %+v", sender.sent)
	}
	for _, p := range sender.sent {
		if p.UserID == "10" && (p.Title != "Anna" || !p.Preview || p.SenderID != "1") {
			t.Fatalf("with previews the name is shown: %+v", p)
		}
		if p.UserID == "11" {
			t.Fatal("receiver 11 turned birthday reminders off")
		}
	}
}

func TestHiddenBirthdaysDoNotBlockLaterReceivers(t *testing.T) {
	b, store, sender, privacy, devices := birthdayFixture()
	hidden := postgres.DefaultPrivacy(1)
	hidden.BirthdayVisibility = postgres.VisibleNobody
	privacy.settings[1] = hidden
	store.candidates = nil
	for i := range birthdayBatch {
		store.candidates = append(store.candidates, postgres.BirthdayCandidate{ReceiverID: int64(1000 + i), UserID: 1, DisplayName: "Anna", ChatID: int64(5000 + i), Year: 2026})
	}
	store.candidates = append(store.candidates, postgres.BirthdayCandidate{ReceiverID: 12, UserID: 2, DisplayName: "Ben", ChatID: 102, Year: 2026})
	store.limit = birthdayBatch
	devices.prefs[12] = postgres.NotificationPreferences{Enabled: true, Previews: true, BirthdayReminders: true}
	b.Tokens = NewActionTokens("secret")
	if _, err := b.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 || sender.sent[0].UserID != "12" {
		t.Fatalf("a full batch of hidden birthdays must not starve receiver 12: %+v", sender.sent)
	}
	if got, err := b.Tokens.ParseAvatar(sender.sent[0].AvatarToken); err != nil || got.IDs[0] != 2 || got.Recipient != 12 {
		t.Fatalf("birthday preview needs the avatar: %v", err)
	}
	if _, err := b.RunOnce(t.Context()); err != nil || store.calls > 4 {
		t.Fatalf("hidden pairs must be settled, not queried again: calls=%d err=%v", store.calls, err)
	}
}
