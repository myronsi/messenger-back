package calls

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestCallsFromContacts(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	alice, bob, carol := e.user("alice"), e.user("bob"), e.user("carol")
	setting := func(uid int64, v string) {
		t.Helper()
		if _, err := e.pg.Social().PatchPrivacy(ctx, uid, postgres.PrivacyPatch{CallsFrom: &v}); err != nil {
			t.Fatal(err)
		}
	}
	exception := func(owner, target int64, effect string) {
		t.Helper()
		if err := e.pg.Social().SetException(ctx, postgres.PrivacyException{OwnerID: owner, SettingKey: postgres.SettingCalls, TargetID: target, Effect: effect}); err != nil {
			t.Fatal(err)
		}
	}

	chat := e.direct(alice, bob)
	setting(bob, postgres.VisibleSharedChats)
	if _, _, err := e.svc.Start(ctx, alice, chat, uuid.New()); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("alice opened the chat but bob never took part: %v", err)
	}
	if _, err := e.pg.Chats().MarkRead(ctx, chat, bob, 1); err != nil {
		t.Fatal(err)
	}
	exception(bob, alice, postgres.EffectDeny)
	if _, _, err := e.svc.Start(ctx, alice, chat, uuid.New()); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("a deny exception wins over contacts: %v", err)
	}
	exception(bob, alice, postgres.EffectAllow)
	c := e.start(alice, chat)
	if find(&c, bob) == nil || find(&c, bob).State != postgres.CallParticipantRinging {
		t.Fatalf("bob is rung: %+v", c.Participants)
	}

	other := e.direct(carol, alice)
	setting(alice, postgres.VisibleNobody)
	exception(alice, carol, postgres.EffectAllow)
	if c := e.start(carol, other); find(&c, alice) == nil {
		t.Fatalf("an allow exception rings despite nobody: %+v", c.Participants)
	}
}

func TestGroupCallsRingOnlyWhoAllowsTheCaller(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	owner, added, open, quiet := e.user("owner"), e.user("added"), e.user("open"), e.user("quiet")
	setting := func(uid int64, v string) {
		t.Helper()
		if _, err := e.pg.Social().PatchPrivacy(ctx, uid, postgres.PrivacyPatch{CallsFrom: &v}); err != nil {
			t.Fatal(err)
		}
	}
	setting(added, postgres.VisibleSharedChats)
	setting(quiet, postgres.VisibleNobody)

	first := e.group(owner, added, open, quiet)
	c := e.start(owner, first)
	if find(&c, added) != nil || find(&c, quiet) != nil || find(&c, open) == nil {
		t.Fatalf("only open is rung: %+v", c.Participants)
	}
	e.join(added, e.session(added), c.ID)
	if c, _ := e.svc.Get(ctx, added, c.ID); find(&c, added) == nil || find(&c, added).State != postgres.CallParticipantJoined {
		t.Fatalf("a member who was not rung can still join: %+v", c.Participants)
	}

	second := e.group(owner, added, open, quiet)
	if err := e.pg.Chats().MarkEngaged(ctx, second, added); err != nil {
		t.Fatal(err)
	}
	if err := e.pg.Social().SetException(ctx, postgres.PrivacyException{OwnerID: quiet, SettingKey: postgres.SettingCalls, TargetID: owner, Effect: postgres.EffectAllow}); err != nil {
		t.Fatal(err)
	}
	c = e.start(owner, second)
	if find(&c, added) == nil || find(&c, quiet) == nil {
		t.Fatalf("added took part and quiet allows the owner: %+v", c.Participants)
	}
}
