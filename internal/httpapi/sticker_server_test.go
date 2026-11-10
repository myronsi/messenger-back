package httpapi

import (
	"context"
	"io/fs"
	"net/http"
	"strconv"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/stickers"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func seedPartyPack(t *testing.T, e *chatEnv) {
	t.Helper()
	img, err := fs.ReadFile(stickers.Builtin(), "noto-animated/1f600.webp")
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{
		"party/pack.json": {Data: []byte(`{"key":"party","title":"Party","default":true,"stickers":[{"key":"a","emoji":"🎉"},{"key":"b","emoji":"👍"}]}`)},
		"party/a.webp":    {Data: img},
		"party/b.webp":    {Data: img},
	}
	deps := stickers.SeedDeps{
		Stickers: e.store.Stickers(), Attachments: e.store.Attachments(),
		Put: func(context.Context, string, []byte, string) error { return nil },
	}
	if err := stickers.Seed(t.Context(), deps, fsys); err != nil {
		t.Fatal(err)
	}
}

func packItems(t *testing.T, r reply) []map[string]any {
	t.Helper()
	if r.Code != http.StatusOK {
		t.Fatalf("packs: %d %s", r.Code, r.Body)
	}
	return items2(t, r)
}

func TestStickersOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	seedPartyPack(t, e)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	chat := e.create(t, alice, e.uid(t, bob), "").json()["id"].(string)

	packs := packItems(t, e.do(t, request{method: http.MethodGet, path: "/stickers/packs", token: alice.access}))
	if len(packs) != 1 || packs[0]["title"] != "Party" || packs[0]["is_installed"] != true || packs[0]["is_official"] != true {
		t.Fatalf("default pack: %v", packs)
	}
	packID := packs[0]["id"].(string)
	first := packs[0]["stickers"].([]any)[0].(map[string]any)
	stickerID := first["id"].(string)
	if first["emoji"] != "🎉" || first["is_animated"] != true || first["image"].(map[string]any)["content_type"] != "image/webp" {
		t.Fatalf("sticker: %v", first)
	}

	sent := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: alice.access,
		body: map[string]any{"client_temp_id": "st-1", "type": "sticker", "sticker_id": stickerID}})
	if sent.Code != http.StatusCreated {
		t.Fatalf("send sticker: %d %s", sent.Code, sent.Body)
	}
	msg := sent.json()
	st, _ := msg["sticker"].(map[string]any)
	att, _ := msg["attachment"].(map[string]any)
	if msg["type"] != "sticker" || msg["content"] != "🎉" || st == nil || st["id"] != stickerID || att == nil ||
		att["id"] != st["image"].(map[string]any)["id"] || att["kind"] != "image" {
		t.Fatalf("sent sticker message: %s", sent.Body)
	}

	history, _ := e.history(t, bob, chat, "")
	if len(history) != 1 || history[0]["sticker"].(map[string]any)["emoji"] != "🎉" {
		t.Fatalf("history: %v", history)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: "/messages/" + msg["id"].(string), token: alice.access, body: map[string]any{"content": "x"}}); r.Code != http.StatusForbidden {
		t.Fatalf("editing a sticker: %d %s", r.Code, r.Body)
	}

	for name, body := range map[string]map[string]any{
		"no sticker_id":     {"type": "sticker"},
		"with content":      {"type": "sticker", "sticker_id": stickerID, "content": "x"},
		"unknown sticker":   {"type": "sticker", "sticker_id": "999999"},
		"text with sticker": {"type": "text", "content": "x", "sticker_id": stickerID},
		"with attachment":   {"type": "sticker", "sticker_id": stickerID, "attachment_id": first["image"].(map[string]any)["id"]},
		"with album":        {"type": "sticker", "sticker_id": stickerID, "attachment_ids": []any{first["image"].(map[string]any)["id"], first["image"].(map[string]any)["id"]}},
	} {
		body["client_temp_id"] = "bad-" + name
		if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: alice.access, body: body}); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}

	recent := packItems(t, e.do(t, request{method: http.MethodGet, path: "/stickers/recent", token: alice.access}))
	if len(recent) != 1 || recent[0]["id"] != stickerID {
		t.Fatalf("recent: %v", recent)
	}
	if r := packItems(t, e.do(t, request{method: http.MethodGet, path: "/stickers/recent", token: bob.access})); len(r) != 0 {
		t.Fatalf("bob sent nothing: %v", r)
	}

	if r := e.do(t, request{method: http.MethodDelete, path: "/stickers/packs/" + packID + "/installation", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("remove pack: %d %s", r.Code, r.Body)
	}
	if p := packItems(t, e.do(t, request{method: http.MethodGet, path: "/stickers/packs", token: alice.access})); len(p) != 0 {
		t.Fatalf("removed default pack still listed: %v", p)
	}
	preview := e.do(t, request{method: http.MethodGet, path: "/stickers/packs/" + packID, token: alice.access})
	if preview.Code != http.StatusOK || preview.json()["is_installed"] != false || len(preview.json()["stickers"].([]any)) != 2 {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body)
	}
	if p := packItems(t, e.do(t, request{method: http.MethodGet, path: "/stickers/packs", token: bob.access})); len(p) != 1 {
		t.Fatalf("bob keeps the default pack: %v", p)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/stickers/packs/" + packID + "/installation", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("add pack: %d %s", r.Code, r.Body)
	}
	if p := packItems(t, e.do(t, request{method: http.MethodGet, path: "/stickers/packs", token: alice.access})); len(p) != 1 {
		t.Fatalf("added pack: %v", p)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/stickers/packs/999999", token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("unknown pack: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/stickers/packs/999999/installation", token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("add unknown pack: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/stickers/packs"}); r.Code != http.StatusUnauthorized {
		t.Fatalf("without a token: %d", r.Code)
	}

	media := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/media?kind=visual", token: alice.access})
	if media.Code != http.StatusOK || len(items2(t, media)) != 0 {
		t.Fatalf("stickers are not chat media: %d %s", media.Code, media.Body)
	}
}

