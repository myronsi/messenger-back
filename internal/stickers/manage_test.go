package stickers_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/stickers"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/testenv"
)

func user(t *testing.T, store *postgres.Store, name string) int64 {
	t.Helper()
	u, err := store.Users().Register(t.Context(), name, name, "hash")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func stickerUpload(t *testing.T, store *postgres.Store, uploader int64, purpose string) uuid.UUID {
	t.Helper()
	side := int32(512)
	a, err := store.Attachments().Create(t.Context(), postgres.NewAttachment{
		UploaderID: &uploader, Purpose: purpose, Kind: "image", Filename: "sticker.webp",
		StorageKey: "stickers/" + uuid.NewString(), MimeType: "image/webp", Size: 100, Width: &side, Height: &side,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func openLink(t *testing.T, svc *stickers.Service, userID int64, p stickers.Pack) {
	t.Helper()
	if _, err := svc.PackByLink(t.Context(), userID, *p.Slug); err != nil {
		t.Fatal(err)
	}
}

func TestOwnPacks(t *testing.T) {
	ctx := t.Context()
	store := testenv.Postgres(t)
	svc := stickers.New(store.Stickers())
	alice, bob := user(t, store, "alice"), user(t, store, "bob")

	if _, err := svc.Create(ctx, alice, "  "); !errors.Is(err, stickers.ErrInvalid) {
		t.Fatalf("blank title: %v", err)
	}
	pack, err := svc.Create(ctx, alice, " Cats ")
	if err != nil {
		t.Fatal(err)
	}
	if pack.Title != "Cats" || !pack.Installed || pack.Slug == nil || len(*pack.Slug) != 12 || !stickers.IsMine(pack.StickerPack, alice) {
		t.Fatalf("new pack: %+v", pack)
	}
	panel, err := svc.Installed(ctx, alice)
	if err != nil || len(panel) != 1 || panel[0].ID != pack.ID {
		t.Fatalf("the new pack is in the owner's panel: %+v %v", panel, err)
	}
	if p, err := svc.Installed(ctx, bob); err != nil || len(p) != 0 {
		t.Fatalf("not in anybody else's: %+v %v", p, err)
	}

	first, err := svc.AddSticker(ctx, alice, pack.ID, stickerUpload(t, store, alice, "sticker"), "🐱")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.AddSticker(ctx, alice, pack.ID, stickerUpload(t, store, alice, "sticker"), "😺")
	if err != nil {
		t.Fatal(err)
	}
	for name, img := range map[string]uuid.UUID{
		"someone else's upload": stickerUpload(t, store, bob, "sticker"),
		"a message upload":      stickerUpload(t, store, alice, "message"),
		"no upload":             uuid.New(),
	} {
		if _, err := svc.AddSticker(ctx, alice, pack.ID, img, "🐱"); !errors.Is(err, stickers.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := svc.AddSticker(ctx, alice, pack.ID, stickerUpload(t, store, alice, "sticker"), "c a t"); !errors.Is(err, stickers.ErrInvalid) {
		t.Fatalf("not an emoji: %v", err)
	}
	if _, err := svc.AddSticker(ctx, bob, pack.ID, stickerUpload(t, store, bob, "sticker"), "🐶"); !errors.Is(err, stickers.ErrForbidden) {
		t.Fatalf("bob adds to alice's pack: %v", err)
	}

	if err := svc.Reorder(ctx, alice, pack.ID, []int64{second.ID}); !errors.Is(err, stickers.ErrInvalid) {
		t.Fatalf("a partial order: %v", err)
	}
	if err := svc.Reorder(ctx, alice, pack.ID, []int64{second.ID, second.ID}); !errors.Is(err, stickers.ErrInvalid) {
		t.Fatalf("a duplicate: %v", err)
	}
	if err := svc.Reorder(ctx, alice, pack.ID, []int64{second.ID, first.ID}); err != nil {
		t.Fatal(err)
	}
	if st, err := svc.SetEmoji(ctx, alice, pack.ID, first.ID, "😼"); err != nil || st.Emoji != "😼" {
		t.Fatalf("emoji: %+v %v", st, err)
	}
	if _, err := svc.Pack(ctx, bob, pack.ID); !errors.Is(err, stickers.ErrNotFound) {
		t.Fatalf("a user pack is not found by its ID without its link: %v", err)
	}
	if err := svc.SetInstalled(ctx, bob, pack.ID, true); !errors.Is(err, stickers.ErrNotFound) {
		t.Fatalf("nor added: %v", err)
	}
	if err := svc.Report(ctx, bob, pack.ID, "spam"); !errors.Is(err, stickers.ErrNotFound) {
		t.Fatalf("nor reported: %v", err)
	}
	got, err := svc.PackByLink(ctx, bob, *pack.Slug)
	if err != nil || got.Installed || len(got.Stickers) != 2 || got.Stickers[0].ID != second.ID || got.Stickers[1].Emoji != "😼" {
		t.Fatalf("bob opens the link: %+v %v", got, err)
	}
	if _, err := svc.PackByLink(ctx, bob, "AAAAAAAAAAAA"); !errors.Is(err, stickers.ErrNotFound) {
		t.Fatalf("unknown link: %v", err)
	}
	if _, err := svc.Rename(ctx, bob, pack.ID, "Mine"); !errors.Is(err, stickers.ErrForbidden) {
		t.Fatalf("bob renames: %v", err)
	}
	if p, err := svc.Rename(ctx, alice, pack.ID, "Cats 2"); err != nil || p.Title != "Cats 2" {
		t.Fatalf("rename: %+v %v", p, err)
	}

	if err := svc.DeleteSticker(ctx, alice, pack.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSticker(ctx, alice, pack.ID, first.ID); !errors.Is(err, stickers.ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
	found, err := store.Stickers().Stickers(ctx, []int64{first.ID})
	if err != nil || !found[first.ID].Deleted {
		t.Fatalf("a deleted sticker stays for sent messages: %+v %v", found, err)
	}

	owned, err := svc.Owned(ctx, alice)
	if err != nil || len(owned) != 1 || !owned[0].Installed || len(owned[0].Stickers) != 1 {
		t.Fatalf("owned: %+v %v", owned, err)
	}
	if err := svc.Delete(ctx, bob, pack.ID); !errors.Is(err, stickers.ErrForbidden) {
		t.Fatalf("bob deletes: %v", err)
	}
	if err := svc.Delete(ctx, alice, pack.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PackByLink(ctx, bob, *pack.Slug); !errors.Is(err, stickers.ErrNotFound) {
		t.Fatalf("the link of a deleted pack: %v", err)
	}
	if p, err := svc.Installed(ctx, alice); err != nil || len(p) != 0 {
		t.Fatalf("a deleted pack leaves the panel: %+v %v", p, err)
	}
}

func TestPackLimits(t *testing.T) {
	ctx := t.Context()
	store := testenv.Postgres(t)
	alice := user(t, store, "alice")
	repo := store.Stickers()
	p, err := repo.CreatePack(ctx, alice, "One", "AAAAAAAA1111", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePack(ctx, alice, "Two", "AAAAAAAA2222", 1); !errors.Is(err, postgres.ErrLimit) {
		t.Fatalf("second pack over the limit: %v", err)
	}
	if _, err := repo.AddSticker(ctx, alice, p.ID, stickerUpload(t, store, alice, "sticker"), "🙂", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddSticker(ctx, alice, p.ID, stickerUpload(t, store, alice, "sticker"), "🙂", 1); !errors.Is(err, postgres.ErrLimit) {
		t.Fatalf("second sticker over the limit: %v", err)
	}
	if _, err := repo.CreatePack(ctx, user(t, store, "bob"), "Taken", "AAAAAAAA1111", 1); !errors.Is(err, postgres.ErrConflict) {
		t.Fatalf("a taken link: %v", err)
	}
}

func TestReportsHideAPack(t *testing.T) {
	ctx := t.Context()
	store := testenv.Postgres(t)
	svc := stickers.New(store.Stickers())
	owner := user(t, store, "owner")
	pack, err := svc.Create(ctx, owner, "Spam")
	if err != nil {
		t.Fatal(err)
	}
	st, err := svc.AddSticker(ctx, owner, pack.ID, stickerUpload(t, store, owner, "sticker"), "🙂")
	if err != nil {
		t.Fatal(err)
	}
	fan := user(t, store, "fan")
	openLink(t, svc, fan, pack)
	if err := svc.SetInstalled(ctx, fan, pack.ID, true); err != nil {
		t.Fatal(err)
	}

	if err := svc.Report(ctx, owner, pack.ID, "spam"); !errors.Is(err, stickers.ErrForbidden) {
		t.Fatalf("the owner reports: %v", err)
	}
	if err := svc.Report(ctx, fan, pack.ID, "boring"); !errors.Is(err, stickers.ErrInvalid) {
		t.Fatalf("unknown reason: %v", err)
	}
	var reporters []int64
	for i := range stickers.HideAfterReports {
		reporters = append(reporters, user(t, store, fmt.Sprintf("reporter%d", i)))
		openLink(t, svc, reporters[i], pack)
	}
	for _, r := range reporters[:stickers.HideAfterReports-1] {
		for range 2 {
			if err := svc.Report(ctx, r, pack.ID, "spam"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if p, err := svc.Pack(ctx, fan, pack.ID); err != nil || p.Hidden {
		t.Fatalf("repeated reports of one user count once: %+v %v", p, err)
	}
	if err := svc.Report(ctx, reporters[len(reporters)-1], pack.ID, "hate"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Pack(ctx, fan, pack.ID); !errors.Is(err, stickers.ErrNotFound) {
		t.Fatalf("a hidden pack is gone for others: %v", err)
	}
	if p, err := svc.Installed(ctx, fan); err != nil || len(p) != 0 {
		t.Fatalf("and leaves their panel: %+v %v", p, err)
	}
	if err := svc.SetInstalled(ctx, fan, pack.ID, true); !errors.Is(err, stickers.ErrNotFound) {
		t.Fatalf("and cannot be added: %v", err)
	}
	mine, err := svc.Pack(ctx, owner, pack.ID)
	if err != nil || !mine.Hidden {
		t.Fatalf("the owner still sees it, marked hidden: %+v %v", mine, err)
	}
	found, err := store.Stickers().Stickers(ctx, []int64{st.ID})
	if err != nil || !found[st.ID].Deleted {
		t.Fatalf("nobody can send from a hidden pack: %+v %v", found, err)
	}

	if ok, err := svc.Unhide(ctx, pack.ID); err != nil || !ok {
		t.Fatalf("unhide: %v %v", ok, err)
	}
	if p, err := svc.Installed(ctx, fan); err != nil || len(p) != 1 {
		t.Fatalf("an unhidden pack is back in the panel: %+v %v", p, err)
	}
	if err := svc.Report(ctx, reporters[0], pack.ID, "spam"); err != nil {
		t.Fatal(err)
	}
	if p, err := svc.Pack(ctx, fan, pack.ID); err != nil || p.Hidden {
		t.Fatalf("old reports do not count after unhiding: %+v %v", p, err)
	}
}

func TestReportsAreLimited(t *testing.T) {
	ctx := t.Context()
	store := testenv.Postgres(t)
	svc := stickers.New(store.Stickers())
	reporter := user(t, store, "reporter")
	var packs []int64
	for i := range stickers.MaxReportsPerDay + 1 {
		owner := user(t, store, fmt.Sprintf("owner%d", i))
		p, err := svc.Create(ctx, owner, "Pack")
		if err != nil {
			t.Fatal(err)
		}
		packs = append(packs, p.ID)
		openLink(t, svc, reporter, p)
		err = svc.Report(ctx, reporter, p.ID, "other")
		if i < stickers.MaxReportsPerDay && err != nil {
			t.Fatal(err)
		}
		if i == stickers.MaxReportsPerDay && !errors.Is(err, stickers.ErrRateLimited) {
			t.Fatalf("report %d: %v", i+1, err)
		}
	}
	if err := svc.Report(ctx, reporter, packs[0], "spam"); err != nil {
		t.Fatalf("reporting an already reported pack again is no new report: %v", err)
	}
	if err := svc.Report(ctx, reporter, packs[len(packs)-1], "spam"); !errors.Is(err, stickers.ErrRateLimited) {
		t.Fatalf("the refused report was not stored: %v", err)
	}
}

func TestDeletedAccountDeletesItsPacks(t *testing.T) {
	ctx := context.Background()
	store := testenv.Postgres(t)
	svc := stickers.New(store.Stickers())
	owner, fan := user(t, store, "owner"), user(t, store, "fan")
	pack, err := svc.Create(ctx, owner, "Bye")
	if err != nil {
		t.Fatal(err)
	}
	openLink(t, svc, fan, pack)
	if err := svc.SetInstalled(ctx, fan, pack.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Users().DeleteAccount(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if p, err := svc.Installed(ctx, fan); err != nil || len(p) != 0 {
		t.Fatalf("packs of a deleted account: %+v %v", p, err)
	}
}
