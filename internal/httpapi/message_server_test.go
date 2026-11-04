package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
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

func TestReplyPreviewOutsideThePageOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	chat := e.create(t, alice, e.uid(t, bob), "").json()["id"].(string)
	long := strings.Repeat("ä", 150)
	original := e.send(t, alice, chat, "o-1", long).json()["id"].(string)
	for i := range 3 {
		e.send(t, alice, chat, "f-"+strconv.Itoa(i), "filler")
	}
	r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: bob.access,
		body: map[string]any{"client_temp_id": "r-1", "type": "text", "content": "answer", "reply_to": original}})
	if r.Code != http.StatusCreated {
		t.Fatalf("reply: %d %s", r.Code, r.Body)
	}
	preview := func(s session) map[string]any {
		t.Helper()
		got, _ := e.history(t, s, chat, "?limit=2")
		p, _ := got[len(got)-1]["reply_preview"].(map[string]any)
		return p
	}

	p := preview(bob)
	if p == nil || p["id"] != original || p["content"] != strings.Repeat("ä", 100) || p["is_deleted"] != false ||
		p["sender"].(map[string]any)["username"] != "alice" {
		t.Fatalf("preview: %v", p)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/messages/" + original + "?scope=me", token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete for me: %d", r.Code)
	}
	if p := preview(bob); p != nil {
		t.Fatalf("a message deleted for bob still shows: %v", p)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/messages/" + original + "?scope=everyone", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete for everyone: %d", r.Code)
	}
	if p := preview(alice); p == nil || p["is_deleted"] != true || p["content"] != nil {
		t.Fatalf("deleted original: %v", p)
	}
}

func TestBulkForwardAndDeleteOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	chat := e.create(t, alice, e.uid(t, bob), "").json()["id"].(string)
	other := e.create(t, alice, e.uid(t, carol), "").json()["id"].(string)
	var mine []string
	for i, text := range []string{"one", "two", "three"} {
		mine = append(mine, e.send(t, alice, chat, "a-"+strconv.Itoa(i), text).json()["id"].(string))
	}
	bobs := e.send(t, bob, chat, "b-1", "from bob").json()["id"].(string)
	forward := func(s session, ids []string, targets ...string) reply {
		return e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages/forward", token: s.access,
			body: map[string]any{"message_ids": ids, "chat_ids": targets}})
	}
	remove := func(s session, scope string, ids []string) reply {
		return e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages/delete", token: s.access,
			body: map[string]any{"message_ids": ids, "scope": scope}})
	}

	fw := forward(alice, []string{mine[2], bobs, mine[0]}, other)
	if fw.Code != http.StatusCreated {
		t.Fatalf("forward: %d %s", fw.Code, fw.Body)
	}
	if got, _ := e.history(t, carol, other, ""); len(got) != 3 || texts(got)[0] != "one" || texts(got)[1] != "three" || texts(got)[2] != "from bob" {
		t.Fatalf("forwarded copies, oldest first: %v", texts(got))
	}
	tooMany := make([]string, 21)
	for i := range tooMany {
		tooMany[i] = strconv.Itoa(i + 1)
	}
	if r := forward(alice, tooMany, other); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("21 messages: %d", r.Code)
	}
	if r := forward(carol, []string{mine[0]}, other); r.Code != http.StatusNotFound {
		t.Fatalf("forward from a chat carol is not in: %d", r.Code)
	}

	if r := remove(bob, "everyone", []string{mine[0], bobs}); r.Code != http.StatusForbidden {
		t.Fatalf("bob deletes alice's message for everyone: %d", r.Code)
	}
	if got, _ := e.history(t, alice, chat, ""); texts(got)[3] != "from bob" {
		t.Fatalf("a refused bulk delete changed something: %v", texts(got))
	}
	if r := remove(alice, "everyone", mine[:2]); r.Code != http.StatusNoContent {
		t.Fatalf("delete own messages: %d %s", r.Code, r.Body)
	}
	if got, _ := e.history(t, bob, chat, ""); texts(got)[0] != "<deleted>" || texts(got)[1] != "<deleted>" || texts(got)[2] != "three" {
		t.Fatalf("after deleting for everyone: %v", texts(got))
	}
	if r := remove(alice, "everyone", mine[:2]); r.Code != http.StatusNoContent {
		t.Fatalf("a retry of a finished bulk delete: %d %s", r.Code, r.Body)
	}
	if r := remove(bob, "me", []string{mine[2], bobs}); r.Code != http.StatusNoContent {
		t.Fatalf("delete for me: %d", r.Code)
	}
	if got, _ := e.history(t, bob, chat, ""); len(got) != 2 {
		t.Fatalf("bob still sees: %v", texts(got))
	}
	if got, _ := e.history(t, alice, chat, ""); len(got) != 4 {
		t.Fatalf("alice lost messages bob deleted for himself: %v", texts(got))
	}
	if r := remove(alice, "me", append(tooMany, tooMany...)); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("repeated ids: %d", r.Code)
	}
}

type sendBudget struct{ charged map[string]int }

func (b *sendBudget) Allow(ctx context.Context, r redis.Rate, subject string) (redis.Result, error) {
	return b.AllowN(ctx, r, subject, 1)
}

