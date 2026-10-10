package postgres

import (
	"context"
	"errors"
	"testing"
)

func TestSocial(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	social := s.Social()
	alice, bob, carol := mustUser(t, s, "alice"), mustUser(t, s, "bob"), mustUser(t, s, "carol")

	p, err := social.Privacy(ctx, []int64{alice.ID, 999999})
	if err != nil || p[alice.ID] != DefaultPrivacy(alice.ID) || p[999999] != DefaultPrivacy(999999) {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	upd := DefaultPrivacy(alice.ID)
	upd.PresenceVisibility, upd.ReadReceiptsEnabled = VisibleNobody, false
	if got, err := social.UpdatePrivacy(ctx, upd); err != nil || got != upd {
		t.Fatalf("update: %+v %v", got, err)
	}
	bad := upd
	bad.DirectMessages = VisibleNobodyExcept // not allowed for this setting
	if _, err := social.UpdatePrivacy(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid value: %v", err)
	}

	if err := social.SetException(ctx, PrivacyException{OwnerID: alice.ID, SettingKey: SettingAvatar, TargetID: bob.ID, Effect: EffectDeny}); err != nil {
		t.Fatal(err)
	}
	if err := social.SetException(ctx, PrivacyException{OwnerID: alice.ID, SettingKey: SettingAvatar, TargetID: bob.ID, Effect: EffectAllow}); err != nil {
		t.Fatal(err)
	}
	of, _ := social.ExceptionsOf(ctx, alice.ID, []int64{bob.ID, carol.ID})
	if of[bob.ID][SettingAvatar] != EffectAllow || len(of[carol.ID]) != 0 {
		t.Fatalf("exceptions of: %v", of)
	}
	if fr, _ := social.ExceptionsFor(ctx, []int64{alice.ID, carol.ID}, bob.ID); fr[alice.ID][SettingAvatar] != EffectAllow {
		t.Fatalf("exceptions for: %v", fr)
	}
	if list, _ := social.Exceptions(ctx, alice.ID); len(list) != 1 {
		t.Fatalf("list: %v", list)
	}
	if err := social.DeleteException(ctx, alice.ID, SettingAvatar, bob.ID); err != nil {
		t.Fatal(err)
	}
	if err := social.DeleteException(ctx, alice.ID, SettingAvatar, bob.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}

	if blocked, _ := social.BlockedEither(ctx, alice.ID, bob.ID); blocked {
		t.Fatal("blocked from the start")
	}
	if err := social.Block(ctx, bob.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	if err := social.Block(ctx, bob.ID, alice.ID); err != nil { // idempotent
		t.Fatal(err)
	}
	if blocked, _ := social.BlockedEither(ctx, alice.ID, bob.ID); !blocked {
		t.Fatal("block not seen from the other side")
	}
	if list, _ := social.Blocked(ctx, bob.ID); len(list) != 1 || list[0].BlockedID != alice.ID {
		t.Fatalf("blocked list: %v", list)
	}
	if err := social.Block(ctx, bob.ID, bob.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blocking yourself: %v", err)
	}
	if err := social.Unblock(ctx, bob.ID, alice.ID); err != nil {
		t.Fatal(err)
	}

	if err := social.SetContactName(ctx, bob.ID, alice.ID, "Ally"); err != nil {
		t.Fatal(err)
	}
	if n, _ := social.ContactNames(ctx, bob.ID, []int64{alice.ID, carol.ID}); n[alice.ID] != "Ally" || len(n) != 1 {
		t.Fatalf("contact names: %v", n)
	}
	if n, _ := social.ContactNamesFor(ctx, []int64{bob.ID, carol.ID}, alice.ID); n[bob.ID] != "Ally" || len(n) != 1 {
		t.Fatalf("contact names for: %v", n)
	}
	if err := social.DeleteContactName(ctx, bob.ID, alice.ID); err != nil {
		t.Fatal(err)
	}

	chat, _, _ := s.Chats().CreateDirect(ctx, alice.ID, bob.ID)
	if shared, _ := social.SharedChatPartners(ctx, alice.ID, []int64{bob.ID, carol.ID}); !shared[bob.ID] || shared[carol.ID] {
		t.Fatalf("shared chats: %v", shared)
	}
	if chats, _ := social.ChatIDs(ctx, alice.ID); len(chats) != 1 || chats[0] != chat.ID {
		t.Fatalf("chat ids: %v", chats)
	}
}
