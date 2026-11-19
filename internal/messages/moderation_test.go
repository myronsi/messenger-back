package messages_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func contents(ms []scylla.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = *m.Content
	}
	return out
}

func TestReportWindowKeepsFiveVisibleMessagesOnEachSide(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	group, err := f.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: alice, Name: "G", MemberIDs: []int64{bob, carol}})
	if err != nil {
		t.Fatal(err)
	}
	var sent []scylla.Message
	for i := range 15 {
		sent = append(sent, f.send(group.ID, alice, fmt.Sprint("t", i), fmt.Sprint("m", i)))
	}
	if err := f.svc.Delete(ctx, alice, group.ID, sent[5].ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(ctx, bob, group.ID, sent[9].ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	w, err := f.svc.ReportWindow(ctx, bob, group.ID, sent[7].ID)
	if err != nil {
		t.Fatal(err)
	}
	if w.Target.ID != sent[7].ID {
		t.Fatalf("target %d", w.Target.ID)
	}
	if got, want := contents(w.Before), []string{"m1", "m2", "m3", "m4", "m6"}; !slices.Equal(got, want) {
		t.Fatalf("before %v, want %v", got, want)
	}
	if got, want := contents(w.After), []string{"m8", "m10", "m11", "m12", "m13"}; !slices.Equal(got, want) {
		t.Fatalf("after %v, want %v", got, want)
	}
	carolView, err := f.svc.ReportWindow(ctx, carol, group.ID, sent[7].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := contents(carolView.After); !slices.Contains(got, "m9") {
		t.Fatalf("carol did not delete m9: %v", got)
	}
	if _, err := f.svc.ReportWindow(ctx, bob, group.ID, sent[9].ID); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("hidden for bob: %v", err)
	}
	if _, err := f.svc.ReportWindow(ctx, bob, group.ID, sent[5].ID); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("deleted for everyone: %v", err)
	}
	if _, err := f.svc.ReportWindow(ctx, f.user("dave"), group.ID, sent[7].ID); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("not a member: %v", err)
	}
	first, err := f.svc.ReportWindow(ctx, bob, group.ID, sent[0].ID)
	if err != nil || len(first.Before) != 0 || len(first.After) != 5 {
		t.Fatalf("first message: %+v %v", first, err)
	}
}

func TestRemoveByModerationDeletesForEveryoneAndKeepsTheReasonForTheSender(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	m := f.send(chat.ID, alice, "x", "buy now")
	reason := scylla.ModerationRemoval{Code: "spam", Text: "Advertising is not allowed."}
	removed, err := f.svc.RemoveByModeration(ctx, chat.ID, m.ID, reason)
	if err != nil {
		t.Fatal(err)
	}
	if !removed.Deleted || removed.Content != nil || removed.Moderation == nil || *removed.Moderation != reason {
		t.Fatalf("removed: %+v", removed)
	}
	page, err := f.svc.History(ctx, bob, scylla.PageQuery{ChatID: chat.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := page.Messages[len(page.Messages)-1]
	if got.ID != m.ID || !got.Deleted || got.Moderation == nil || got.Moderation.Code != "spam" {
		t.Fatalf("history: %+v", got)
	}
	for _, uid := range []int64{alice, bob} {
		if r, ok := f.rec.removed[uid]; !ok || r.Moderation == nil {
			t.Fatalf("user %d was not told: %+v", uid, f.rec.removed)
		}
	}
	if !slices.Contains(f.events.types(), events.MessageDeleted) {
		t.Fatalf("events: %v", f.events.types())
	}
	if _, err := f.svc.Edit(ctx, alice, chat.ID, m.ID, "again"); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("editing a removed message: %v", err)
	}
	again, err := f.svc.RemoveByModeration(ctx, chat.ID, m.ID, scylla.ModerationRemoval{Code: "other"})
	if err != nil || again.Moderation.Code != "spam" {
		t.Fatalf("second removal keeps the first reason: %+v %v", again, err)
	}
	if _, err := f.svc.RemoveByModeration(ctx, chat.ID, m.ID+1000, reason); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("unknown message: %v", err)
	}
	if _, err := f.svc.RemoveByModeration(ctx, chat.ID, m.ID, scylla.ModerationRemoval{}); !errors.Is(err, messages.ErrInvalid) {
		t.Fatalf("missing code: %v", err)
	}
}
