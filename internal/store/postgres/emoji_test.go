package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func emojiImage(t *testing.T, s *Store, uploader int64) uuid.UUID {
	t.Helper()
	a, err := s.Attachments().Create(context.Background(), NewAttachment{
		UploaderID: &uploader, Purpose: PurposeEmoji, Kind: "image", Filename: "e.png", StorageKey: "emoji/" + uuid.NewString(), MimeType: "image/png", Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func TestEmojiUsability(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	owner, fan, stranger, member := mustUser(t, s, "emoowner"), mustUser(t, s, "emofan"), mustUser(t, s, "emostranger"), mustUser(t, s, "emomember")
	pack, err := s.Emoji().CreatePack(ctx, owner.ID, "Cats", "abcdefgh1234")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := s.Emoji().AddEmoji(ctx, pack.ID, emojiImage(t, s, owner.ID), "cat", false, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Emoji().AddEmoji(ctx, pack.ID, emojiImage(t, s, owner.ID), "cat", false, 3); !errors.Is(err, ErrConflict) {
		t.Fatalf("a shortcode once per pack: %v", err)
	}
	group, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "Team", MemberIDs: []int64{member.ID}})
	if err != nil {
		t.Fatal(err)
	}
	gpack, err := s.Emoji().GroupPack(ctx, group.ID, "Team")
	if err != nil || gpack.ChatID == nil || *gpack.ChatID != group.ID {
		t.Fatalf("group pack %+v %v", gpack, err)
	}
	again, _ := s.Emoji().GroupPack(ctx, group.ID, "Team")
	if again.ID != gpack.ID {
		t.Fatal("one pack per group")
	}
	wave, err := s.Emoji().AddEmoji(ctx, gpack.ID, emojiImage(t, s, owner.ID), "wave", true, 3)
	if err != nil {
		t.Fatal(err)
	}

	usable := func(u User) map[int64]CustomEmoji {
		t.Helper()
		got, err := s.Emoji().Usable(ctx, u.ID, []int64{cat.ID, wave.ID})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := usable(owner); len(got) != 2 || got[wave.ID].Shortcode != "wave" || !got[wave.ID].IsAnimated {
		t.Fatalf("owner: %v", got)
	}
	if got := usable(stranger); len(got) != 0 {
		t.Fatalf("stranger: %v", got)
	}
	if got := usable(member); len(got) != 1 || got[wave.ID].ID == 0 {
		t.Fatalf("member uses group emoji only: %v", got)
	}
	if err := s.Emoji().SetInstalled(ctx, fan.ID, pack.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := usable(fan); len(got) != 1 || got[cat.ID].ID == 0 {
		t.Fatalf("installed: %v", got)
	}
	if err := s.Chats().RemoveMemberAs(ctx, group.ID, member.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if got := usable(member); len(got) != 0 {
		t.Fatalf("after leaving the group: %v", got)
	}

	packs, err := s.Emoji().UserPacks(ctx, owner.ID, 10)
	if err != nil || len(packs) != 2 || packs[0].ID != pack.ID || packs[1].ID != gpack.ID {
		t.Fatalf("owner packs %+v %v", packs, err)
	}
	if fanPacks, _ := s.Emoji().UserPacks(ctx, fan.ID, 10); len(fanPacks) != 1 || !fanPacks[0].Installed {
		t.Fatalf("fan packs %+v", fanPacks)
	}

	for i := range 3 {
		err := s.Emoji().Report(ctx, EmojiReport{PackID: pack.ID, ReporterID: []int64{fan.ID, stranger.ID, member.ID}[i], Reason: "spam", Since: time.Now().Add(-time.Hour), MaxReports: 5, HideAfter: 3})
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := usable(fan); len(got) != 0 {
		t.Fatalf("a hidden pack is the owner's only: %v", got)
	}
	if got := usable(owner); got[cat.ID].ID == 0 {
		t.Fatalf("the owner keeps it: %v", got)
	}

	if err := s.Emoji().DeleteEmoji(ctx, pack.ID, cat.ID); err != nil {
		t.Fatal(err)
	}
	img, err := s.Emoji().Emoji(ctx, cat.ID)
	if err != nil || !img.Deleted || img.PackSlug == nil {
		t.Fatalf("deleted emoji %+v %v", img, err)
	}
}

func TestEmojiPackLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "emolimit")
	pack, err := s.Emoji().CreatePack(ctx, u.ID, "Few", "limit0000001")
	if err != nil {
		t.Fatal(err)
	}
	for i, code := range []string{"one", "two"} {
		e, err := s.Emoji().AddEmoji(ctx, pack.ID, emojiImage(t, s, u.ID), code, false, 2)
		if err != nil || e.Position != i {
			t.Fatalf("%s: %+v %v", code, e, err)
		}
	}
	if _, err := s.Emoji().AddEmoji(ctx, pack.ID, emojiImage(t, s, u.ID), "three", false, 2); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit: %v", err)
	}
	before, _ := s.Emoji().Pack(ctx, pack.ID)
	if err := s.Emoji().RenamePack(ctx, pack.ID, "Many"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Emoji().Pack(ctx, pack.ID)
	if after.Title != "Many" || after.Version <= before.Version {
		t.Fatalf("rename grows the version: %+v -> %+v", before, after)
	}
}
