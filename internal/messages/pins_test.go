package messages_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type pinCall struct {
	change     messages.PinChange
	recipients []int64
}

func (r *recorder) PinChanged(_ context.Context, c messages.PinChange, recipients []int64) {
	r.mu.Lock()
	r.pins = append(r.pins, pinCall{change: c, recipients: slices.Clone(recipients)})
	r.mu.Unlock()
}

func (r *recorder) lastPin() pinCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pins) == 0 {
		return pinCall{}
	}
	return r.pins[len(r.pins)-1]
}

func (r *recorder) systemKinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.created {
		if e, ok := messages.ParseSystemEvent(m); ok {
			out = append(out, e.Kind)
		}
	}
	return out
}

func (f *fixture) pinned(user, chat int64) map[int64]messages.Pin {
	f.t.Helper()
	page, err := f.svc.Pins(context.Background(), user, chat, nil, 100)
	if err != nil {
		f.t.Fatalf("pins: %v", err)
	}
	out := map[int64]messages.Pin{}
	for _, p := range page.Pins {
		out[p.Message.ID] = p
	}
	return out
}

type pinGroup struct {
	id                        int64
	owner, admin, mod, member int64
	admin2, mod2, member2     int64
}

func (f *fixture) pinGroup() pinGroup {
	f.t.Helper()
	ctx := context.Background()
	g := pinGroup{
		owner: f.user("owner"), admin: f.user("admin"), mod: f.user("mod"), member: f.user("member"),
		admin2: f.user("admin2"), mod2: f.user("mod2"), member2: f.user("member2"),
	}
	chat, err := f.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: g.owner, Name: "G", MemberIDs: []int64{g.admin, g.mod, g.member, g.admin2, g.mod2, g.member2}})
	if err != nil {
		f.t.Fatal(err)
	}
	g.id = chat.ID
	for uid, role := range map[int64]postgres.Role{g.admin: postgres.RoleAdmin, g.admin2: postgres.RoleAdmin, g.mod: postgres.RoleModerator, g.mod2: postgres.RoleModerator} {
		if err := f.pg.Chats().SetRole(ctx, chat.ID, uid, role); err != nil {
			f.t.Fatal(err)
		}
	}
	return g
}

func TestGroupMembersPinOnlyForThemselves(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g := f.pinGroup()
	m := f.send(g.id, g.member, "p-1", "hello")
	if _, err := f.svc.Pin(ctx, g.member, g.id, m.ID, messages.ScopeEveryone); !errors.Is(err, messages.ErrForbidden) {
		t.Fatalf("member pinning for everyone: %v", err)
	}
	pin, err := f.svc.Pin(ctx, g.member, g.id, m.ID, messages.ScopeMe)
	if err != nil || pin.Scope != messages.ScopeMe || pin.CanUnpinForEveryone {
		t.Fatalf("own pin: %+v %v", pin, err)
	}
	if c := f.rec.lastPin(); !slices.Equal(c.recipients, []int64{g.member}) || c.change.Count == nil || *c.change.Count != 1 {
		t.Fatalf("own pin event: %+v", c)
	}
	if len(f.pinned(g.owner, g.id)) != 0 {
		t.Fatal("an own pin is visible to others")
	}
	if slices.Contains(f.rec.systemKinds(), messages.SystemMessagePinned) {
		t.Fatal("an own pin posted a system message")
	}
	if _, err := f.svc.Pin(ctx, g.mod, g.id, m.ID, "all"); !errors.Is(err, messages.ErrInvalid) {
		t.Fatalf("bad scope: %v", err)
	}
	stranger := f.user("stranger")
	if _, err := f.svc.Pin(ctx, stranger, g.id, m.ID, messages.ScopeMe); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("stranger: %v", err)
	}
}

