package users_test

import (
	"context"
	"testing"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/testenv"
	"github.com/myronsi/messenger-back/internal/users"
)

func TestVoiceVideoAndForwardLinkMatrix(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	social := pg.Social()
	reg := func(name string) int64 {
		u, err := pg.Users().Register(ctx, name, name, "hash")
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	alice, contact, stranger, allowed, denied, blocked := reg("alice"), reg("contact"), reg("stranger"), reg("allowed"), reg("denied"), reg("blocked")
	for _, other := range []int64{contact, denied, blocked} {
		if _, _, err := pg.Chats().CreateDirect(ctx, alice, other); err != nil {
			t.Fatal(err)
		}
	}
	if err := social.Block(ctx, alice, blocked); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{postgres.SettingVoiceVideo, postgres.SettingForwardLink} {
		if err := social.SetException(ctx, postgres.PrivacyException{OwnerID: alice, SettingKey: key, TargetID: allowed, Effect: postgres.EffectAllow}); err != nil {
			t.Fatal(err)
		}
		if err := social.SetException(ctx, postgres.PrivacyException{OwnerID: alice, SettingKey: key, TargetID: denied, Effect: postgres.EffectDeny}); err != nil {
			t.Fatal(err)
		}
	}
	viewers := []int64{alice, contact, stranger, allowed, denied, blocked}
	dir := users.NewDirectory(pg, online{}, "/api/v2")

	cases := []struct {
		key, value string
		want       map[int64]bool
	}{
		{postgres.SettingVoiceVideo, postgres.VisibleEveryone, map[int64]bool{alice: true, contact: true, stranger: true, allowed: true}},
		{postgres.SettingVoiceVideo, postgres.VisibleSharedChats, map[int64]bool{alice: true, contact: true, allowed: true}},
		{postgres.SettingForwardLink, postgres.VisibleEveryone, map[int64]bool{alice: true, contact: true, stranger: true, allowed: true}},
		{postgres.SettingForwardLink, postgres.VisibleSharedChats, map[int64]bool{alice: true, contact: true, allowed: true}},
		{postgres.SettingForwardLink, postgres.VisibleNobody, map[int64]bool{alice: true, allowed: true}},
	}
	for _, c := range cases {
		p, err := social.Privacy(ctx, []int64{alice})
		if err != nil {
			t.Fatal(err)
		}
		s := p[alice]
		s.VoiceVideoMessages, s.ForwardLink = postgres.VisibleEveryone, postgres.VisibleEveryone
		if c.key == postgres.SettingVoiceVideo {
			s.VoiceVideoMessages = c.value
		} else {
			s.ForwardLink = c.value
		}
		if _, err := social.UpdatePrivacy(ctx, s); err != nil {
			t.Fatalf("%s=%s: %v", c.key, c.value, err)
		}
		got, err := users.AllowedViewers(ctx, social, c.key, alice, viewers)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range viewers {
			if got[v] != c.want[v] {
				t.Errorf("%s=%s viewer %d: got %v, want %v", c.key, c.value, v, got[v], c.want[v])
			}
			one, err := users.AllowedSubjects(ctx, social, c.key, v, []int64{alice})
			if err != nil {
				t.Fatal(err)
			}
			if one[alice] != c.want[v] {
				t.Errorf("%s=%s subject view of %d: got %v, want %v", c.key, c.value, v, one[alice], c.want[v])
			}
			if c.key != postgres.SettingVoiceVideo {
				continue
			}
			peer, err := dir.VoiceVideoAllowed(ctx, v, []int64{alice})
			if err != nil {
				t.Fatal(err)
			}
			if peer[alice] != c.want[v] {
				t.Errorf("%s=%s peer flag for %d: got %v, want %v", c.key, c.value, v, peer[alice], c.want[v])
			}
		}
	}
}

func TestPrivacyRejectsUnknownVoiceVideoAndForwardValues(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	u, err := pg.Users().Register(ctx, "alice", "alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	bad := postgres.DefaultPrivacy(u.ID)
	bad.VoiceVideoMessages = postgres.VisibleNobody
	if _, err := pg.Social().UpdatePrivacy(ctx, bad); err == nil {
		t.Fatal("voice_video_messages accepted nobody")
	}
	bad = postgres.DefaultPrivacy(u.ID)
	bad.ForwardLink = postgres.WaitApproval
	if _, err := pg.Social().UpdatePrivacy(ctx, bad); err == nil {
		t.Fatal("forward_link accepted wait_approval")
	}
}
