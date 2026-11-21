package httpapi

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestPinnedMessagesOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	chat := e.group(t, alice, "Pins", bob, carol)
	msgID := e.send(t, carol, chat, "p-1", "meeting at five").json()["id"].(string)

	pin := func(s session, id, scope string) reply {
		return e.do(t, request{method: http.MethodPut, path: "/chats/" + chat + "/messages/" + id + "/pin", token: s.access, body: map[string]any{"scope": scope}})
	}
	unpin := func(s session, id, scope string) reply {
		return e.do(t, request{method: http.MethodDelete, path: "/chats/" + chat + "/messages/" + id + "/pin?scope=" + scope, token: s.access})
	}
	list := func(s session, query string) reply {
		return e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/pins" + query, token: s.access})
	}
	count := func(s session) float64 {
		t.Helper()
		for _, c := range items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: s.access})) {
			if c["id"] == chat {
				return c["pinned_message_count"].(float64)
			}
		}
		t.Fatal("chat not listed")
		return 0
	}

	if r := pin(bob, msgID, "everyone"); r.Code != http.StatusForbidden {
		t.Fatalf("member pins for everyone: %d %s", r.Code, r.Body)
	}
	if r := pin(bob, msgID, "me"); r.Code != http.StatusOK || r.json()["scope"] != "me" || r.json()["can_unpin_for_everyone"] != false {
		t.Fatalf("own pin: %d %s", r.Code, r.Body)
	}
	if count(bob) != 1 || count(carol) != 0 {
		t.Fatalf("counts after an own pin: bob %v, carol %v", count(bob), count(carol))
	}
	r := pin(alice, msgID, "everyone")
	if r.Code != http.StatusOK || r.json()["scope"] != "everyone" || r.json()["can_unpin_for_everyone"] != true {
		t.Fatalf("owner pins for everyone: %d %s", r.Code, r.Body)
	}
	if m := r.json()["message"].(map[string]any); m["content"] != "meeting at five" {
		t.Fatalf("pinned message: %v", m)
	}
	got := items(t, list(carol, ""))
	if len(got) != 1 || got[0]["scope"] != "everyone" || got[0]["can_unpin_for_everyone"] != false {
		t.Fatalf("carol's pins: %v", got)
	}
	if count(carol) != 1 || count(bob) != 1 {
		t.Fatalf("counts after the shared pin: bob %v, carol %v", count(bob), count(carol))
	}
	history, _ := e.history(t, carol, chat, "")
	var line map[string]any
	for _, m := range history {
		if ev, ok := m["system_event"].(map[string]any); ok && ev["kind"] == "message_pinned" {
			line = ev
		}
	}
	if line == nil || line["message_id"] != msgID || line["snippet"] != "meeting at five" || line["message_type"] != "text" {
		t.Fatalf("system line: %v", line)
	}

	bobID, _ := strconv.ParseInt(e.uid(t, bob), 10, 64)
	chatID, _ := strconv.ParseInt(chat, 10, 64)
	if err := e.store.Chats().SetRole(t.Context(), chatID, bobID, postgres.RoleModerator); err != nil {
		t.Fatal(err)
	}
	if r := unpin(bob, msgID, "everyone"); r.Code != http.StatusForbidden {
		t.Fatalf("moderator unpins the owner's pin: %d %s", r.Code, r.Body)
	}
	if r := unpin(carol, msgID, "me"); r.Code != http.StatusNoContent {
		t.Fatalf("hide: %d %s", r.Code, r.Body)
	}
	if got := items(t, list(carol, "")); len(got) != 0 {
		t.Fatalf("hidden pin listed: %v", got)
	}
	if r := unpin(alice, msgID, "everyone"); r.Code != http.StatusNoContent {
		t.Fatalf("owner unpins: %d %s", r.Code, r.Body)
	}
	if got := items(t, list(bob, "")); len(got) != 1 || got[0]["scope"] != "me" {
		t.Fatalf("bob's own pin is back: %v", got)
	}

	if r := unpin(alice, msgID, "all"); r.Code != http.StatusBadRequest {
		t.Fatalf("bad scope: %d %s", r.Code, r.Body)
	}
	if r := list(alice, "?after=nope"); r.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor: %d %s", r.Code, r.Body)
	}
	if r := pin(alice, history[0]["id"].(string), "me"); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("pinning a system message: %d %s", r.Code, r.Body)
	}
	second := e.send(t, carol, chat, "p-2", "second").json()["id"].(string)
	if r := pin(alice, second, "everyone"); r.Code != http.StatusOK {
		t.Fatalf("pin before clearing: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/clear", token: alice.access, body: map[string]any{"scope": "everyone"}}); r.Code != http.StatusNoContent {
		t.Fatalf("clear: %d %s", r.Code, r.Body)
	}
	for _, s := range []session{alice, bob, carol} {
		if got := items(t, list(s, "")); len(got) != 0 {
			t.Fatalf("pins after clearing for everyone: %v", got)
		}
	}
	dave := e.register(t, "dave")
	if r := list(dave, ""); r.Code != http.StatusNotFound {
		t.Fatalf("stranger lists pins: %d %s", r.Code, r.Body)
	}
}
