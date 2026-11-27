package httpapi

import (
	"net/http"
	"testing"
)

func TestChatImportOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	chat := e.group(t, alice, "Old friends", bob)
	bobID := e.uid(t, bob)
	start := func(s session) reply {
		return e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/imports", token: s.access, body: map[string]any{
			"source": "telegram", "message_count": 2, "file_count": 0,
			"first_at": "2020-01-01T10:00:00Z", "last_at": "2020-01-02T10:00:00Z",
			"senders": []map[string]any{
				{"key": "s1", "name": "Anna", "message_count": 1, "me": true},
				{"key": "s2", "name": "Bert", "message_count": 1, "user_id": bobID},
			},
		}})
	}
	if r := start(bob); r.Code != http.StatusForbidden {
		t.Fatalf("member starts an import: %d %s", r.Code, r.Body)
	}
	r := start(alice)
	if r.Code != http.StatusCreated || r.json()["state"] != "running" {
		t.Fatalf("start: %d %s", r.Code, r.Body)
	}
	id := r.json()["id"].(string)
	batch := map[string]any{"from_index": 0, "messages": []map[string]any{
		{"index": 0, "sender_key": "s1", "original_at": "2020-01-01T10:00:00Z", "type": "text", "content": "happy new year"},
		{"index": 1, "sender_key": "s2", "original_at": "2020-01-02T10:00:00Z", "type": "text", "content": "you too", "reply_to_index": 0},
	}}
	if r := e.do(t, request{method: http.MethodPut, path: "/imports/" + id + "/messages", token: bob.access, body: batch}); r.Code != http.StatusForbidden {
		t.Fatalf("bob adds messages: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/imports/" + id + "/messages", token: alice.access, body: batch}); r.Code != http.StatusOK || r.json()["next_index"] != 2.0 {
		t.Fatalf("batch: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/imports/" + id + "/finish", token: alice.access}); r.Code != http.StatusOK || r.json()["state"] != "done" {
		t.Fatalf("finish: %d %s", r.Code, r.Body)
	}

	history, _ := e.history(t, bob, chat, "")
	var imported []map[string]any
	for _, m := range history {
		if m["import"] != nil && m["type"] == "text" {
			imported = append(imported, m)
		}
	}
	if len(imported) != 2 {
		t.Fatalf("imported messages in history: %v", history)
	}
	first, second := imported[0], imported[1]
	if first["created_at"] != "2020-01-01T10:00:00Z" || first["sender"] == nil || first["import"].(map[string]any)["sender_state"] != "me" {
		t.Fatalf("own imported message: %v", first)
	}
	if second["sender"] != nil || second["import"].(map[string]any)["sender_name"] != "Bert" || second["reply_to"] != first["id"] {
		t.Fatalf("unconfirmed message: %v", second)
	}
	if history[0]["system_event"].(map[string]any)["kind"] != "history_imported" {
		t.Fatalf("history does not start with the import line: %v", history[0])
	}

	senders := func(s session) []any {
		t.Helper()
		list := items(t, e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/imports", token: s.access}))
		return list[0]["senders"].([]any)
	}
	if s := senders(bob)[1].(map[string]any); s["state"] != "pending" || s["user_id"] != bobID {
		t.Fatalf("bob's view of his name: %v", s)
	}
	decide := func(decision string) reply {
		return e.do(t, request{method: http.MethodPost, path: "/imports/" + id + "/senders/s2/decision", token: bob.access, body: map[string]any{"decision": decision}})
	}
	if r := decide("confirm"); r.Code != http.StatusOK || r.json()["state"] != "confirmed" {
		t.Fatalf("confirm: %d %s", r.Code, r.Body)
	}
	history, _ = e.history(t, alice, chat, "")
	for _, m := range history {
		if m["content"] == "you too" {
			if sender, _ := m["sender"].(map[string]any); sender == nil || sender["id"] != bobID {
				t.Fatalf("confirmed message sender: %v", m["sender"])
			}
		}
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/imports/" + id, token: bob.access}); r.Code != http.StatusForbidden {
		t.Fatalf("member removes the import: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/imports/" + id, token: alice.access}); r.Code != http.StatusAccepted {
		t.Fatalf("remove: %d %s", r.Code, r.Body)
	}
	history, _ = e.history(t, bob, chat, "")
	for _, m := range history {
		if m["import"] != nil {
			t.Fatalf("imported message after removal: %v", m)
		}
	}
}

