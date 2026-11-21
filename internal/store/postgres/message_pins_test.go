package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestMessagePinLimitsHoldUnderConcurrency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	owner, member := mustUser(t, s, "owner"), mustUser(t, s, "member")
	chat, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "G", MemberIDs: []int64{member.ID}})
	if err != nil {
		t.Fatal(err)
	}
	pins := s.MessagePins()
	const maxPins = 3
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, limited := 0, 0
	for i := range 8 {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_, ok, _, err := pins.PinForEveryone(ctx, chat.ID, id, owner.ID, maxPins)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, ErrLimit):
				limited++
			case err != nil:
				t.Errorf("pin %d: %v", id, err)
			case ok:
				created++
			}
		}(int64(100 + i))
	}
	wg.Wait()
	if created != maxPins || limited != 8-maxPins {
		t.Fatalf("created %d, limited %d", created, limited)
	}
	for i := range maxPins {
		if _, changed, err := pins.PinForMe(ctx, member.ID, chat.ID, int64(200+i), maxPins); err != nil || !changed {
			t.Fatalf("own pin %d: %v", i, err)
		}
	}
	if _, _, err := pins.PinForMe(ctx, member.ID, chat.ID, 300, maxPins); !errors.Is(err, ErrLimit) {
		t.Fatalf("own pin over the limit: %v", err)
	}
	if _, changed, err := pins.PinForMe(ctx, owner.ID, chat.ID, 300, maxPins); err != nil || !changed {
		t.Fatalf("another member's own pins do not count: %v", err)
	}
	if _, changed, err := pins.PinForMe(ctx, member.ID, chat.ID, 200, maxPins); err != nil || changed {
		t.Fatalf("pinning an own pin again: %v %v", changed, err)
	}
	counts, err := pins.VisiblePinCounts(ctx, member.ID, []int64{chat.ID})
	if err != nil || counts[chat.ID] != 2*maxPins {
		t.Fatalf("counts: %v %v", counts, err)
	}
}

func TestMessagePinCleanup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	owner, member := mustUser(t, s, "owner"), mustUser(t, s, "member")
	chat, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "G", MemberIDs: []int64{member.ID}})
	if err != nil {
		t.Fatal(err)
	}
	pins := s.MessagePins()
	visible := func(user int64) map[int64]string {
		t.Helper()
		got, err := pins.VisiblePins(ctx, user, chat.ID, nil, 100)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]string{}
		for _, p := range got {
			out[p.MessageID] = p.Scope
		}
		return out
	}
	for _, id := range []int64{10, 20, 30} {
		if _, _, _, err := pins.PinForEveryone(ctx, chat.ID, id, owner.ID, 10); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{15, 25} {
		if _, _, err := pins.PinForMe(ctx, member.ID, chat.ID, id, 10); err != nil {
			t.Fatal(err)
		}
	}
	if changed, err := pins.UnpinForMe(ctx, member.ID, chat.ID, 30); err != nil || !changed {
		t.Fatalf("hide: %v %v", changed, err)
	}
	if got := visible(member.ID); len(got) != 4 || got[15] != PinScopeMe || got[10] != PinScopeEveryone {
		t.Fatalf("member sees %v", got)
	}
	if err := pins.ClearUserPins(ctx, member.ID, chat.ID, 15); err != nil {
		t.Fatal(err)
	}
	if err := s.Chats().ClearForUser(ctx, chat.ID, member.ID, 15, false); err != nil {
		t.Fatal(err)
	}
	if got := visible(member.ID); len(got) != 2 || got[20] == "" || got[25] == "" {
		t.Fatalf("after clearing for the member: %v", got)
	}
	if got := visible(owner.ID); len(got) != 3 {
		t.Fatalf("clearing for one member changed the owner's pins: %v", got)
	}
	if err := pins.ClearPins(ctx, chat.ID, 20); err != nil {
		t.Fatal(err)
	}
	if got := visible(owner.ID); len(got) != 1 || got[30] == "" {
		t.Fatalf("after clearing for everyone: %v", got)
	}
	if err := pins.RemoveUserPins(ctx, member.ID, chat.ID); err != nil {
		t.Fatal(err)
	}
	if got := visible(member.ID); len(got) != 1 || got[30] == "" {
		t.Fatalf("after leaving, own pins and hides are gone: %v", got)
	}
	removed, err := pins.RemoveMessagePins(ctx, chat.ID, []int64{30, 99})
	if err != nil || len(removed) != 1 || removed[0] != 30 {
		t.Fatalf("remove: %v %v", removed, err)
	}
	if _, err := s.Chats().Delete(ctx, chat.ID); err != nil {
		t.Fatal(err)
	}
}
