package httpapi

import (
	"net/http"
	"testing"
)

func TestPrivacySettingValues(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	patch := func(body map[string]any) reply {
		t.Helper()
		return e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: body})
	}

	p := e.do(t, request{method: http.MethodGet, path: "/me/privacy", token: alice.access}).json()
	if p["group_invites"] != "contacts" || p["calls_from"] != "everyone" || p["calls_enabled"] != true || p["direct_messages"] != "everyone" {
		t.Fatalf("defaults: %v", p)
	}
	if _, ok := p["exceptions"].(map[string]any)["calls_from"]; !ok {
		t.Fatalf("calls_from exceptions are listed: %v", p["exceptions"])
	}

	r := patch(map[string]any{"avatar_visibility": "everyone_except", "profile_visibility": "nobody_except", "group_invites": "nobody_except"})
	if r.Code != http.StatusOK || r.json()["avatar_visibility"] != "everyone" || r.json()["profile_visibility"] != "nobody" || r.json()["group_invites"] != "nobody" {
		t.Fatalf("deprecated *_except values: %d %s", r.Code, r.Body)
	}
	if r := patch(map[string]any{"direct_messages": "shared_chats"}); r.Code != http.StatusOK || r.json()["direct_messages"] != "contacts" {
		t.Fatalf("shared_chats is returned as contacts: %d %s", r.Code, r.Body)
	}
	for _, v := range []string{"contacts", "nobody", "wait_approval", "everyone"} {
		if r := patch(map[string]any{"direct_messages": v}); r.Code != http.StatusOK || r.json()["direct_messages"] != v {
			t.Fatalf("direct_messages %s: %d %s", v, r.Code, r.Body)
		}
	}

	if r := patch(map[string]any{"calls_enabled": false}); r.Code != http.StatusOK || r.json()["calls_from"] != "nobody" || r.json()["calls_enabled"] != false {
		t.Fatalf("calls_enabled false: %d %s", r.Code, r.Body)
	}
	if r := patch(map[string]any{"calls_enabled": true}); r.Code != http.StatusOK || r.json()["calls_from"] != "everyone" || r.json()["calls_enabled"] != true {
		t.Fatalf("calls_enabled true after nobody: %d %s", r.Code, r.Body)
	}
	if r := patch(map[string]any{"calls_from": "contacts"}); r.Code != http.StatusOK || r.json()["calls_from"] != "contacts" || r.json()["calls_enabled"] != true {
		t.Fatalf("calls_from contacts: %d %s", r.Code, r.Body)
	}
	if r := patch(map[string]any{"calls_enabled": true}); r.Code != http.StatusOK || r.json()["calls_from"] != "contacts" {
		t.Fatalf("calls_enabled true keeps contacts: %d %s", r.Code, r.Body)
	}
	if r := patch(map[string]any{"calls_enabled": false, "calls_from": "everyone"}); r.Code != http.StatusOK || r.json()["calls_from"] != "everyone" {
		t.Fatalf("calls_from wins over calls_enabled: %d %s", r.Code, r.Body)
	}
	if r := patch(map[string]any{"calls_from": "friends"}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown calls_from: %d", r.Code)
	}
	r = e.do(t, request{method: http.MethodPut, path: "/me/privacy/exceptions/calls_from/deny", token: alice.access, body: map[string]any{"user_ids": []string{e.uid(t, bob)}}})
	if r.Code != http.StatusOK {
		t.Fatalf("calls_from exceptions: %d %s", r.Code, r.Body)
	}
	if deny := r.json()["exceptions"].(map[string]any)["calls_from"].(map[string]any)["deny"].([]any); len(deny) != 1 || deny[0] != e.uid(t, bob) {
		t.Fatalf("calls_from deny list: %v", deny)
	}
}

func TestBeingAddedToAGroupIsNotContact(t *testing.T) {
	e := newChatEnv(t)
	mallory, bob, carol := e.register(t, "mallory"), e.register(t, "bob"), e.register(t, "carol")
	bobID, carolID := e.uid(t, bob), e.uid(t, carol)

	if r := e.do(t, request{method: http.MethodPost, path: "/groups", token: mallory.access, body: map[string]any{"name": "Spam", "member_ids": []string{carolID}}}); r.Code != http.StatusForbidden {
		t.Fatalf("new users only take group invites from contacts: %d %s", r.Code, r.Body)
	}

	e.openInvites(t, bob)
	if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: bob.access, body: map[string]any{"direct_messages": "contacts", "search_visibility": "nobody", "avatar_visibility": "contacts"}}); r.Code != http.StatusOK {
		t.Fatalf("bob's privacy: %d %s", r.Code, r.Body)
	}
	g := e.do(t, request{method: http.MethodPost, path: "/groups", token: mallory.access, body: map[string]any{"name": "Spam", "member_ids": []string{bobID}}})
	if g.Code != http.StatusCreated {
		t.Fatalf("group: %d %s", g.Code, g.Body)
	}
	gid := g.json()["id"].(string)

	if r := e.create(t, mallory, bobID, ""); r.Code != http.StatusForbidden {
		t.Fatalf("a group add opens bob's direct messages: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/users/" + bobID, token: mallory.access}); r.Code != http.StatusNotFound {
		t.Fatalf("a group add makes hidden bob findable: %d %s", r.Code, r.Body)
	}

	msg := e.send(t, mallory, gid, "m-1", "hi").json()["id"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + gid + "/read", token: bob.access, body: map[string]any{"message_id": msg}}); r.Code != http.StatusOK {
		t.Fatalf("bob reads: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/users/" + bobID, token: mallory.access}); r.Code != http.StatusOK {
		t.Fatalf("bob took part in the group: %d %s", r.Code, r.Body)
	}
	if r := e.create(t, mallory, bobID, ""); r.Code != http.StatusCreated {
		t.Fatalf("direct chat after bob took part: %d %s", r.Code, r.Body)
	}
}

func TestDirectMessagesFromNobody(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	carolID := e.uid(t, carol)
	if r := e.create(t, bob, carolID, ""); r.Code != http.StatusCreated {
		t.Fatalf("bob's chat with carol: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: carol.access, body: map[string]any{"direct_messages": "nobody"}}); r.Code != http.StatusOK {
		t.Fatalf("carol closes direct messages: %d %s", r.Code, r.Body)
	}
	if r := e.create(t, alice, carolID, "hello?"); r.Code != http.StatusForbidden {
		t.Fatalf("nobody refuses chats and requests: %d %s", r.Code, r.Body)
	}
	if inbox := items(t, e.do(t, request{method: http.MethodGet, path: "/requests", token: carol.access})); len(inbox) != 0 {
		t.Fatalf("nobody made a request: %v", inbox)
	}
	if r := e.create(t, bob, carolID, ""); r.Code != http.StatusOK {
		t.Fatalf("an existing chat stays: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/me/privacy/exceptions/direct_messages/allow", token: carol.access, body: map[string]any{"user_ids": []string{e.uid(t, alice)}}}); r.Code != http.StatusOK {
		t.Fatalf("allow alice: %d %s", r.Code, r.Body)
	}
	if r := e.create(t, alice, carolID, ""); r.Code != http.StatusCreated {
		t.Fatalf("an allow exception opens the chat: %d %s", r.Code, r.Body)
	}
}