func TestRunningImportStaysOutOfTheOtherMembersChatList(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	chat := e.group(t, alice, "Old friends", bob)
	r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/imports", token: alice.access, body: map[string]any{
		"source": "whatsapp", "message_count": 2, "file_count": 0,
		"first_at": "2020-01-01T10:00:00Z", "last_at": "2020-01-02T10:00:00Z",
		"senders": []map[string]any{
			{"key": "s1", "name": "Anna", "message_count": 1, "me": true},
			{"key": "s2", "name": "Bert", "message_count": 1},
		},
	}})
	if r.Code != http.StatusCreated {
		t.Fatalf("start: %d %s", r.Code, r.Body)
	}
	id := r.json()["id"].(string)
	batch := map[string]any{"from_index": 0, "messages": []map[string]any{
		{"index": 0, "sender_key": "s1", "original_at": "2020-01-01T10:00:00Z", "type": "text", "content": "secret one"},
		{"index": 1, "sender_key": "s2", "original_at": "2020-01-02T10:00:00Z", "type": "text", "content": "secret two"},
	}}
	if r := e.do(t, request{method: http.MethodPut, path: "/imports/" + id + "/messages", token: alice.access, body: batch}); r.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", r.Code, r.Body)
	}

	view := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat, token: bob.access}).json()
	list := items(t, e.do(t, request{method: http.MethodGet, path: "/chats?folder=main", token: bob.access}))
	for _, v := range append(list, view) {
		last, _ := v["last_message"].(map[string]any)
		if last != nil && (last["import"] != nil || last["content"] == "secret one" || last["content"] == "secret two") {
			t.Fatalf("running import shows as the last message: %v", last)
		}
		if v["unread_count"] != 0.0 || v["first_unread_message_id"] != nil {
			t.Fatalf("running import counts as unread: %v %v", v["unread_count"], v["first_unread_message_id"])
		}
	}
	if history, _ := e.history(t, bob, chat, ""); len(history) == 0 {
		t.Fatal("the group's own messages are gone")
	} else {
		for _, m := range history {
			if m["import"] != nil {
				t.Fatalf("running import in bob's history: %v", m)
			}
		}
	}
}

func TestFinishedImportIsNeverUnread(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	chat := e.group(t, alice, "Old friends", bob)
	r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/imports", token: alice.access, body: map[string]any{
		"source": "whatsapp", "message_count": 2, "file_count": 0,
		"first_at": "2020-01-01T10:00:00Z", "last_at": "2020-01-02T10:00:00Z",
		"senders": []map[string]any{
			{"key": "s1", "name": "Anna", "message_count": 1, "me": true},
			{"key": "s2", "name": "Bert", "message_count": 1},
		},
	}})
	if r.Code != http.StatusCreated {
		t.Fatalf("start: %d %s", r.Code, r.Body)
	}
	id := r.json()["id"].(string)
	batch := map[string]any{"from_index": 0, "messages": []map[string]any{
		{"index": 0, "sender_key": "s1", "original_at": "2020-01-01T10:00:00Z", "type": "text", "content": "old one"},
		{"index": 1, "sender_key": "s2", "original_at": "2020-01-02T10:00:00Z", "type": "text", "content": "old two"},
	}}
	if r := e.do(t, request{method: http.MethodPut, path: "/imports/" + id + "/messages", token: alice.access, body: batch}); r.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/imports/" + id + "/finish", token: alice.access}); r.Code != http.StatusOK {
		t.Fatalf("finish: %d %s", r.Code, r.Body)
	}
	view := func() map[string]any {
		return e.do(t, request{method: http.MethodGet, path: "/chats/" + chat, token: bob.access}).json()
	}
	if v := view(); v["unread_count"] != 0.0 || v["first_unread_message_id"] != nil {
		t.Fatalf("finished import counts as unread: %v %v", v["unread_count"], v["first_unread_message_id"])
	}
	live := e.send(t, alice, chat, "live-1", "new message").json()["id"]
	if v := view(); v["unread_count"] != 1.0 || v["first_unread_message_id"] != live {
		t.Fatalf("live message after the import: %v %v, want 1 %v", v["unread_count"], v["first_unread_message_id"], live)
	}
}
