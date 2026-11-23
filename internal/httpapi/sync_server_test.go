package httpapi

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"

	"github.com/myronsi/messenger-back/internal/auth"
)

func testSealer(t *testing.T) *auth.Sealer {
	t.Helper()
	s, err := auth.NewSealer(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *chatEnv) changes(t *testing.T, s session, cursor string) (changes []map[string]any, page map[string]any) {
	t.Helper()
	path := "/sync/changes"
	if cursor != "" {
		path += "?cursor=" + cursor
	}
	r := e.do(t, request{method: http.MethodGet, path: path, token: s.access})
	if r.Code != http.StatusOK {
		t.Fatalf("sync changes: %d %s", r.Code, r.Body)
	}
	page = r.json()
	for _, c := range page["changes"].([]any) {
		changes = append(changes, c.(map[string]any))
	}
	return changes, page
}

func (e *chatEnv) cursor(t *testing.T, s session) string {
	t.Helper()
	_, page := e.changes(t, s, "")
	return page["next_cursor"].(string)
}

func id64(t *testing.T, id string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSyncChangesOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	chat := e.group(t, alice, "Sync", bob)
	first := e.send(t, alice, chat, "s-1", "first").json()["id"].(string)
	second := e.send(t, alice, chat, "s-2", "second").json()["id"].(string)
	third := e.send(t, bob, chat, "s-3", "third").json()["id"].(string)

	start := e.cursor(t, bob)
	if got, page := e.changes(t, bob, start); len(got) != 0 || page["has_more"] != false || page["reset_required"] != false {
		t.Fatalf("nothing changed yet: %v %v", got, page)
	}

	if r := e.do(t, request{method: http.MethodPatch, path: "/messages/" + first, token: alice.access, body: map[string]any{"content": "first, edited"}}); r.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", r.Code, r.Body)
	}
	if err := e.msgs.React(t.Context(), id64(t, e.uid(t, bob)), id64(t, chat), id64(t, first), "👍", true); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/messages/" + second + "?scope=everyone", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete for everyone: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/messages/" + third + "?scope=me", token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete for me: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/chats/" + chat + "/messages/" + first + "/pin", token: alice.access, body: map[string]any{"scope": "everyone"}}); r.Code != http.StatusOK {
		t.Fatalf("pin: %d %s", r.Code, r.Body)
	}

	got, page := e.changes(t, bob, start)
	byType := map[string][]map[string]any{}
	for _, c := range got {
		if c["chat_id"] != chat {
			t.Fatalf("change of another chat: %v", c)
		}
		byType[c["type"].(string)] = append(byType[c["type"].(string)], c)
	}
	updated := byType["message_updated"]
	if len(updated) != 1 {
		t.Fatalf("edit and reaction of one message come as one entry: %v", got)
	}
	m := updated[0]["message"].(map[string]any)
	if m["id"] != first || m["content"] != "first, edited" || len(m["reactions"].([]any)) != 1 {
		t.Fatalf("current state of the message: %v", m)
	}
	deleted := map[string]string{}
	for _, c := range byType["message_deleted"] {
		deleted[c["message_id"].(string)] = c["scope"].(string)
	}
	if deleted[second] != "everyone" || deleted[third] != "me" || len(deleted) != 2 {
		t.Fatalf("deletions for bob: %v", deleted)
	}
	if len(byType["pins_changed"]) != 1 {
		t.Fatalf("pin change: %v", got)
	}
	next := page["next_cursor"].(string)
	if id64(t, next) <= id64(t, start) {
		t.Fatalf("cursor moves forward: %s after %s", next, start)
	}
	if again, _ := e.changes(t, bob, next); len(again) != 0 {
		t.Fatalf("nothing after the new cursor: %v", again)
	}

	aliceChanges, _ := e.changes(t, alice, start)
	for _, c := range aliceChanges {
		if c["type"] == "message_deleted" && c["message_id"] == third {
			t.Fatalf("bob's delete for himself reached alice: %v", c)
		}
	}
	if theirs, _ := e.changes(t, carol, start); len(theirs) != 0 {
		t.Fatalf("carol is not in the chat: %v", theirs)
	}
}

func TestSyncChangesPagesClearsAndResets(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	chat := e.group(t, alice, "Pages", bob)
	var sent []string
	for i := range 3 {
		sent = append(sent, e.send(t, alice, chat, "p-"+strconv.Itoa(i), "message "+strconv.Itoa(i)).json()["id"].(string))
	}
	start := e.cursor(t, bob)
	for _, id := range sent {
		if r := e.do(t, request{method: http.MethodPatch, path: "/messages/" + id, token: alice.access, body: map[string]any{"content": "edited"}}); r.Code != http.StatusOK {
			t.Fatalf("edit: %d %s", r.Code, r.Body)
		}
	}
	r := e.do(t, request{method: http.MethodGet, path: "/sync/changes?limit=2&cursor=" + start, token: bob.access})
	if r.Code != http.StatusOK || len(r.json()["changes"].([]any)) != 2 || r.json()["has_more"] != true {
		t.Fatalf("first page: %d %s", r.Code, r.Body)
	}
	rest, page := e.changes(t, bob, r.json()["next_cursor"].(string))
	if len(rest) != 1 || page["has_more"] != false {
		t.Fatalf("second page: %v %v", rest, page)
	}

	cleared := page["next_cursor"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/clear", token: bob.access, body: map[string]any{"scope": "me"}}); r.Code != http.StatusNoContent {
		t.Fatalf("clear for me: %d %s", r.Code, r.Body)
	}
	got, _ := e.changes(t, bob, cleared)
	if len(got) != 1 || got[0]["type"] != "history_cleared" || got[0]["scope"] != "me" || got[0]["before_message_id"] != sent[2] {
		t.Fatalf("bob's clear: %v", got)
	}
	if theirs, _ := e.changes(t, alice, cleared); len(theirs) != 0 {
		t.Fatalf("bob's clear reached alice: %v", theirs)
	}

	if _, err := e.store.Pool().Exec(t.Context(), `DELETE FROM sync_changes WHERE seq <= $1`, id64(t, cleared)+1); err != nil {
		t.Fatal(err)
	}
	if _, page := e.changes(t, bob, start); page["reset_required"] != true || page["next_cursor"] == "" {
		t.Fatalf("a cursor older than the log: %v", page)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/sync/changes?cursor=abc", token: bob.access}); r.Code != http.StatusBadRequest && r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad cursor: %d %s", r.Code, r.Body)
	}
}

func TestStorageKeyOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	alice := e.register(t, "alice")
	key := func(s session) reply {
		return e.do(t, request{method: http.MethodPost, path: "/me/storage-key", token: s.access})
	}
	r := key(alice)
	if r.Code != http.StatusOK || r.json()["algorithm"] != "A256GCM" || len(r.json()["key"].(string)) != 43 {
		t.Fatalf("storage key: %d %s", r.Code, r.Body)
	}
	first := r.json()["key"].(string)
	if again := key(alice).json()["key"]; again != first {
		t.Fatalf("the key stays the same: %v then %v", first, again)
	}
	login := e.login(t, "alice")
	other := session{access: login.json()["access_token"].(string)}
	if theirs := key(other).json()["key"]; theirs == first {
		t.Fatal("another session gets another key")
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/auth/logout", token: alice.access, cookie: alice.refresh}); r.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", r.Code, r.Body)
	}
	if r := key(alice); r.Code != http.StatusUnauthorized {
		t.Fatalf("a signed-out session gets no key: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/me/storage-key"}); r.Code != http.StatusUnauthorized {
		t.Fatalf("without a token: %d", r.Code)
	}
}