func TestStaffPinForEveryoneWithSystemLine(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g := f.pinGroup()
	m := f.send(g.id, g.member, "p-1", strings.Repeat("long text ", 10))
	pin, err := f.svc.Pin(ctx, g.mod, g.id, m.ID, messages.ScopeEveryone)
	if err != nil || pin.Scope != messages.ScopeEveryone || !pin.CanUnpinForEveryone || pin.PinnedBy != g.mod {
		t.Fatalf("pin for everyone: %+v %v", pin, err)
	}
	c := f.rec.lastPin()
	if c.change.Action != messages.PinActionPinned || c.change.Scope != messages.ScopeEveryone || len(c.recipients) != 7 {
		t.Fatalf("pin event: %+v", c)
	}
	var line messages.SystemEvent
	for _, msg := range f.rec.created {
		if e, ok := messages.ParseSystemEvent(msg); ok && e.Kind == messages.SystemMessagePinned {
			line = e
		}
	}
	if line.MessageID == nil || *line.MessageID != m.ID || line.ActorName != "mod" || line.MessageType != scylla.TypeText {
		t.Fatalf("system line: %+v", line)
	}
	if !strings.HasSuffix(line.Snippet, "…") || len([]rune(line.Snippet)) != 61 {
		t.Fatalf("snippet: %q", line.Snippet)
	}
	for _, uid := range []int64{g.owner, g.member, g.member2} {
		if p, ok := f.pinned(uid, g.id)[m.ID]; !ok || p.Scope != messages.ScopeEveryone {
			t.Fatalf("user %d does not see the pin", uid)
		}
	}
	before := len(f.rec.created)
	if _, err := f.svc.Pin(ctx, g.admin, g.id, m.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if len(f.rec.created) != before {
		t.Fatal("pinning a pinned message posted another line")
	}
	sys := f.rec.created[len(f.rec.created)-1]
	if _, err := f.svc.Pin(ctx, g.owner, g.id, sys.ID, messages.ScopeMe); !errors.Is(err, messages.ErrInvalid) {
		t.Fatalf("pinning a system message: %v", err)
	}
}

func TestUnpinForEveryoneFollowsTheRank(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g := f.pinGroup()
	cases := []struct {
		name          string
		pinner, actor int64
		allowed       bool
	}{
		{"owner over admin", g.admin, g.owner, true},
		{"admin over admin", g.admin, g.admin2, true},
		{"admin over moderator", g.mod, g.admin, true},
		{"moderator over moderator", g.mod, g.mod2, true},
		{"moderator under admin", g.admin, g.mod, false},
		{"moderator under owner", g.owner, g.mod, false},
		{"admin under owner", g.owner, g.admin, false},
		{"member", g.mod, g.member, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := f.send(g.id, g.member, "r-"+string(rune('a'+i)), "x")
			pin, err := f.svc.Pin(ctx, tc.pinner, g.id, m.ID, messages.ScopeEveryone)
			if err != nil {
				t.Fatal(err)
			}
			if page, _ := f.svc.Pins(ctx, tc.actor, g.id, nil, 100); !slices.ContainsFunc(page.Pins, func(p messages.Pin) bool {
				return p.Message.ID == pin.Message.ID && p.CanUnpinForEveryone == tc.allowed
			}) {
				t.Fatal("can_unpin_for_everyone does not match")
			}
			err = f.svc.Unpin(ctx, tc.actor, g.id, m.ID, messages.ScopeEveryone)
			if tc.allowed && err != nil {
				t.Fatalf("unpin: %v", err)
			}
			if !tc.allowed && !errors.Is(err, messages.ErrForbidden) {
				t.Fatalf("unpin should be forbidden: %v", err)
			}
			_, still := f.pinned(g.member2, g.id)[m.ID]
			if still == tc.allowed {
				t.Fatalf("still pinned: %v", still)
			}
		})
	}
}

