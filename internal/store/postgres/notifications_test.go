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
		got, err := q.Devices(ctx, chat.ID, 123, []int64{b.ID})
		idle := 0
		for _, g := range got {
			if g.ActiveUntil == nil {
				idle++
			}
		}
		if err != nil || idle != want {
			t.Fatalf("devices=%d want=%d err=%v", idle, want, err)
		}
	}
	devices(1)
	client := uuid.New()
	must(q.Activity(ctx, b.ID, sess.ID, d.ID, client, true))
	devices(0)
	active, err := q.Devices(ctx, chat.ID, 123, []int64{b.ID})
	if err != nil || len(active) != 1 || active[0].ActiveUntil == nil || active[0].UserID != b.ID {
		t.Fatalf("active device must come back with its lease: %+v %v", active, err)
	}
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
	got, err := q.Devices(ctx, chat.ID, 124, []int64{b.ID})
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
	until := time.Now().Add(time.Minute).UTC().Truncate(time.Millisecond)
	if err := q.Defer(ctx, 123, until); err != nil {
		t.Fatal(err)
	}
	var next time.Time
	var attempts int
	if err := s.Pool().QueryRow(ctx, `SELECT next_at,attempts FROM notification_jobs WHERE message_id=123`).Scan(&next, &attempts); err != nil {
		t.Fatal(err)
	}
	if !next.Equal(until) || attempts != 1 {
		t.Fatalf("defer: next=%v attempts=%d", next, attempts)
	}
	_, err = s.Pool().Exec(ctx, `UPDATE notification_jobs SET next_at=now()-interval '1 second'`)
	if err != nil {
		t.Fatal(err)
	}
	if j, err = q.Claim(ctx); err != nil || !j.Deferred {
		t.Fatalf("deferred flag lost: %+v %v", j, err)
	}
	if err := q.Finish(ctx, 123); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationRecipientsCarryPreferencesAndBlocks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustUser(t, s, "alice")
	b := mustUser(t, s, "bob")
	chat, _, err := s.Chats().CreateDirect(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	q := s.Notifications()
	if err := q.SetPreferences(ctx, b.ID, NotificationPreferences{Enabled: true, Previews: true, BirthdayReminders: true}); err != nil {
		t.Fatal(err)
	}
	got, err := q.Recipients(ctx, chat.ID, a.ID, []int64{a.ID, b.ID, 999})
	if err != nil || len(got) != 1 || got[0].UserID != b.ID || !got[0].Preferences.Previews || got[0].Blocked {
		t.Fatalf("recipients: %+v %v", got, err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO user_blocks(blocker_id,blocked_id) VALUES($1,$2)`, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if got, err = q.Recipients(ctx, chat.ID, a.ID, []int64{b.ID}); err != nil || len(got) != 1 || !got[0].Blocked {
		t.Fatalf("block not reported: %+v %v", got, err)
	}
}

func TestRejectedDestinationIsDroppedAfterRepeatedFailures(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustUser(t, s, "alice")
	b := mustUser(t, s, "bob")
	chat, _, err := s.Chats().CreateDirect(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess := newSession(t, s, b.ID, "rejected-test")
	q := s.Notifications()
	d := NotificationDevice{ID: uuid.New(), Platform: "android", Destination: "rejected-token"}
	if err := q.Register(ctx, b.ID, sess.ID, d); err != nil {
		t.Fatal(err)
	}
	reject := func(mid int64, counts bool) bool {
		t.Helper()
		if err := q.Enqueue(ctx, chat.ID, mid, []int64{b.ID}); err != nil {
			t.Fatal(err)
		}
		removed, err := q.Rejected(ctx, mid, d.ID, counts)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := q.Devices(ctx, chat.ID, mid, []int64{b.ID}); !removed && len(got) != 0 {
			t.Fatal("a rejected destination must be closed for that message")
		}
		return removed
	}
	if reject(1, true) || reject(2, true) {
		t.Fatal("removed too early")
	}
	if err := q.Enqueue(ctx, chat.ID, 3, []int64{b.ID}); err != nil {
		t.Fatal(err)
	}
	if err := q.Delivered(ctx, 3, d.ID); err != nil {
		t.Fatal(err)
	}
	if reject(4, true) || reject(5, false) || reject(6, true) {
		t.Fatal("a success must reset the count, and a too-large payload must not count")
	}
	if !reject(7, true) {
		t.Fatal("third rejection in a row must remove the destination")
	}
	if got, _ := q.UserDevices(ctx, b.ID); len(got) != 0 {
		t.Fatalf("destination still registered: %+v", got)
	}
}
