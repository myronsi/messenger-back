package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func messageIDs(list []map[string]any) []string {
	out := make([]string, len(list))
	for i, it := range list {
		out[i] = it["message"].(map[string]any)["id"].(string)
	}
	return out
}

func TestVoiceAndLinkMedia(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID, _ := strconv.ParseInt(e.uid(t, alice), 10, 64)
	chat := e.create(t, alice, e.uid(t, bob), "").json()["id"].(string)
	media := func(s session, kind string) []string {
		t.Helper()
		r := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/media?kind=" + kind, token: s.access})
		if r.Code != http.StatusOK {
			t.Fatalf("%s list: %d %s", kind, r.Code, r.Body)
		}
		return messageIDs(items(t, r))
	}

	voice, err := e.store.Attachments().Create(context.Background(), postgres.NewAttachment{UploaderID: &aliceID, Purpose: "message", Kind: "voice",
		Filename: "v.ogg", StorageKey: "attachments/" + uuid.NewString(), MimeType: "audio/ogg", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	vr := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: alice.access,
		body: map[string]any{"client_temp_id": "v-1", "type": "voice", "attachment_id": voice.ID.String()}})
	if vr.Code != http.StatusCreated {
		t.Fatalf("send voice: %d %s", vr.Code, vr.Body)
	}
	voiceID := vr.json()["id"].(string)
	if got := media(bob, "voice"); !slices.Equal(got, []string{voiceID}) {
		t.Fatalf("voice list: %v", got)
	}
	if got := media(bob, "audio"); !slices.Equal(got, []string{voiceID}) {
		t.Fatalf("audio still includes voice: %v", got)
	}

	first := e.send(t, alice, chat, "l-1", "look at https://example.com/a").json()["id"].(string)
	e.send(t, alice, chat, "l-2", "no link in here")
	second := e.send(t, bob, chat, "l-3", "and www.go.dev too").json()["id"].(string)
	if got := media(bob, "link"); !slices.Equal(got, []string{second, first}) {
		t.Fatalf("link list: %v", got)
	}
	page := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/media?kind=link&limit=1", token: bob.access})
	if got := messageIDs(items(t, page)); !slices.Equal(got, []string{second}) || page.json()["next_cursor"] == nil {
		t.Fatalf("first link page: %s", page.Body)
	}
	rest := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/media?kind=link&limit=1&after=" + page.json()["next_cursor"].(string), token: bob.access})
	if got := messageIDs(items(t, rest)); !slices.Equal(got, []string{first}) {
		t.Fatalf("second link page: %s", rest.Body)
	}

	if r := e.do(t, request{method: http.MethodPatch, path: "/messages/" + first, token: alice.access, body: map[string]any{"content": "link removed"}}); r.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", r.Code, r.Body)
	}
	if got := media(bob, "link"); !slices.Equal(got, []string{second}) {
		t.Fatalf("after removing the link by an edit: %v", got)
	}
	e.do(t, request{method: http.MethodPatch, path: "/messages/" + first, token: alice.access, body: map[string]any{"content": "back: http://x.org/page"}})
	if got := media(bob, "link"); !slices.Equal(got, []string{second, first}) {
		t.Fatalf("after adding a link by an edit: %v", got)
	}

	if r := e.do(t, request{method: http.MethodDelete, path: "/messages/" + second + "?scope=everyone", token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete for everyone: %d %s", r.Code, r.Body)
	}
	if got := media(alice, "link"); !slices.Equal(got, []string{first}) {
		t.Fatalf("after delete for everyone: %v", got)
	}
	e.do(t, request{method: http.MethodDelete, path: "/messages/" + first + "?scope=me", token: bob.access})
	if got := media(bob, "link"); len(got) != 0 {
		t.Fatalf("after delete for me: %v", got)
	}
	if got := media(alice, "link"); !slices.Equal(got, []string{first}) {
		t.Fatalf("delete for me changed alice's list: %v", got)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/clear", token: alice.access, body: map[string]any{"scope": "me"}}); r.Code != http.StatusNoContent {
		t.Fatalf("clear: %d %s", r.Code, r.Body)
	}
	if got := media(alice, "link"); len(got) != 0 {
		t.Fatalf("after clearing: %v", got)
	}
	if got := media(alice, "voice"); len(got) != 0 {
		t.Fatalf("voice after clearing: %v", got)
	}
}

