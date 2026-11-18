package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"testing"
)

func TestRequestsFolder(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol, dave := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol"), e.register(t, "dave")
	aliceID, bobID, carolID := e.uid(t, alice), e.uid(t, bob), e.uid(t, carol)
	num := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	view := func(s session, chat string) map[string]any {
		t.Helper()
		return e.do(t, request{method: http.MethodGet, path: "/chats/" + chat, token: s.access}).json()
	}
	listIDs := func(s session, query string) []string {
		t.Helper()
		out := []string{}
		for _, c := range items(t, e.do(t, request{method: http.MethodGet, path: "/chats" + query, token: s.access})) {
			out = append(out, c["id"].(string))
		}
		return out
	}
	count := func(s session) float64 {
		t.Helper()
		r := e.do(t, request{method: http.MethodGet, path: "/chats/requests/count", token: s.access})
		if r.Code != http.StatusOK {
			t.Fatalf("count: %d %s", r.Code, r.Body)
		}
		return r.json()["requests_count"].(float64)
	}
	contact := func(subject, viewer string) bool {
		t.Helper()
		shared, err := e.store.Social().SharedChatViewers(t.Context(), num(subject), []int64{num(viewer)})
		if err != nil {
			t.Fatal(err)
		}
		partners, err := e.store.Social().SharedChatPartners(t.Context(), num(viewer), []int64{num(subject)})
		if err != nil {
			t.Fatal(err)
		}
		if shared[num(viewer)] != partners[num(subject)] {
			t.Fatalf("viewers and partners disagree for %s seen by %s", subject, viewer)
		}
		return shared[num(viewer)]
	}
	updates := func(chat string) int {
		e.events.mu.Lock()
		defer e.events.mu.Unlock()
		return len(e.events.updated[num(chat)])
	}

	opened := e.create(t, alice, bobID, "hi from a stranger")
	if opened.Code != http.StatusCreated || opened.json()["is_request"] != false {
		t.Fatalf("stranger opens a chat: %d %s", opened.Code, opened.Body)
	}
	chat := opened.json()["id"].(string)
	if v := view(bob, chat); v["is_request"] != true || v["unread_count"].(float64) != 1 {
		t.Fatalf("the recipient sees a request: %v", v)
	}
	if slices.Contains(listIDs(bob, ""), chat) || slices.Contains(listIDs(bob, "?folder=main"), chat) || !slices.Contains(listIDs(bob, "?folder=requests"), chat) {
		t.Fatal("a request is only in the Requests folder")
	}
	if !slices.Contains(listIDs(alice, ""), chat) || slices.Contains(listIDs(alice, "?folder=requests"), chat) {
		t.Fatal("the requester keeps the chat in the main list")
	}
	if b, a := count(bob), count(alice); b != 1 || a != 0 {
		t.Fatalf("request counts: bob %v alice %v", b, a)
	}
	if !slices.Contains(listIDs(bob, "?folder=all"), chat) || !slices.Contains(listIDs(alice, "?folder=all"), chat) {
		t.Fatal("folder=all lists requests and the main list together")
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/chats?folder=spam", token: bob.access}); r.Code != http.StatusBadRequest {
		t.Fatalf("unknown folder: %d %s", r.Code, r.Body)
	}

	first := view(bob, chat)["last_message"].(map[string]any)["id"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/read", token: bob.access, body: map[string]any{"message_id": first}}); r.Code != http.StatusOK {
		t.Fatalf("read: %d %s", r.Code, r.Body)
	}
	if v := view(bob, chat); v["is_request"] != true || count(bob) != 1 {
		t.Fatalf("reading keeps the request: %v", v)
	}
	if contact(bobID, aliceID) {
		t.Fatal("reading a request makes the requester a contact")
	}
	if v := view(alice, chat); len(v["last_message"].(map[string]any)["read_by"].([]any)) != 0 {
		t.Fatalf("the requester sees that the request was read: %v", v["last_message"])
	}

	before := updates(chat)
	if r := e.send(t, bob, chat, "reply", "hello back"); r.Code != http.StatusCreated {
		t.Fatalf("reply: %d %s", r.Code, r.Body)
	}
	if v := view(bob, chat); v["is_request"] != false || count(bob) != 0 || !slices.Contains(listIDs(bob, ""), chat) {
		t.Fatalf("a reply moves the chat out: %v", v)
	}
	if got := updates(chat); got != before+1 {
		t.Fatalf("chat_list_update for the reply: %d, before %d", got, before)
	}
	if !contact(bobID, aliceID) {
		t.Fatal("after the reply the requester is a contact")
	}

	group := e.do(t, request{method: http.MethodPost, path: "/groups", token: bob.access, body: map[string]any{"name": "Team", "member_ids": []string{carolID}}})
	if group.Code != http.StatusCreated {
		t.Fatalf("group: %d %s", group.Code, group.Body)
	}
	gid := group.json()["id"].(string)
	if v := view(carol, gid); v["is_request"] != false || !slices.Contains(listIDs(carol, ""), gid) {
		t.Fatalf("groups are never requests: %v", v)
	}
	fromContact := e.create(t, carol, bobID, "hi from the team")
	if fromContact.Code != http.StatusCreated {
		t.Fatalf("contact opens a chat: %d %s", fromContact.Code, fromContact.Body)
	}
	if v := view(bob, fromContact.json()["id"].(string)); v["is_request"] != false {
		t.Fatalf("a contact's chat lands in the main list: %v", v)
	}

	daveChat := e.create(t, dave, bobID, "hello").json()["id"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + daveChat + "/accept", token: carol.access}); r.Code != http.StatusNotFound {
		t.Fatalf("accept someone else's chat: %d", r.Code)
	}
	before = updates(daveChat)
	for range 2 {
		if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + daveChat + "/accept", token: bob.access}); r.Code != http.StatusNoContent {
			t.Fatalf("accept: %d %s", r.Code, r.Body)
		}
	}
	if v := view(bob, daveChat); v["is_request"] != false || count(bob) != 0 {
		t.Fatalf("accepted: %v", v)
	}
	if got := updates(daveChat); got != before+1 {
		t.Fatalf("chat_list_update once for the accept: %d, before %d", got, before)
	}

	eve, frank := e.register(t, "eve"), e.register(t, "frank")
	eveChat := e.create(t, eve, bobID, "pin me").json()["id"].(string)
	frankChat := e.create(t, frank, bobID, "archive me").json()["id"].(string)
	if n := count(bob); n != 2 {
		t.Fatalf("two new requests: %v", n)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/chats/" + eveChat + "/pin", token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("pin: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/chats/" + frankChat + "/archive", token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("archive: %d %s", r.Code, r.Body)
	}
	if view(bob, eveChat)["is_request"] != false || view(bob, frankChat)["is_request"] != false || count(bob) != 0 {
		t.Fatal("pinning or archiving moves a request out")
	}
}

func TestApprovedRequestIsNotARequest(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: bob.access, body: map[string]any{"direct_messages": "wait_approval"}}); r.Code != http.StatusOK {
		t.Fatalf("privacy: %d %s", r.Code, r.Body)
	}
	asked := e.create(t, alice, e.uid(t, bob), "may I?")
	if asked.Code != http.StatusAccepted {
		t.Fatalf("request: %d %s", asked.Code, asked.Body)
	}
	approved := e.do(t, request{method: http.MethodPost, path: "/requests/" + asked.json()["id"].(string) + "/approve", token: bob.access})
	if approved.Code != http.StatusOK || approved.json()["is_request"] != false {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body)
	}
}
