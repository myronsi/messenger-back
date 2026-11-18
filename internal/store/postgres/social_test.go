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
	bad.DirectMessages = "everyone_except"
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
	if shared, _ := social.SharedChatPartners(ctx, bob.ID, []int64{alice.ID, carol.ID}); !shared[alice.ID] || shared[carol.ID] {
		t.Fatalf("alice opened the chat and counts as bob's contact: %v", shared)
	}
	if shared, _ := social.SharedChatPartners(ctx, alice.ID, []int64{bob.ID}); shared[bob.ID] {
		t.Fatalf("bob has not taken part in the chat alice opened: %v", shared)
	}
	if shared, _ := social.SharedChatViewers(ctx, bob.ID, []int64{alice.ID}); shared[alice.ID] {
		t.Fatalf("viewers of bob: %v", shared)
	}
	if _, err := s.Chats().MarkRead(ctx, chat.ID, bob.ID, 5); err != nil {
		t.Fatal(err)
	}
	if shared, _ := social.SharedChatPartners(ctx, alice.ID, []int64{bob.ID}); !shared[bob.ID] {
		t.Fatalf("bob read in the chat: %v", shared)
	}
	if shared, _ := social.SharedChatViewers(ctx, bob.ID, []int64{alice.ID, carol.ID}); !shared[alice.ID] || shared[carol.ID] {
		t.Fatalf("viewers of bob after reading: %v", shared)
	}
	group, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: carol.ID, Name: "g", MemberIDs: []int64{alice.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if shared, _ := social.SharedChatViewers(ctx, alice.ID, []int64{carol.ID}); shared[carol.ID] {
		t.Fatalf("being added to a group makes its owner a contact: %v", shared)
	}
	if shared, _ := social.SharedChatPartners(ctx, carol.ID, []int64{alice.ID}); shared[alice.ID] {
		t.Fatalf("partners of carol before alice took part: %v", shared)
	}
	if shared, _ := social.SharedChatViewers(ctx, carol.ID, []int64{alice.ID}); !shared[alice.ID] {
		t.Fatalf("carol created the group and counts alice as a contact: %v", shared)
	}
	if _, err := s.Chats().MarkRead(ctx, group.ID, alice.ID, 7); err != nil {
		t.Fatal(err)
	}
	if shared, _ := social.SharedChatViewers(ctx, alice.ID, []int64{carol.ID}); !shared[carol.ID] {
		t.Fatalf("alice read in the group: %v", shared)
	}
	if p, err := s.Chats().Participant(ctx, group.ID, alice.ID); err != nil || !p.Engaged {
		t.Fatalf("reading marks alice engaged: %+v %v", p, err)
	}

	dave := mustUser(t, s, "dave")
	other, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: carol.ID, Name: "h", MemberIDs: []int64{dave.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Chats().MarkEngaged(ctx, other.ID, dave.ID); err != nil {
		t.Fatal(err)
	}
	if shared, _ := social.SharedChatViewers(ctx, dave.ID, []int64{carol.ID}); !shared[carol.ID] {
		t.Fatalf("dave wrote in the group: %v", shared)
	}

	erin, frank := mustUser(t, s, "erin"), mustUser(t, s, "frank")
	invite, _, err := s.Chats().InviteAs(ctx, other.ID, carol.ID, erin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, joined, err := s.Approvals().ApproveInvite(ctx, invite.ID, erin.ID); err != nil || !joined {
		t.Fatalf("erin accepts: %v %v", joined, err)
	}
	if shared, _ := social.SharedChatViewers(ctx, erin.ID, []int64{carol.ID}); !shared[carol.ID] {
		t.Fatalf("accepting an invitation is taking part: %v", shared)
	}
	if err := s.Chats().AddMemberAs(ctx, other.ID, carol.ID, frank.ID); err != nil {
		t.Fatal(err)
	}
	if shared, _ := social.SharedChatViewers(ctx, frank.ID, []int64{carol.ID}); shared[carol.ID] {
		t.Fatalf("frank was only added: %v", shared)
	}
	if chats, _ := social.ChatIDs(ctx, bob.ID); len(chats) != 1 || chats[0] != chat.ID {
		t.Fatalf("chat ids: %v", chats)
	}
}

func TestExceptionKeysKeepTheirPredecessors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	owner, target := mustUser(t, s, "owner"), mustUser(t, s, "target")
	keys := []string{
		SettingAvatar, SettingProfile, SettingPresence, SettingLastSeen, SettingBirthday, SettingGroupInvites,
		SettingDirectMessages, SettingSearch, SettingVoiceVideo, SettingForwardLink,
	}
	for _, key := range keys {
		if err := s.Social().SetException(ctx, PrivacyException{OwnerID: owner.ID, SettingKey: key, TargetID: target.ID, Effect: EffectDeny}); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	if err := s.Social().SetException(ctx, PrivacyException{OwnerID: owner.ID, SettingKey: "unknown_setting", TargetID: target.ID, Effect: EffectDeny}); err == nil {
		t.Fatal("an unknown setting key was accepted")
	}
	got, err := s.Social().ExceptionsOf(ctx, owner.ID, []int64{target.ID})
	if err != nil || len(got[target.ID]) != len(keys) {
		t.Fatalf("exceptions: %v %v", got, err)
	}
}
