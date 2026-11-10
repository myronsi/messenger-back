package httpapi

import (
	"context"
	"io/fs"
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/myronsi/messenger-back/internal/stickers"
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
