package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func TestContentReportEndpoints(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	bobID := e.uid(t, bob)
	if r := e.create(t, bob, e.uid(t, alice), "hi"); r.Code != http.StatusCreated && r.Code != http.StatusOK {
		t.Fatalf("bob opens a chat: %d %s", r.Code, r.Body)
	}
	group := e.do(t, request{method: http.MethodPost, path: "/groups", token: alice.access, body: map[string]any{"name": "Shop", "member_ids": []string{bobID}}})
	if group.Code != http.StatusCreated {
		t.Fatalf("group: %d %s", group.Code, group.Body)
	}
	groupID := group.json()["id"].(string)
	sent := e.send(t, alice, groupID, "t1", "cheap pills")
	if sent.Code != http.StatusCreated {
		t.Fatalf("send: %d %s", sent.Code, sent.Body)
	}
	msgID := sent.json()["id"].(string)
	path := "/chats/" + groupID + "/messages/" + msgID + "/report"
	report := func(s session, p string, body any) reply {
		return e.do(t, request{method: http.MethodPost, path: p, token: s.access, body: body})
	}
	if r := report(bob, path, map[string]any{"reason": "scam", "note": "fake medicine"}); r.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", r.Code, r.Body)
	}
	if r := report(bob, path, map[string]any{"reason": "illegal"}); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "note") {
		t.Fatalf("illegal without a note: %d %s", r.Code, r.Body)
	}
	if r := report(bob, path, map[string]any{"reason": "illegal", "note": "Sells prescription drugs without a licence."}); r.Code != http.StatusNoContent {
		t.Fatalf("illegal: %d %s", r.Code, r.Body)
	}
	if r := report(bob, path, map[string]any{"reason": "other", "note": strings.Repeat("n", 1001)}); r.Code != http.StatusBadRequest && r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("long note: %d %s", r.Code, r.Body)
	}
	if r := report(alice, path, map[string]any{"reason": "spam"}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("own message: %d %s", r.Code, r.Body)
	}
	if r := report(carol, path, map[string]any{"reason": "spam"}); r.Code != http.StatusNotFound {
		t.Fatalf("not a member: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: path, token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("withdraw: %d %s", r.Code, r.Body)
	}
	groupPath := "/groups/" + groupID + "/report"
	if r := report(bob, groupPath, map[string]any{"reason": "inappropriate_profile"}); r.Code != http.StatusNoContent {
		t.Fatalf("group report: %d %s", r.Code, r.Body)
	}
	if r := report(carol, groupPath, map[string]any{"reason": "spam"}); r.Code != http.StatusNotFound {
		t.Fatalf("group report by a stranger: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: groupPath, token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("withdraw group report: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: path, body: map[string]any{"reason": "spam"}}); r.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", r.Code, r.Body)
	}
}

func TestRemovedMessageShowsTheReasonOnlyToItsSender(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	chat := e.create(t, alice, e.uid(t, bob), "")
	if chat.Code != http.StatusCreated && chat.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", chat.Code, chat.Body)
	}
	chatID := chat.json()["id"].(string)
	sent := e.send(t, alice, chatID, "t1", "buy now")
	msgID := sent.json()["id"].(string)
	reply := e.do(t, request{method: http.MethodPost, path: "/chats/" + chatID + "/messages", token: bob.access,
		body: map[string]any{"client_temp_id": "t2", "type": "text", "content": "no thanks", "reply_to": msgID}})
	if reply.Code != http.StatusCreated {
		t.Fatalf("reply: %d %s", reply.Code, reply.Body)
	}
	if _, err := e.msgs.RemoveByModeration(context.Background(), mustID(t, chatID), mustID(t, msgID), scylla.ModerationRemoval{Code: "spam", Text: "Advertising"}); err != nil {
		t.Fatal(err)
	}
	find := func(s session) (removed, answer map[string]any) {
		items, _ := e.history(t, s, chatID, "")
		for _, m := range items {
			switch m["id"] {
			case msgID:
				removed = m
			case reply.json()["id"]:
				answer = m
			}
		}
		return removed, answer
	}
	mine, _ := find(alice)
	if mine["is_deleted"] != true || mine["removed_by_moderation"] != true || mine["content"] != nil {
		t.Fatalf("sender's view: %v", mine)
	}
	reason, _ := mine["moderation_reason"].(map[string]any)
	if reason["code"] != "spam" || reason["text"] != "Advertising" {
		t.Fatalf("sender's reason: %v", mine["moderation_reason"])
	}
	theirs, answer := find(bob)
	if theirs["removed_by_moderation"] != true {
		t.Fatalf("member's view: %v", theirs)
	}
	if _, ok := theirs["moderation_reason"]; ok {
		t.Fatalf("the reason leaked to another member: %v", theirs)
	}
	if answer == nil {
		t.Fatal("reply missing")
	}
	if p, _ := answer["reply_preview"].(map[string]any); p["is_deleted"] != true || p["removed_by_moderation"] != true {
		t.Fatalf("reply preview: %v", p)
	}
}
