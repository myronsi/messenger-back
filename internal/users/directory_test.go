package users_test

import (
	"context"
	"testing"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/testenv"
	"github.com/myronsi/messenger-back/internal/users"
)

func TestVisible(t *testing.T) {
	cases := []struct {
		setting, exception string
		shares, want       bool
	}{
		{postgres.VisibleEveryone, "", false, true},
		{postgres.VisibleEveryone, postgres.EffectDeny, false, true}, // exceptions only apply to *_except
		{postgres.VisibleSharedChats, "", false, false},
		{postgres.VisibleSharedChats, "", true, true},
		{postgres.VisibleEveryoneExcept, "", false, true},
		{postgres.VisibleEveryoneExcept, postgres.EffectDeny, true, false},
		{postgres.VisibleNobodyExcept, "", true, false},
		{postgres.VisibleNobodyExcept, postgres.EffectAllow, false, true},
		{postgres.VisibleNobody, postgres.EffectAllow, true, false},
		{"something new", "", true, false},
	}
	for _, c := range cases {
		if got := users.Visible(c.setting, c.exception, c.shares); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

type online map[int64]bool

func (o online) Online(_ context.Context, ids ...int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	for _, id := range ids {
		out[id] = o[id]
	}
	return out, nil
}

func TestDirectoryAppliesPrivacy(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	reg := func(name string) int64 {
		u, err := pg.Users().Register(ctx, name, name, "hash")
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	alice, bob, carol := reg("alice"), reg("bob"), reg("carol")
	avatar, bio := "avatars/alice.png", "hello"
	if _, err := pg.Users().UpdateProfile(ctx, alice, postgres.Profile{DisplayName: "Alice", AvatarURL: &avatar, Bio: &bio}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pg.Chats().CreateDirect(ctx, alice, bob); err != nil {
		t.Fatal(err)
	}
	p, _ := pg.Social().Privacy(ctx, []int64{alice})
	s := p[alice]
	s.AvatarVisibility = postgres.VisibleSharedChats
	s.ProfileVisibility = postgres.VisibleNobodyExcept
	s.PresenceVisibility = postgres.VisibleEveryoneExcept
	if _, err := pg.Social().UpdatePrivacy(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := pg.Social().SetException(ctx, postgres.PrivacyException{OwnerID: alice, SettingKey: postgres.SettingProfile, TargetID: carol, Effect: postgres.EffectAllow}); err != nil {
		t.Fatal(err)
	}
	if err := pg.Social().SetException(ctx, postgres.PrivacyException{OwnerID: alice, SettingKey: postgres.SettingPresence, TargetID: carol, Effect: postgres.EffectDeny}); err != nil {
		t.Fatal(err)
	}
	if err := pg.Social().SetContactName(ctx, bob, alice, "Ally"); err != nil {
		t.Fatal(err)
	}
	dir := users.NewDirectory(pg, online{alice: true}, "/api/v2")

	// Bob shares a chat: avatar yes, bio no (nobody except carol), presence yes, his contact name.
	asBob, err := dir.ForViewer(ctx, bob, []int64{alice, 999999})
	if err != nil {
		t.Fatal(err)
	}
	a := asBob[alice]
	if a.AvatarURL == nil || *a.AvatarURL != dir.AvatarURL(alice) || a.Bio != nil || !a.IsOnline || a.ContactName == nil || *a.ContactName != "Ally" {
		t.Fatalf("as bob: %+v", a)
	}
	if !asBob[999999].IsDeleted {
		t.Fatal("unknown user not shown as deleted")
	}
	// Carol shares no chat: no avatar, the bio (allowed), no presence (denied).
	asCarol, _ := dir.ForViewer(ctx, carol, []int64{alice})
	c := asCarol[alice]
	if c.AvatarURL != nil || c.Bio == nil || c.IsOnline || c.LastSeen != nil || c.ContactName != nil {
		t.Fatalf("as carol: %+v", c)
	}
	// Alice sees herself completely.
	self, _ := dir.ForViewer(ctx, alice, []int64{alice})
	if self[alice].AvatarURL == nil || self[alice].Bio == nil {
		t.Fatalf("self: %+v", self[alice])
	}
	// The per-viewer rendering of one subject agrees.
	many, err := dir.ToViewers(ctx, alice, []int64{bob, carol}, false)
	if err != nil || many[bob].AvatarURL == nil || many[carol].AvatarURL != nil || many[carol].Bio == nil || *many[bob].ContactName != "Ally" {
		t.Fatalf("to viewers: %+v %v", many, err)
	}
	vis, err := dir.PresenceVisibleTo(ctx, alice, []int64{bob, carol}, false)
	if err != nil || !vis[bob] || vis[carol] {
		t.Fatalf("presence visibility: %v %v", vis, err)
	}
}