func TestGroupMembersAndCommonGroups(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol, dave, eve := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol"), e.register(t, "dave"), e.register(t, "eve")
	bobID, carolID, daveID := e.uid(t, bob), e.uid(t, carol), e.uid(t, dave)
	group := func(name string, members ...string) string {
		t.Helper()
		r := e.do(t, request{method: http.MethodPost, path: "/groups", token: alice.access, body: map[string]any{"name": name, "member_ids": members}})
		if r.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, r.Code, r.Body)
		}
		return r.json()["id"].(string)
	}
	team := group("Team", daveID, carolID, bobID)
	if r := e.do(t, request{method: http.MethodPatch, path: "/groups/" + team + "/participants/" + bobID, token: alice.access, body: map[string]any{"role": "admin"}}); r.Code != http.StatusOK {
		t.Fatalf("make bob admin: %d %s", r.Code, r.Body)
	}
	pair := group("Pair", bobID)
	group("Other", carolID)

	names := func(r reply) []string {
		t.Helper()
		if r.Code != http.StatusOK {
			t.Fatalf("members: %d %s", r.Code, r.Body)
		}
		var out []string
		for _, it := range items(t, r) {
			out = append(out, it["user"].(map[string]any)["username"].(string)+":"+it["role"].(string))
		}
		return out
	}
	first := e.do(t, request{method: http.MethodGet, path: "/groups/" + team + "/participants?limit=2", token: carol.access})
	if got := names(first); !slices.Equal(got, []string{"alice:owner", "bob:admin"}) || first.json()["total"] != float64(4) || first.json()["next_cursor"] == nil {
		t.Fatalf("first member page: %s", first.Body)
	}
	second := e.do(t, request{method: http.MethodGet, path: "/groups/" + team + "/participants?limit=2&after=" + first.json()["next_cursor"].(string), token: carol.access})
	if got := names(second); !slices.Equal(got, []string{"carol:member", "dave:member"}) || second.json()["next_cursor"] != nil {
		t.Fatalf("second member page: %s", second.Body)
	}
	filtered := e.do(t, request{method: http.MethodGet, path: "/groups/" + team + "/participants?q=CAR", token: alice.access})
	if got := names(filtered); !slices.Equal(got, []string{"carol:member"}) || filtered.json()["total"] != float64(1) {
		t.Fatalf("filtered members: %s", filtered.Body)
	}
	byName := e.do(t, request{method: http.MethodGet, path: "/groups/" + team + "/participants?q=display%20d", token: alice.access})
	if got := names(byName); !slices.Equal(got, []string{"dave:member"}) {
		t.Fatalf("display name filter: %s", byName.Body)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/groups/" + team + "/participants", token: eve.access}); r.Code != http.StatusNotFound {
		t.Fatalf("stranger: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/groups/" + team + "/participants?after=not-a-cursor", token: alice.access}); r.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor: %d", r.Code)
	}

	common := e.do(t, request{method: http.MethodGet, path: "/users/" + bobID + "/common-groups", token: alice.access})
	if common.Code != http.StatusOK {
		t.Fatalf("common groups: %d %s", common.Code, common.Body)
	}
	got := items(t, common)
	if len(got) != 2 || got[0]["id"] != pair || got[1]["id"] != team || got[1]["member_count"] != float64(4) || got[0]["name"] != "Pair" {
		t.Fatalf("common groups: %s", common.Body)
	}
	paged := e.do(t, request{method: http.MethodGet, path: "/users/" + bobID + "/common-groups?limit=1", token: alice.access})
	if rows := items(t, paged); len(rows) != 1 || rows[0]["id"] != pair || paged.json()["next_cursor"] == nil {
		t.Fatalf("first common page: %s", paged.Body)
	}
	next := e.do(t, request{method: http.MethodGet, path: "/users/" + bobID + "/common-groups?limit=1&after=" + paged.json()["next_cursor"].(string), token: alice.access})
	if rows := items(t, next); len(rows) != 1 || rows[0]["id"] != team {
		t.Fatalf("second common page: %s", next.Body)
	}
	if rows := items(t, e.do(t, request{method: http.MethodGet, path: "/users/" + e.uid(t, alice) + "/common-groups", token: alice.access})); len(rows) != 0 {
		t.Fatalf("own id: %v", rows)
	}
	if rows := items(t, e.do(t, request{method: http.MethodGet, path: "/users/" + e.uid(t, eve) + "/common-groups", token: alice.access})); len(rows) != 0 {
		t.Fatalf("nothing shared: %v", rows)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/users/999999999/common-groups", token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d", r.Code)
	}
}
