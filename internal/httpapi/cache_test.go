package httpapi

import (
	"net/http"
	"testing"
)

func TestEtagMatches(t *testing.T) {
	for header, want := range map[string]bool{
		`"abc"`:             true,
		`W/"abc"`:           true,
		`"x", "abc"`:        true,
		`*`:                 true,
		`"abd"`:             false,
		`"x", W/"y"`:        false,
		`abc`:               false,
		` "abc" , "other" `: true,
	} {
		if got := etagMatches(header, `"abc"`); got != want {
			t.Errorf("%q: %v, want %v", header, got, want)
		}
	}
}

func TestCachedReadsAnswerNotModified(t *testing.T) {
	e := newChatEnv(t)
	seedPartyPack(t, e)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	bobID := e.uid(t, bob)
	group := e.do(t, request{method: http.MethodPost, path: "/groups", token: alice.access, body: map[string]any{"name": "Team", "member_ids": []string{bobID}}})
	if group.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", group.Code, group.Body)
	}
	packs := packItems(t, e.do(t, request{method: http.MethodGet, path: "/stickers/packs", token: alice.access}))

	for _, path := range []string{
		"/me",
		"/users/" + bobID,
		"/groups/" + group.json()["id"].(string),
		"/stickers/packs",
		"/stickers/packs/" + packs[0]["id"].(string),
	} {
		first := e.do(t, request{method: http.MethodGet, path: path, token: alice.access})
		etag := first.Header().Get("ETag")
		if first.Code != http.StatusOK || etag == "" || first.Header().Get("Cache-Control") != "private, no-cache" {
			t.Fatalf("%s: %d %q %q", path, first.Code, etag, first.Header().Get("Cache-Control"))
		}
		again := e.do(t, request{method: http.MethodGet, path: path, token: alice.access, header: map[string]string{"If-None-Match": etag}})
		if again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("ETag") != etag {
			t.Fatalf("%s revalidated: %d %q", path, again.Code, again.Body)
		}
		stale := e.do(t, request{method: http.MethodGet, path: path, token: alice.access, header: map[string]string{"If-None-Match": `"old"`}})
		if stale.Code != http.StatusOK || stale.Body.Len() == 0 {
			t.Fatalf("%s with an old tag: %d", path, stale.Code)
		}
	}

	me := e.do(t, request{method: http.MethodGet, path: "/me", token: alice.access})
	version := me.json()["profile_version"].(float64)
	patched := e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: map[string]any{"display_name": "Alice Cooper"}})
	if patched.Code != http.StatusOK || patched.json()["profile_version"].(float64) != version+1 {
		t.Fatalf("profile version after a change: %d %s", patched.Code, patched.Body)
	}
	changed := e.do(t, request{method: http.MethodGet, path: "/me", token: alice.access, header: map[string]string{"If-None-Match": me.Header().Get("ETag")}})
	if changed.Code != http.StatusOK || changed.Header().Get("ETag") == me.Header().Get("ETag") {
		t.Fatalf("changed profile: %d", changed.Code)
	}
	same := e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: map[string]any{"display_name": "Alice Cooper"}})
	if same.json()["profile_version"].(float64) != version+1 {
		t.Fatalf("an unchanged name bumped the version: %s", same.Body)
	}

	if list := e.do(t, request{method: http.MethodGet, path: "/chats", token: alice.access}); list.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("other routes stay uncached: %q", list.Header().Get("Cache-Control"))
	}
}

func TestAttachmentContentIsImmutable(t *testing.T) {
	e := newMediaEnv(t)
	alice := e.register(t, "alice")
	id := e.upload(t, alice.access, map[string]string{"purpose": "message"}, "x.png", smallPNG(t)).json()["id"].(string)
	original := e.do(t, request{method: http.MethodGet, path: "/attachments/" + id + "/content", token: alice.access})
	if original.Code != http.StatusOK || original.Header().Get("Cache-Control") != cacheForever {
		t.Fatalf("original: %d %q", original.Code, original.Header().Get("Cache-Control"))
	}
	thumb := e.do(t, request{method: http.MethodGet, path: "/attachments/" + id + "/content?variant=thumbnail", token: alice.access})
	if thumb.Code == http.StatusOK && thumb.Header().Get("Cache-Control") != "private, max-age=3600" {
		t.Fatalf("thumbnail: %q", thumb.Header().Get("Cache-Control"))
	}
}