func (b *sendBudget) AllowN(_ context.Context, r redis.Rate, _ string, n int) (redis.Result, error) {
	b.charged[r.Name] += n
	return redis.Result{Allowed: r.Name != RateSend.Name, RetryAfter: time.Second}, nil
}

func TestForwardChargesEveryCopy(t *testing.T) {
	if messages.MaxForwardCopies > RateSend.Burst || messages.MaxForwardTargets > RateSend.Burst {
		t.Fatalf("a forward allowed by the limits can never fit the send burst of %d", RateSend.Burst)
	}
	budget := &sendBudget{charged: map[string]int{}}
	srv := NewMessageServer(MessageOptions{Limiter: budget, BasePath: apiBase})
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = strconv.Itoa(i + 1)
		}
		return out
	}
	call := func(single bool, sources, targets int) int {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"message_ids": ids(sources), "chat_ids": ids(targets)})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), principalKey{}, auth.Principal{UserID: 7}))
		w := httptest.NewRecorder()
		if single {
			srv.ForwardMessage(w, req, "1", ForwardMessageParams{})
		} else {
			srv.ForwardMessages(w, req, "1", ForwardMessagesParams{})
		}
		return w.Code
	}

	if code := call(false, 4, 5); code != http.StatusTooManyRequests || budget.charged[RateSend.Name] != 20 {
		t.Fatalf("4 messages to 5 chats: %d, charged %d", code, budget.charged[RateSend.Name])
	}
	if code := call(false, 5, 5); code != http.StatusUnprocessableEntity || budget.charged[RateSend.Name] != 20 {
		t.Fatalf("25 copies: %d, charged %d", code, budget.charged[RateSend.Name])
	}
	if code := call(true, 1, 21); code != http.StatusUnprocessableEntity || budget.charged[RateSend.Name] != 20 {
		t.Fatalf("one message to 21 chats: %d, charged %d", code, budget.charged[RateSend.Name])
	}
	if code := call(true, 1, 3); code != http.StatusTooManyRequests || budget.charged[RateSend.Name] != 23 {
		t.Fatalf("one message to 3 chats: %d, charged %d", code, budget.charged[RateSend.Name])
	}
}

func TestEditClearsTheCaptionOfAFileOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID, _ := strconv.ParseInt(e.uid(t, alice), 10, 64)
	chat := e.create(t, alice, e.uid(t, bob), "").json()["id"].(string)
	a, err := e.store.Attachments().Create(context.Background(), postgres.NewAttachment{UploaderID: &aliceID, Purpose: "message", Kind: "image",
		Filename: "p.png", StorageKey: "attachments/" + uuid.NewString(), MimeType: "image/png", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	photo := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: alice.access,
		body: map[string]any{"client_temp_id": "p-1", "type": "file", "attachment_id": a.ID.String(), "content": "holiday"}})
	if photo.Code != http.StatusCreated {
		t.Fatalf("send a photo: %d %s", photo.Code, photo.Body)
	}
	cleared := e.do(t, request{method: http.MethodPatch, path: "/messages/" + photo.json()["id"].(string), token: alice.access, body: map[string]any{"content": ""}})
	if cleared.Code != http.StatusOK || cleared.json()["content"] != nil || cleared.json()["attachment"] == nil || cleared.json()["edited_at"] == nil {
		t.Fatalf("clear the caption: %d %s", cleared.Code, cleared.Body)
	}
	text := e.send(t, alice, chat, "text", "hello")
	if r := e.do(t, request{method: http.MethodPatch, path: "/messages/" + text.json()["id"].(string), token: alice.access, body: map[string]any{"content": ""}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty a text message: %d %s", r.Code, r.Body)
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

func TestSearchOverHTTP(t *testing.T) {
	e := newChatEnvWith(t, true)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	ab := e.create(t, alice, e.uid(t, bob), "").json()["id"].(string)
	ac := e.create(t, alice, e.uid(t, carol), "").json()["id"].(string)
	e.send(t, alice, ab, "s-1", "The quarterly report is ready")
	e.send(t, alice, ac, "s-2", "Report for carol")
	if err := e.index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	find := func(s session, path string) []map[string]any {
		t.Helper()
		return items(t, e.do(t, request{method: http.MethodGet, path: path, token: s.access}))
	}
	all := find(alice, "/search/messages?q=report")
	if len(all) != 2 {
		t.Fatalf("alice finds %d", len(all))
	}
	if h, _ := all[0]["highlight"].(string); !strings.Contains(h, "\ue000") {
		t.Fatalf("highlight: %q", h)
	}
	if got := find(bob, "/search/messages?q=report"); len(got) != 1 || got[0]["message"].(map[string]any)["chat_id"] != ab {
		t.Fatalf("bob: %v", got)
	}
	if got := find(alice, "/chats/"+ac+"/messages/search?q=report"); len(got) != 1 {
		t.Fatalf("in one chat: %v", got)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/chats/" + ac + "/messages/search?q=report", token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("a chat bob is not in: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/search/messages?q=r", token: bob.access}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("short query: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/search/messages?q=report&after=nope", token: bob.access}); r.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor: %d", r.Code)
	}
}
