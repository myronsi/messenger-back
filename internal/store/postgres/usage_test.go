package postgres

import (
	"context"
	"testing"
	"time"
)

func TestUsageFigures(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	end := day.AddDate(0, 0, 1)
	for i, seen := range map[string][]time.Time{
		"alice": {day.Add(9 * time.Hour), day.AddDate(0, 0, -50)},
		"bob":   {day.Add(-time.Hour)},
		"carol": {day.AddDate(0, 0, -20)},
		"dave":  {day.AddDate(0, 0, -40)},
	} {
		u := mustUser(t, s, i)
		for _, at := range seen {
			sess, err := s.Sessions().Create(ctx, NewSession{UserID: u.ID, RefreshTokenHash: i + at.String(), ExpiresAt: end.AddDate(0, 1, 0)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Pool().Exec(ctx, `UPDATE user_sessions SET last_active_at = $2 WHERE id = $1`, sess.ID, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	owner := mustUser(t, s, "erin")
	video, err := s.Attachments().Create(ctx, NewAttachment{UploaderID: &owner.ID, Purpose: PurposeMessage, Kind: "video", StorageKey: "attachments/a", MimeType: "video/mp4", Size: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attachments().Create(ctx, NewAttachment{UploaderID: &owner.ID, Purpose: PurposeMessage, Kind: "file", StorageKey: "attachments/b", MimeType: "application/pdf", Size: 250}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE attachments SET variants = '{"720p":{"key":"v/720","size":400},"poster":{"key":"v/p","size":30}}' WHERE id = $1`, video.ID); err != nil {
		t.Fatal(err)
	}

	got, err := s.Usage(ctx, day, end)
	if err != nil {
		t.Fatal(err)
	}
	if want := (UsageFigures{ActiveDay: 1, Active30d: 3, StorageBytes: 1000 + 250 + 400 + 30}); got != want {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
}
