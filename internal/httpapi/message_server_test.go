package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func (e *chatEnv) send(t *testing.T, s session, chatID, temp, text string) reply {
	t.Helper()
	return e.do(t, request{method: http.MethodPost, path: "/chats/" + chatID + "/messages", token: s.access,
		body: map[string]any{"client_temp_id": temp, "type": "text", "content": text}})
}

func (e *chatEnv) history(t *testing.T, s session, chatID, query string) (items []map[string]any, page map[string]any) {
	t.Helper()
	r := e.do(t, request{method: http.MethodGet, path: "/chats/" + chatID + "/messages" + query, token: s.access})
	return items2(t, r), r.json()
}

func items2(t *testing.T, r reply) []map[string]any {
	t.Helper()
	return items(t, r)
}

func texts(items []map[string]any) []string {
	out := make([]string, len(items))
	for i, it := range items {
		if c, ok := it["content"].(string); ok {
			out[i] = c
		} else {
			out[i] = "<deleted>"
		}
	}
	return out
}

func TestMessagesOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	bobID, carolID := e.uid(t, bob), e.uid(t, carol)
	chat := e.create(t, alice, bobID, "").json()["id"].(string)
	other := e.create(t, alice, carolID, "").json()["id"].(string)

	// Sending is idempotent per client_temp_id.
	first := e.send(t, alice, chat, "t-1", "one")
	if first.Code != http.StatusCreated || first.json()["client_temp_id"] != "t-1" || first.json()["sender"].(map[string]any)["username"] != "alice" {
		t.Fatalf("send: %d %s", first.Code, first.Body)
	}
	if again := e.send(t, alice, chat, "t-1", "one"); again.Code != http.StatusOK || again.json()["id"] != first.json()["id"] {
		t.Fatalf("resend: %d %s", again.Code, again.Body)
	}
	for i := 2; i <= 5; i++ {
		e.send(t, alice, chat, "t-"+strconv.Itoa(i), []string{"", "", "two", "three", "four", "five"}[i])
	}
	if r := e.send(t, carol, chat, "c-1", "intruder"); r.Code != http.StatusNotFound {
		t.Fatalf("send by an outsider: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: alice.access,
		body: map[string]any{"client_temp_id": "t-x", "type": "text"}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("text without content: %d", r.Code)
	}

	// Pages run oldest to newest; next goes back in time, prev forward.
	got, page := e.history(t, bob, chat, "?limit=2")
	if want := []string{"four", "five"}; len(got) != 2 || texts(got)[0] != want[0] || texts(got)[1] != want[1] || page["next_cursor"] == nil || page["prev_cursor"] != nil {
		t.Fatalf("newest page: %v %v", texts(got), page)
	}
	older, page2 := e.history(t, bob, chat, "?limit=2&before="+page["next_cursor"].(string))
	if texts(older)[0] != "two" || texts(older)[1] != "three" || page2["prev_cursor"] == nil {
		t.Fatalf("older page: %v %v", texts(older), page2)
	}
	newer, _ := e.history(t, bob, chat, "?limit=10&after="+page2["prev_cursor"].(string))
	if len(newer) != 2 || texts(newer)[0] != "four" {
		t.Fatalf("after: %v", texts(newer))
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/messages?before=1&after=2", token: bob.access}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("two cursors: %d", r.Code)
	}

	// Read receipts show on the sender's messages up to bob's marker.
	all, _ := e.history(t, alice, chat, "")
	e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/read", token: bob.access, body: map[string]any{"message_id": all[2]["id"]}})
	all, _ = e.history(t, alice, chat, "")
	if len(all[2]["read_by"].([]any)) != 1 || len(all[3]["read_by"].([]any)) != 0 {
		t.Fatalf("read_by: %v / %v", all[2]["read_by"], all[3]["read_by"])
	}

	// Editing: only the sender.
	id1 := first.json()["id"].(string)
	if r := e.do(t, request{method: http.MethodPatch, path: "/messages/" + id1, token: bob.access, body: map[string]any{"content": "hacked"}}); r.Code != http.StatusForbidden {
		t.Fatalf("edit by another member: %d", r.Code)
	}
	ed := e.do(t, request{method: http.MethodPatch, path: "/messages/" + id1, token: alice.access, body: map[string]any{"content": "uno"}})
	if ed.Code != http.StatusOK || ed.json()["content"] != "uno" || ed.json()["edited_at"] == nil {
		t.Fatalf("edit: %d %s", ed.Code, ed.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: "/messages/" + id1, token: carol.access, body: map[string]any{"content": "x"}}); r.Code != http.StatusNotFound {
		t.Fatalf("edit by an outsider: %d", r.Code)
	}

	// Deleting for me hides it from bob only; for everyone leaves a tombstone.
	id2 := all[1]["id"].(string)
	if r := e.do(t, request{method: http.MethodDelete, path: "/messages/" + id2 + "?scope=me", token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete for me: %d %s", r.Code, r.Body)
	}
	if b, _ := e.history(t, bob, chat, ""); len(b) != 4 {
		t.Fatalf("bob still sees the hidden message: %v", texts(b))
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/messages/" + id1 + "?scope=everyone", token: bob.access}); r.Code != http.StatusForbidden {
		t.Fatalf("delete someone else's message in a direct chat: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/messages/" + id1 + "?scope=everyone", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete for everyone: %d", r.Code)
	}
	if a, _ := e.history(t, alice, chat, ""); a[0]["is_deleted"] != true || a[0]["content"] != nil {
		t.Fatalf("tombstone: %v", a[0])
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/messages/" + id1 + "?scope=all", token: alice.access}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown scope: %d", r.Code)
	}

	// Forwarding copies into chats the forwarder is in and names the original.
	src := all[4]["id"].(string)
	fw := e.do(t, request{method: http.MethodPost, path: "/messages/" + src + "/forward", token: alice.access, body: map[string]any{"chat_ids": []string{other}}})
	if fw.Code != http.StatusCreated {
		t.Fatalf("forward: %d %s", fw.Code, fw.Body)
	}
	copies := fw.json()["items"].([]any)
	c := copies[0].(map[string]any)
	if len(copies) != 1 || c["chat_id"] != other || c["content"] != "five" || c["forwarded_from"].(map[string]any)["message_id"] != src {
		t.Fatalf("copy: %v", c)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/messages/" + src + "/forward", token: bob.access, body: map[string]any{"chat_ids": []string{other}}}); r.Code != http.StatusNotFound {
		t.Fatalf("forward into a chat bob is not in: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/messages/" + src + "/forward", token: carol.access, body: map[string]any{"chat_ids": []string{other}}}); r.Code != http.StatusNotFound {
		t.Fatalf("forward a message carol cannot see: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/messages/" + src + "/forward", token: alice.access, body: map[string]any{"chat_ids": []string{other, other}}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("repeated targets: %d", r.Code)
	}
	// A forwarded copy shows the original sender's words: it cannot be edited.
	if r := e.do(t, request{method: http.MethodPatch, path: "/messages/" + c["id"].(string), token: alice.access, body: map[string]any{"content": "changed"}}); r.Code != http.StatusForbidden {
		t.Fatalf("edit a forwarded copy: %d", r.Code)
	}
	// around a message of another chat.
	if r := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/messages?around=" + c["id"].(string), token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("around a foreign message: %d %s", r.Code, r.Body)
	}
	// Bob blocks alice: his receipts disappear from her view.
	e.do(t, request{method: http.MethodPut, path: "/me/blocked-users/" + e.uid(t, alice), token: bob.access})
	if a, _ := e.history(t, alice, chat, ""); len(a[2]["read_by"].([]any)) != 0 {
		t.Fatalf("read_by of a user who blocked the viewer: %v", a[2]["read_by"])
	}
}

func TestChatMediaLists(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID, _ := strconv.ParseInt(e.uid(t, alice), 10, 64)
	chat := e.create(t, alice, e.uid(t, bob), "").json()["id"].(string)
	ctx := context.Background()
	var photos []string
	for i := range 3 {
		a, err := e.store.Attachments().Create(ctx, postgres.NewAttachment{UploaderID: &aliceID, Purpose: "message", Kind: "image",
			Filename: "p.png", StorageKey: "attachments/" + uuid.NewString(), MimeType: "image/png", Size: 1})
		if err != nil {
			t.Fatal(err)
		}
		r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: alice.access,
			body: map[string]any{"client_temp_id": "p-" + strconv.Itoa(i), "type": "file", "attachment_id": a.ID.String()}})
		if r.Code != http.StatusCreated || r.json()["attachment"] == nil {
			t.Fatalf("send a photo: %d %s", r.Code, r.Body)
		}
		photos = append(photos, r.json()["id"].(string))
	}
	e.send(t, alice, chat, "text", "not media")

	page := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/media?kind=image&limit=2", token: bob.access})
	got := items(t, page)
	if len(got) != 2 || got[0]["message"].(map[string]any)["id"] != photos[2] || page.json()["next_cursor"] == nil {
		t.Fatalf("first media page: %s", page.Body)
	}
	rest := items(t, e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/media?kind=image&after=" + page.json()["next_cursor"].(string), token: bob.access}))
	if len(rest) != 1 || rest[0]["message"].(map[string]any)["id"] != photos[0] {
		t.Fatalf("second media page: %v", rest)
	}
	// A photo deleted for bob drops out of his list.
	e.do(t, request{method: http.MethodDelete, path: "/messages/" + photos[2] + "?scope=me", token: bob.access})
	if left := items(t, e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/media?kind=image", token: bob.access})); len(left) != 2 {
		t.Fatalf("after hiding one: %d", len(left))
	}
	if audio := items(t, e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/media?kind=audio", token: bob.access})); len(audio) != 0 {
		t.Fatalf("audio: %v", audio)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/media?kind=video", token: bob.access}); r.Code != http.StatusUnprocessableEntity && r.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind: %d", r.Code)
	}
}