func TestPinnerWhoLeftOrWasDemotedCountsAsMember(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g := f.pinGroup()
	m1 := f.send(g.id, g.member, "d-1", "one")
	m2 := f.send(g.id, g.member, "d-2", "two")
	if _, err := f.svc.Pin(ctx, g.admin, g.id, m1.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Pin(ctx, g.admin2, g.id, m2.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if err := f.pg.Chats().SetRole(ctx, g.id, g.admin, postgres.RoleMember); err != nil {
		t.Fatal(err)
	}
	if err := f.pg.Chats().RemoveMember(ctx, g.id, g.admin2); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{m1.ID, m2.ID} {
		if err := f.svc.Unpin(ctx, g.mod, g.id, id, messages.ScopeEveryone); err != nil {
			t.Fatalf("moderator unpinning a former admin's pin: %v", err)
		}
	}
}

func TestDirectChatSharedPins(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.user("anna"), f.user("bert")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, a, b)
	m := f.send(chat.ID, a, "s-1", "both")
	if _, err := f.svc.Pin(ctx, a, chat.ID, m.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if p := f.pinned(b, chat.ID)[m.ID]; p.Scope != messages.ScopeEveryone || !p.CanUnpinForEveryone {
		t.Fatalf("bert's view: %+v", p)
	}
	if err := f.svc.Unpin(ctx, b, chat.ID, m.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.pinned(b, chat.ID)[m.ID]; ok {
		t.Fatal("hidden pin still visible for bert")
	}
	if _, ok := f.pinned(a, chat.ID)[m.ID]; !ok {
		t.Fatal("hiding removed the pin for anna")
	}
	if c := f.rec.lastPin(); !slices.Equal(c.recipients, []int64{b}) || c.change.Scope != messages.ScopeMe || c.change.Action != messages.PinActionUnpinned {
		t.Fatalf("hide event: %+v", c)
	}
	if _, err := f.svc.Pin(ctx, b, chat.ID, m.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	if p, ok := f.pinned(b, chat.ID)[m.ID]; !ok || p.Scope != messages.ScopeEveryone {
		t.Fatalf("pinning for me did not show the shared pin again: %+v", p)
	}
	if err := f.svc.Unpin(ctx, b, chat.ID, m.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if len(f.pinned(a, chat.ID)) != 0 || len(f.pinned(b, chat.ID)) != 0 {
		t.Fatal("unpin for both left a pin")
	}
	if !slices.Contains(f.rec.systemKinds(), messages.SystemMessageUnpinned) {
		t.Fatal("no unpinned line")
	}
	if err := f.svc.Unpin(ctx, b, chat.ID, m.ID, messages.ScopeEveryone); err != nil {
		t.Fatalf("unpinning again: %v", err)
	}
}

func TestOwnPinUnderASharedPin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g := f.pinGroup()
	m := f.send(g.id, g.member, "o-1", "mine first")
	if _, err := f.svc.Pin(ctx, g.member, g.id, m.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Pin(ctx, g.mod, g.id, m.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	page, _ := f.svc.Pins(ctx, g.member, g.id, nil, 100)
	if len(page.Pins) != 1 || page.Pins[0].Scope != messages.ScopeEveryone {
		t.Fatalf("pinned both ways shows once: %+v", page.Pins)
	}
	if err := f.svc.Unpin(ctx, g.mod, g.id, m.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if p, ok := f.pinned(g.member, g.id)[m.ID]; !ok || p.Scope != messages.ScopeMe {
		t.Fatalf("own pin did not come back: %+v", p)
	}
	if err := f.svc.Unpin(ctx, g.member, g.id, m.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	if len(f.pinned(g.member, g.id)) != 0 {
		t.Fatal("own pin still there")
	}
}

func TestOnlyOwnPinsWhereSharedPinsDoNotFit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.user("anna"), f.user("bert")
	saved, _, err := f.pg.Chats().CreateDirect(ctx, a, a)
	if err != nil {
		t.Fatal(err)
	}
	note := f.send(saved.ID, a, "n-1", "note")
	if _, err := f.svc.Pin(ctx, a, saved.ID, note.ID, messages.ScopeEveryone); !errors.Is(err, messages.ErrForbidden) {
		t.Fatalf("saved messages for everyone: %v", err)
	}
	if _, err := f.svc.Pin(ctx, a, saved.ID, note.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	direct, _, _ := f.pg.Chats().CreateDirect(ctx, a, b)
	m := f.send(direct.ID, a, "n-2", "hi")
	if err := f.pg.Social().Block(ctx, b, a); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Pin(ctx, a, direct.ID, m.ID, messages.ScopeEveryone); !errors.Is(err, messages.ErrForbidden) {
		t.Fatalf("blocked chat for everyone: %v", err)
	}
	if _, err := f.svc.Pin(ctx, a, direct.ID, m.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
}

func TestDeletingAMessageRemovesItsPins(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g := f.pinGroup()
	shared := f.send(g.id, g.member, "x-1", "shared")
	own := f.send(g.id, g.member, "x-2", "own")
	gone := f.send(g.id, g.member, "x-3", "gone")
	for _, id := range []int64{shared.ID, gone.ID} {
		if _, err := f.svc.Pin(ctx, g.mod, g.id, id, messages.ScopeEveryone); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Pin(ctx, g.member2, g.id, own.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(ctx, g.member2, g.id, shared.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(ctx, g.member2, g.id, own.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	if len(f.pinned(g.member2, g.id)) != 1 {
		t.Fatalf("delete for me left pins: %v", f.pinned(g.member2, g.id))
	}
	if _, ok := f.pinned(g.owner, g.id)[shared.ID]; !ok {
		t.Fatal("delete for me removed the pin for others")
	}
	if err := f.svc.Delete(ctx, g.member, g.id, gone.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.pinned(g.owner, g.id)[gone.ID]; ok {
		t.Fatal("pin of a deleted message")
	}
	if _, err := f.pg.MessagePins().SharedPin(ctx, g.id, gone.ID); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("the shared pin row is still stored: %v", err)
	}
	hidden := f.send(g.id, g.member, "x-4", "hidden first")
	if err := f.svc.Delete(ctx, g.member2, g.id, hidden.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Pin(ctx, g.mod, g.id, hidden.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.pinned(g.member2, g.id)[hidden.ID]; ok {
		t.Fatal("a message deleted for me shows as pinned")
	}
	if slices.Contains(f.rec.lastPin().recipients, g.member2) {
		t.Fatal("pin_update reached a member who deleted the message")
	}
}

func TestPinsArePagedNewestFirst(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.user("anna"), f.user("bert")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, a, b)
	var want []int64
	for i := range 5 {
		m := f.send(chat.ID, a, "pg-"+string(rune('a'+i)), "m")
		scope := messages.ScopeMe
		if i%2 == 0 {
			scope = messages.ScopeEveryone
		}
		if _, err := f.svc.Pin(ctx, a, chat.ID, m.ID, scope); err != nil {
			t.Fatal(err)
		}
		want = append([]int64{m.ID}, want...)
	}
	var got []int64
	var after *postgres.PinCursor
	for {
		page, err := f.svc.Pins(ctx, a, chat.ID, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Pins {
			got = append(got, p.Message.ID)
		}
		if page.Next == nil {
			break
		}
		after = page.Next
	}
	if !slices.Equal(got, want) {
		t.Fatalf("order: got %v, want %v", got, want)
	}
	if n := f.svc.PinCounts(ctx, a, []int64{chat.ID})[chat.ID]; n != 5 {
		t.Fatalf("count for anna: %d", n)
	}
	if n := f.svc.PinCounts(ctx, b, []int64{chat.ID})[chat.ID]; n != 3 {
		t.Fatalf("count for bert: %d", n)
	}
}
