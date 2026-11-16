package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBirthdayCandidates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	anna, berlin, tokyo, leap, stranger := mustUser(t, s, "anna"), mustUser(t, s, "berlin"), mustUser(t, s, "tokyo"), mustUser(t, s, "leap"), mustUser(t, s, "stranger")
	for _, r := range []User{berlin, tokyo, leap} {
		if _, _, err := s.Chats().CreateDirect(ctx, anna.ID, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	zone := func(u User, tz string) {
		t.Helper()
		if _, err := s.Users().PatchProfile(ctx, u.ID, ProfilePatch{TimeZone: &tz}); err != nil {
			t.Fatal(err)
		}
	}
	zone(berlin, "Europe/Berlin")
	zone(tokyo, "Asia/Tokyo")
	birthday := func(u User, day, month int) {
		t.Helper()
		if _, err := s.Users().PatchProfile(ctx, u.ID, ProfilePatch{SetBirthday: true, Birthday: &Birthday{Day: day, Month: month}}); err != nil {
			t.Fatal(err)
		}
	}
	birthday(anna, 15, 5)
	birthday(leap, 29, 2)
	_ = stranger

	receivers := func(now time.Time) map[int64]int64 {
		t.Helper()
		got, err := s.Users().BirthdayCandidates(ctx, now, 9, 100)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]int64{}
		for _, c := range got {
			out[c.ReceiverID] = c.UserID
		}
		return out
	}

	at0730utc := time.Date(2026, 5, 15, 7, 30, 0, 0, time.UTC)
	got := receivers(at0730utc)
	if got[berlin.ID] != anna.ID || got[tokyo.ID] != anna.ID || len(got) != 2 {
		t.Fatalf("09:30 in Berlin and 16:30 in Tokyo on 15 May, 07:30 in UTC is too early: %v", got)
	}
	if got := receivers(time.Date(2026, 5, 14, 23, 30, 0, 0, time.UTC)); len(got) != 0 {
		t.Fatalf("08:30 on 15 May in Tokyo is before nine, and Berlin is still on the 15th at 01:30: %v", got)
	}
	marked, err := s.Users().MarkBirthdayReminder(ctx, anna.ID, berlin.ID, 2026)
	if err != nil || !marked {
		t.Fatalf("mark: %v %v", marked, err)
	}
	if again, _ := s.Users().MarkBirthdayReminder(ctx, anna.ID, berlin.ID, 2026); again {
		t.Fatal("a reminder is recorded once a year")
	}
	if got := receivers(at0730utc); got[berlin.ID] != 0 || got[tokyo.ID] != anna.ID {
		t.Fatalf("berlin got it already: %v", got)
	}

	if err := s.Social().Block(ctx, anna.ID, tokyo.ID); err != nil {
		t.Fatal(err)
	}
	if got := receivers(at0730utc); got[tokyo.ID] != 0 {
		t.Fatalf("a block stops the reminder: %v", got)
	}

	if got := receivers(time.Date(2027, 2, 28, 12, 0, 0, 0, time.UTC)); got[anna.ID] != leap.ID {
		t.Fatalf("29 February birthdays come on 28 February in other years: %v", got)
	}
	if got := receivers(time.Date(2028, 2, 28, 12, 0, 0, 0, time.UTC)); got[anna.ID] != 0 {
		t.Fatalf("in a leap year they wait for the 29th: %v", got)
	}
	if got := receivers(time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC)); got[anna.ID] != leap.ID {
		t.Fatalf("29 February in a leap year: %v", got)
	}

	if err := s.Notifications().SetPreferences(ctx, anna.ID, NotificationPreferences{Enabled: true, BirthdayReminders: false}); err != nil {
		t.Fatal(err)
	}
	if got := receivers(time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC)); got[anna.ID] != 0 {
		t.Fatalf("turned off: %v", got)
	}
}

func TestProfileMediaKeepsAttachmentsAlive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "melody")
	add := func(purpose string) Attachment {
		t.Helper()
		a, err := s.Attachments().Create(ctx, NewAttachment{UploaderID: &u.ID, Purpose: purpose, Kind: "audio", Filename: "f", StorageKey: "profiles/" + uuid.NewString(), MimeType: "audio/mp4", Size: 1})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	music, bg := add(PurposeProfileMusic), add(PurposeProfileBackground)
	title := "Song"
	got, err := s.Users().SetMusic(ctx, u.ID, &music.ID, &title, nil)
	if err != nil || got.MusicAttachmentID == nil || *got.MusicTitle != "Song" {
		t.Fatalf("set music: %+v %v", got, err)
	}
	if got, err = s.Users().SetBackground(ctx, u.ID, &Background{Kind: BackgroundMedia, AttachmentID: &bg.ID}); err != nil || got.Background == nil || got.Background.Kind != BackgroundMedia {
		t.Fatalf("set background: %+v %v", got, err)
	}
	if got.ProfileVersion != 2 {
		t.Fatalf("music and background grow the version: %d", got.ProfileVersion)
	}
	list, err := s.Attachments().Unreferenced(ctx, time.Now().Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.ID == music.ID || a.ID == bg.ID {
			t.Fatalf("attachment %s in use is listed as unreferenced", a.ID)
		}
	}
	owners, err := s.Attachments().UsersWithProfileMedia(ctx, bg.ID)
	if err != nil || len(owners) != 1 || owners[0] != u.ID {
		t.Fatalf("owners %v %v", owners, err)
	}
	if _, err := s.Users().SetBackground(ctx, u.ID, &Background{Kind: BackgroundColor, Colors: []string{"#zzzzzz"}}); err == nil {
		t.Fatal("the schema rejects a bad color")
	}
	if got, err = s.Users().SetMusic(ctx, u.ID, nil, &title, nil); err != nil || got.MusicAttachmentID != nil || got.MusicTitle != nil {
		t.Fatalf("clear music: %+v %v", got, err)
	}
}
