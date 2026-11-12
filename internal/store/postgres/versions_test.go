package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestProfileVersionFollowsProfileChangesOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	if u.ProfileVersion != 0 {
		t.Fatalf("new user: %d", u.ProfileVersion)
	}
	name := "Alice B"
	patched, err := s.Users().PatchProfile(ctx, u.ID, ProfilePatch{DisplayName: &name})
	if err != nil || patched.ProfileVersion != 1 {
		t.Fatalf("display name: %d %v", patched.ProfileVersion, err)
	}
	if _, err := s.Users().PatchProfile(ctx, u.ID, ProfilePatch{DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	if err := s.Users().TouchLastSeen(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Users().Get(ctx, u.ID); got.ProfileVersion != 1 {
		t.Fatalf("same name and last seen bumped the version: %d", got.ProfileVersion)
	}
	a, err := s.Attachments().Create(ctx, NewAttachment{UploaderID: &u.ID, Purpose: PurposeAvatar, Kind: "image", StorageKey: "avatars/" + uuid.NewString(), MimeType: "image/png", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments().SetUserAvatar(ctx, u.ID, &a.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Users().Get(ctx, u.ID); got.ProfileVersion != 2 {
		t.Fatalf("avatar: %d", got.ProfileVersion)
	}
}

func TestStickerPackVersionFollowsPackChanges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	pack, err := s.Stickers().CreatePack(ctx, u.ID, "Cats", "cats"+strings.ReplaceAll(uuid.NewString(), "-", "")[:12], 10)
	if err != nil {
		t.Fatal(err)
	}
	version := func() int64 {
		t.Helper()
		p, err := s.Stickers().Pack(ctx, pack.ID)
		if err != nil {
			t.Fatal(err)
		}
		return p.Version
	}
	start := version()
	side := int32(512)
	upload := func() uuid.UUID {
		a, err := s.Attachments().Create(ctx, NewAttachment{UploaderID: &u.ID, Purpose: PurposeSticker, Kind: "image", StorageKey: "stickers/" + uuid.NewString(),
			MimeType: "image/webp", Size: 100, Width: &side, Height: &side})
		if err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	first, err := s.Stickers().AddSticker(ctx, u.ID, pack.ID, upload(), "🐱", 120)
	if err != nil {
		t.Fatal(err)
	}
	afterAdd := version()
	if afterAdd <= start {
		t.Fatalf("add: %d -> %d", start, afterAdd)
	}
	second, err := s.Stickers().AddSticker(ctx, u.ID, pack.ID, upload(), "😺", 120)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name string
		run  func() error
	}{
		{"emoji", func() error { return s.Stickers().SetStickerEmoji(ctx, u.ID, pack.ID, first, "🐶") }},
		{"order", func() error { return s.Stickers().ReorderStickers(ctx, u.ID, pack.ID, []int64{second, first}) }},
		{"rename", func() error { _, err := s.Stickers().RenamePack(ctx, u.ID, pack.ID, "Dogs"); return err }},
		{"delete", func() error { return s.Stickers().DeleteSticker(ctx, u.ID, pack.ID, first) }},
	}
	previous := version()
	for _, step := range steps {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		next := version()
		if next <= previous {
			t.Fatalf("%s did not bump the version: %d -> %d", step.name, previous, next)
		}
		previous = next
	}
}
