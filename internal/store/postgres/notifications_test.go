package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNotificationDestinationsRespectSessionMuteReadAndRetries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustUser(t, s, "alice")
	b := mustUser(t, s, "bob")
	chat, _, err := s.Chats().CreateDirect(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess := newSession(t, s, b.ID, "notification-test")
	q := s.Notifications()
	d := NotificationDevice{ID: uuid.New(), Platform: "android", Destination: "test-token"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(q.Register(ctx, b.ID, sess.ID, d))
	must(q.Enqueue(ctx, chat.ID, 123, []int64{b.ID}))
	devices := func(want int) {
		t.Helper()
		got, err := q.Devices(ctx, b.ID, chat.ID, 123)
		if err != nil || len(got) != want {
			t.Fatalf("devices=%d want=%d err=%v", len(got), want, err)
		}
	}
	devices(1)
	client := uuid.New()
	must(q.Activity(ctx, b.ID, sess.ID, d.ID, client, true))
	devices(0)
	// A hidden tab cannot clear another tab's foreground lease.
	must(q.Activity(ctx, b.ID, sess.ID, d.ID, uuid.New(), false))
	devices(0)
	must(q.Activity(ctx, b.ID, sess.ID, d.ID, client, false))
	devices(1)
	must(q.Mute(ctx, b.ID, chat.ID, time.Now().Add(time.Hour)))
	devices(0)
	must(q.Mute(ctx, b.ID, chat.ID, time.Now().Add(-time.Hour)))
	devices(1)
	must(q.SetPreferences(ctx, b.ID, NotificationPreferences{Enabled: false}))
	devices(0)
	must(q.SetPreferences(ctx, b.ID, NotificationPreferences{Enabled: true}))
	devices(1)
	must(q.Delivered(ctx, 123, d.ID))
	devices(0)
	_, err = s.Pool().Exec(ctx, `DELETE FROM notification_deliveries`)
	must(err)
	devices(1)
	_, err = s.Chats().MarkRead(ctx, chat.ID, b.ID, 123)
	must(err)
	devices(0)
	must(q.Enqueue(ctx, chat.ID, 124, []int64{b.ID}))
	must(s.Sessions().Revoke(ctx, b.ID, sess.ID))
	got, err := q.Devices(ctx, b.ID, chat.ID, 124)
	if err != nil || len(got) != 0 {
		t.Fatalf("revoked session received a notification: %v", err)
	}
}
func TestNotificationJobLeaseAndRecovery(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustUser(t, s, "alice")
	b := mustUser(t, s, "bob")
	chat, _, err := s.Chats().CreateDirect(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	q := s.Notifications()
	if err := q.Enqueue(ctx, chat.ID, 123, []int64{b.ID}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool().Exec(ctx, `UPDATE notification_jobs SET next_at=now()-interval '1 second'`)
	if err != nil {
		t.Fatal(err)
	}
	j, err := q.Claim(ctx)
	if err != nil || j.Attempts != 1 {
		t.Fatalf("claim: %+v %v", j, err)
	}
	if _, err = q.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("leased job claimed again: %v", err)
	}
	_, err = s.Pool().Exec(ctx, `UPDATE notification_jobs SET next_at=now()-interval '1 second'`)
	if err != nil {
		t.Fatal(err)
	}
	j, err = q.Claim(ctx)
	if err != nil || j.Attempts != 2 {
		t.Fatalf("recovery: %+v %v", j, err)
	}
	if err := q.Finish(ctx, 123); err != nil {
		t.Fatal(err)
	}
}
