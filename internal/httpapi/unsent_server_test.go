package httpapi

import (
	"net/http"
	"testing"
)

func TestUnsentMessagesOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	chat := e.create(t, alice, e.uid(t, bob), "").json()["id"].(string)
	unsent := func(s session) []map[string]any {
		t.Helper()
		return items(t, e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/unsent", token: s.access}))
	}

	e.do(t, request{method: http.MethodPut, path: "/me/blocked-users/" + e.uid(t, alice), token: bob.access})
	if r := e.send(t, alice, chat, "u-1", "are you there?"); r.Code != http.StatusForbidden {
		t.Fatalf("send to a user who blocked the sender: %d %s", r.Code, r.Body)
	}
	e.send(t, alice, chat, "u-2", "second try")
	kept := unsent(alice)
	if len(kept) != 2 || kept[0]["client_temp_id"] != "u-1" || kept[0]["content"] != "are you there?" || kept[0]["error_code"] != "blocked_by_user" {
		t.Fatalf("kept sends: %v", kept)
	}
	if len(kept[0]["attachments"].([]any)) != 0 || kept[0]["chat_id"] != chat {
		t.Fatalf("kept send fields: %v", kept[0])
	}
	if other := unsent(bob); len(other) != 0 {
		t.Fatalf("another member sees the sender's kept sends: %v", other)
	}

	if r := e.do(t, request{method: http.MethodDelete, path: "/chats/" + chat + "/unsent/u-2", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("discard: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/chats/" + chat + "/unsent/u-2", token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("discard twice: %d %s", r.Code, r.Body)
	}

	e.do(t, request{method: http.MethodDelete, path: "/me/blocked-users/" + e.uid(t, alice), token: bob.access})
	if r := e.send(t, alice, chat, "u-1", "are you there?"); r.Code != http.StatusCreated {
		t.Fatalf("resend after the block ended: %d %s", r.Code, r.Body)
	}
	if left := unsent(alice); len(left) != 0 {
		t.Fatalf("a delivered send stays kept: %v", left)
	}

	if r := e.send(t, alice, chat, "u-3", ""); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid send: %d %s", r.Code, r.Body)
	}
	if left := unsent(alice); len(left) != 0 {
		t.Fatalf("an invalid send is kept: %v", left)
	}
	stranger := e.register(t, "carol")
	if r := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/unsent", token: stranger.access}); r.Code != http.StatusNotFound {
		t.Fatalf("a non-member lists kept sends: %d", r.Code)
	}
}
