package messages_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestRequestRecipientGetsNoPushUntilTheyReply(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _, err := f.pg.Chats().OpenDirect(ctx, alice, bob, true)
	if err != nil {
		t.Fatal(err)
	}
	var accepted []int64
	f.svc.SetRequestAccepted(func(_ context.Context, chatID, userID int64) {
		if chatID == chat.ID {
			accepted = append(accepted, userID)
		}
	})
	recipients := func(messageID int64) ([]int64, bool) {
		t.Helper()
		var out []int64
		err := f.pg.Pool().QueryRow(ctx, `SELECT recipients FROM notification_jobs WHERE message_id=$1`, messageID).Scan(&out)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return out, true
	}

	first := f.send(chat.ID, alice, "to-request", "hello")
	if got, queued := recipients(first.ID); queued {
		t.Fatalf("a request queued a push for %v", got)
	}
	copies, err := f.svc.Forward(ctx, alice, first.ID, []int64{chat.ID})
	if err != nil || len(copies) != 1 {
		t.Fatalf("forward: %v %v", copies, err)
	}
	if got, queued := recipients(copies[0].ID); queued {
		t.Fatalf("a forward into a request queued a push for %v", got)
	}
	if len(accepted) != 0 {
		t.Fatalf("the requester's messages accepted the request: %v", accepted)
	}
	entry, err := f.pg.Chats().Entry(ctx, bob, chat.ID)
	if err != nil || !entry.IsRequest {
		t.Fatalf("the recipient's chat is a request: %v %v", entry.IsRequest, err)
	}

	if got, ok := recipients(f.send(chat.ID, bob, "reply", "hi").ID); !ok || !slices.Equal(got, []int64{alice}) {
		t.Fatalf("the reply pushes to the requester: %v", got)
	}
	if !slices.Equal(accepted, []int64{bob}) {
		t.Fatalf("the reply accepts the request once: %v", accepted)
	}
	if entry, err = f.pg.Chats().Entry(ctx, bob, chat.ID); err != nil || entry.IsRequest {
		t.Fatalf("still a request after the reply: %v %v", entry.IsRequest, err)
	}
	if got, ok := recipients(f.send(chat.ID, alice, "after", "welcome").ID); !ok || !slices.Equal(got, []int64{bob}) {
		t.Fatalf("pushes resume after the reply: %v", got)
	}
	f.send(chat.ID, bob, "second", "again")
	if len(accepted) != 1 {
		t.Fatalf("later replies accept nothing: %v", accepted)
	}
}

func TestGroupMessagesStillPush(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	group, err := f.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: alice, Name: "Team", MemberIDs: []int64{bob, carol}})
	if err != nil {
		t.Fatal(err)
	}
	m := f.send(group.ID, alice, "group", "hello team")
	var got []int64
	if err := f.pg.Pool().QueryRow(ctx, `SELECT recipients FROM notification_jobs WHERE message_id=$1`, m.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []int64{bob, carol}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("group push recipients: %v", got)
	}
}

func TestRequestReadsStayPrivateUntilAccepted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _, err := f.pg.Chats().OpenDirect(ctx, alice, bob, true)
	if err != nil {
		t.Fatal(err)
	}
	lastReadRecipients := func() []int64 {
		f.rec.mu.Lock()
		defer f.rec.mu.Unlock()
		return f.rec.reads[len(f.rec.reads)-1]
	}
	bobsMarker := func() *int64 {
		t.Helper()
		markers, err := f.pg.Chats().OtherMembers(ctx, alice, []int64{chat.ID})
		if err != nil || len(markers) != 1 {
			t.Fatalf("markers: %v %v", markers, err)
		}
		return markers[0].MessageID
	}

	first := f.send(chat.ID, alice, "first", "hello")
	if _, err := f.svc.Read(ctx, bob, chat.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if got := lastReadRecipients(); !slices.Equal(got, []int64{bob}) {
		t.Fatalf("a read in a request reached %v", got)
	}
	if got := bobsMarker(); got != nil {
		t.Fatalf("the requester sees the request's read marker %d", *got)
	}

	if _, err := f.pg.Chats().AcceptRequest(ctx, bob, chat.ID); err != nil {
		t.Fatal(err)
	}
	second := f.send(chat.ID, alice, "second", "still there?")
	if _, err := f.svc.Read(ctx, bob, chat.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if got := lastReadRecipients(); !slices.Contains(got, alice) {
		t.Fatalf("after accepting, the read reaches the requester: %v", got)
	}
	if got := bobsMarker(); got == nil || *got != second.ID {
		t.Fatalf("after accepting, the requester sees the marker: %v", got)
	}
}