func (e *chatEnv) stickerImage(t *testing.T, s session) string {
	t.Helper()
	uid, err := strconv.ParseInt(e.uid(t, s), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	side := int32(512)
	a, err := e.store.Attachments().Create(t.Context(), postgres.NewAttachment{
		UploaderID: &uid, Purpose: postgres.PurposeSticker, Kind: "image", Filename: "sticker.webp",
		StorageKey: "stickers/" + uuid.NewString(), MimeType: "image/webp", Size: 100, Width: &side, Height: &side,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID.String()
}

func TestOwnStickerPacksOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")

	created := e.do(t, request{method: http.MethodPost, path: "/stickers/packs", token: alice.access, body: map[string]any{"title": "Cats"}})
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	pack := created.json()
	packID, slug := pack["id"].(string), pack["slug"].(string)
	if pack["is_mine"] != true || pack["is_installed"] != true || pack["is_official"] != false || pack["is_hidden"] != false || pack["owner_id"] != e.uid(t, alice) {
		t.Fatalf("new pack: %v", pack)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/stickers/packs", token: alice.access, body: map[string]any{"title": ""}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty title: %d %s", r.Code, r.Body)
	}

	base := "/stickers/packs/" + packID
	add := func(s session, emoji string) reply {
		return e.do(t, request{method: http.MethodPost, path: base + "/stickers", token: s.access,
			body: map[string]any{"attachment_id": e.stickerImage(t, s), "emoji": emoji}})
	}
	first := add(alice, "🐱")
	if first.Code != http.StatusCreated || first.json()["emoji"] != "🐱" || first.json()["pack_id"] != packID {
		t.Fatalf("add: %d %s", first.Code, first.Body)
	}
	second := add(alice, "😺")
	if r := add(bob, "🐶"); r.Code != http.StatusForbidden {
		t.Fatalf("bob adds: %d %s", r.Code, r.Body)
	}
	firstID, secondID := first.json()["id"].(string), second.json()["id"].(string)

	if r := e.do(t, request{method: http.MethodPut, path: base + "/order", token: alice.access, body: map[string]any{"sticker_ids": []string{secondID}}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("partial order: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: base + "/order", token: alice.access, body: map[string]any{"sticker_ids": []string{secondID, firstID}}}); r.Code != http.StatusNoContent {
		t.Fatalf("order: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: base + "/stickers/" + firstID, token: alice.access, body: map[string]any{"emoji": "😼"}}); r.Code != http.StatusOK || r.json()["emoji"] != "😼" {
		t.Fatalf("emoji: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: base, token: bob.access, body: map[string]any{"title": "Mine"}}); r.Code != http.StatusForbidden {
		t.Fatalf("bob renames: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: base, token: alice.access, body: map[string]any{"title": "Cats 2"}}); r.Code != http.StatusOK || r.json()["title"] != "Cats 2" {
		t.Fatalf("rename: %d %s", r.Code, r.Body)
	}

	link := e.do(t, request{method: http.MethodGet, path: "/stickers/links/" + slug, token: bob.access})
	if link.Code != http.StatusOK || link.json()["is_mine"] != false || link.json()["is_installed"] != false {
		t.Fatalf("bob opens the link: %d %s", link.Code, link.Body)
	}
	got := link.json()["stickers"].([]any)
	if len(got) != 2 || got[0].(map[string]any)["id"] != secondID {
		t.Fatalf("order seen by bob: %v", got)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/stickers/links/AAAAAAAAAAAA", token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("unknown link: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPut, path: base + "/installation", token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("bob adds the pack: %d", r.Code)
	}
	chat := e.create(t, bob, e.uid(t, alice), "").json()["id"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: bob.access,
		body: map[string]any{"client_temp_id": "own-1", "type": "sticker", "sticker_id": firstID}}); r.Code != http.StatusCreated {
		t.Fatalf("bob sends alice's sticker: %d %s", r.Code, r.Body)
	}

	if r := e.do(t, request{method: http.MethodPost, path: base + "/report", token: alice.access, body: map[string]any{"reason": "spam"}}); r.Code != http.StatusForbidden {
		t.Fatalf("the owner reports: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: base + "/report", token: bob.access, body: map[string]any{"reason": "spam"}}); r.Code != http.StatusNoContent {
		t.Fatalf("bob reports: %d %s", r.Code, r.Body)
	}

	owned := packItems(t, e.do(t, request{method: http.MethodGet, path: "/stickers/packs?owned=true", token: alice.access}))
	if len(owned) != 1 || owned[0]["id"] != packID {
		t.Fatalf("owned: %v", owned)
	}
	if o := packItems(t, e.do(t, request{method: http.MethodGet, path: "/stickers/packs?owned=true", token: bob.access})); len(o) != 0 {
		t.Fatalf("bob owns nothing: %v", o)
	}

	if r := e.do(t, request{method: http.MethodDelete, path: base + "/stickers/" + firstID, token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete sticker: %d %s", r.Code, r.Body)
	}
	history, _ := e.history(t, bob, chat, "")
	if len(history) != 1 || history[0]["sticker"] == nil {
		t.Fatalf("a sent sticker stays after it leaves the pack: %v", history)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: base, token: bob.access}); r.Code != http.StatusForbidden {
		t.Fatalf("bob deletes: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: base, token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete pack: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/stickers/links/" + slug, token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("link of a deleted pack: %d", r.Code)
	}
}
