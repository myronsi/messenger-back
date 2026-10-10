package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/chats"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func (c *chatEvents) GroupCreated(_ context.Context, chatID int64, v map[int64]chats.GroupView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for uid := range v {
		c.groupCreated[chatID] = append(c.groupCreated[chatID], uid)
	}
}

func (c *chatEvents) GroupUpdated(_ context.Context, chatID int64, v map[int64]chats.GroupView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.groupUpdates[chatID] += len(v)
}

// waitFor polls until cond holds (group events go out after the request).
func (c *chatEvents) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		ok := cond()
		c.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func roles(g map[string]any) map[string]string {
	out := map[string]string{}
	for _, m := range g["members"].([]any) {
		mm := m.(map[string]any)
		out[mm["user"].(map[string]any)["username"].(string)] = mm["role"].(string)
	}
	return out
}

func TestGroupsOverHTTP(t *testing.T) {
	e := newChatEnv(t)
	owner, bob, carol, dave, erin := e.register(t, "owner"), e.register(t, "bob"), e.register(t, "carol"), e.register(t, "dave"), e.register(t, "erin")
	bobID, carolID, daveID, erinID := e.uid(t, bob), e.uid(t, carol), e.uid(t, dave), e.uid(t, erin)
	e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: carol.access, body: map[string]any{"group_invites": "wait_approval"}})
	e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: dave.access, body: map[string]any{"group_invites": "nobody"}})

	// Anyone who refuses invitations fails the whole request.
	if r := e.do(t, request{method: http.MethodPost, path: "/groups", token: owner.access, body: map[string]any{"name": "Team", "member_ids": []string{bobID, daveID}}}); r.Code != http.StatusForbidden {
		t.Fatalf("group with a refusing member: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/groups", token: owner.access, body: map[string]any{"name": "  ", "member_ids": []string{}}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty name: %d", r.Code)
	}
	r := e.do(t, request{method: http.MethodPost, path: "/groups", token: owner.access, body: map[string]any{"name": " Team ", "description": "Our team", "member_ids": []string{bobID, carolID}}})
	if r.Code != http.StatusCreated || r.json()["name"] != "Team" || r.json()["my_role"] != "owner" {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	g := r.json()
	gid := g["id"].(string)
	if rs := roles(g); len(rs) != 2 || rs["owner"] != "owner" || rs["bob"] != "member" {
		t.Fatalf("members: %v", rs)
	}
	cid, _ := strconv.ParseInt(gid, 10, 64)
	e.events.waitFor(t, "group_created for owner and bob", func() bool { return len(e.events.groupCreated[cid]) == 2 })

	// Carol approves first: the invitation is in her inbox with the group's name.
	inbox := items(t, e.do(t, request{method: http.MethodGet, path: "/requests", token: carol.access}))
	if len(inbox) != 1 || inbox[0]["type"] != "group_invite" || inbox[0]["group_name"] != "Team" {
		t.Fatalf("invitation: %v", inbox)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/groups/" + gid, token: carol.access}); r.Code != http.StatusNotFound {
		t.Fatalf("invitee before accepting: %d", r.Code)
	}
	if ok := e.do(t, request{method: http.MethodPost, path: "/requests/" + inbox[0]["id"].(string) + "/approve", token: carol.access}); ok.Code != http.StatusOK || ok.json()["type"] != "group" || ok.json()["my_role"] != "member" {
		t.Fatalf("accept: %d %s", ok.Code, ok.Body)
	}
	e.events.waitFor(t, "group_created for carol", func() bool { return len(e.events.groupCreated[cid]) == 3 })

	// Changing the group: owners and admins only.
	if r := e.do(t, request{method: http.MethodPatch, path: "/groups/" + gid, token: bob.access, body: map[string]any{"name": "Mine"}}); r.Code != http.StatusForbidden {
		t.Fatalf("member renames: %d", r.Code)
	}
	up := e.do(t, request{method: http.MethodPatch, path: "/groups/" + gid, token: owner.access, body: map[string]any{"name": "Crew", "description": nil}})
	if up.Code != http.StatusOK || up.json()["name"] != "Crew" || up.json()["description"] != nil {
		t.Fatalf("rename: %d %s", up.Code, up.Body)
	}

	// Roles: bob becomes an admin and can manage members, but not the owner.
	if r := e.do(t, request{method: http.MethodPatch, path: "/groups/" + gid + "/participants/" + bobID, token: owner.access, body: map[string]any{"role": "admin"}}); r.Code != http.StatusOK || roles(r.json())["bob"] != "admin" {
		t.Fatalf("make admin: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: "/groups/" + gid + "/participants/" + e.uid(t, owner), token: bob.access, body: map[string]any{"role": "member"}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("demote the owner: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/groups/" + gid + "/participants", token: bob.access, body: map[string]any{"user_id": erinID}}); r.Code != http.StatusOK || roles(r.json())["erin"] != "member" {
		t.Fatalf("add: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/groups/" + gid + "/participants", token: bob.access, body: map[string]any{"user_id": erinID}}); r.Code != http.StatusConflict {
		t.Fatalf("add twice: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/groups/" + gid + "/participants", token: erin.access, body: map[string]any{"user_id": daveID}}); r.Code != http.StatusForbidden {
		t.Fatalf("member adds: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid + "/participants/" + erinID, token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("remove: %d", r.Code)
	}
	if len(e.events.removed[cid]) != 1 || e.events.removed[cid][0] != mustID(t, erinID) {
		t.Fatalf("chat_deleted for the removed member: %v", e.events.removed[cid])
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid + "/participants/" + e.uid(t, owner), token: bob.access}); r.Code != http.StatusForbidden {
		t.Fatalf("remove the owner: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid + "/participants/" + e.uid(t, owner), token: owner.access}); r.Code != http.StatusForbidden {
		t.Fatalf("the owner removes themselves: %d", r.Code)
	}
	// Outsiders learn nothing, whatever the invitee's settings.
	if r := e.do(t, request{method: http.MethodPost, path: "/groups/" + gid + "/participants", token: dave.access, body: map[string]any{"user_id": erinID}}); r.Code != http.StatusNotFound {
		t.Fatalf("outsider adds: %d", r.Code)
	}
	// An invitation lapses when its inviter can no longer add members.
	fay := e.register(t, "fay")
	e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: fay.access, body: map[string]any{"group_invites": "wait_approval"}})
	if r := e.do(t, request{method: http.MethodPost, path: "/groups/" + gid + "/participants", token: bob.access, body: map[string]any{"user_id": e.uid(t, fay)}}); r.Code != http.StatusOK {
		t.Fatalf("invite fay: %d %s", r.Code, r.Body)
	}
	e.do(t, request{method: http.MethodPatch, path: "/groups/" + gid + "/participants/" + bobID, token: owner.access, body: map[string]any{"role": "member"}})
	invite := items(t, e.do(t, request{method: http.MethodGet, path: "/requests", token: fay.access}))[0]["id"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: "/requests/" + invite + "/approve", token: fay.access}); r.Code != http.StatusConflict {
		t.Fatalf("accept a lapsed invitation: %d %s", r.Code, r.Body)
	}
	e.do(t, request{method: http.MethodPatch, path: "/groups/" + gid + "/participants/" + bobID, token: owner.access, body: map[string]any{"role": "admin"}})

	// The group avatar is an avatar upload of the caller.
	ownerID := mustID(t, e.uid(t, owner))
	av, err := e.store.Attachments().Create(context.Background(), postgres.NewAttachment{UploaderID: &ownerID, Purpose: "avatar", Kind: "image",
		Filename: "a.png", StorageKey: "avatars/" + uuid.NewString(), MimeType: "image/png", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/groups/" + gid + "/avatar", token: owner.access, body: map[string]any{"attachment_id": av.ID.String()}}); r.Code != http.StatusOK || r.json()["avatar_url"] == nil {
		t.Fatalf("group avatar: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/groups/" + gid + "/avatar", token: bob.access, body: map[string]any{"attachment_id": av.ID.String()}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("someone else's upload: %d", r.Code)
	}

	// Ownership moves; the old owner becomes an admin and may leave, the new one may not.
	tr := e.do(t, request{method: http.MethodPost, path: "/groups/" + gid + "/transfer-owner", token: owner.access, body: map[string]any{"user_id": bobID}})
	if tr.Code != http.StatusOK || tr.json()["owner_id"] != bobID || tr.json()["my_role"] != "admin" {
		t.Fatalf("transfer: %d %s", tr.Code, tr.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/groups/" + gid + "/leave", token: bob.access}); r.Code != http.StatusConflict {
		t.Fatalf("owner leaves: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/groups/" + gid + "/leave", token: owner.access}); r.Code != http.StatusNoContent {
		t.Fatalf("leave: %d", r.Code)
	}

	// The group list has groups only.
	e.create(t, bob, carolID, "")
	if gl := items(t, e.do(t, request{method: http.MethodGet, path: "/groups", token: bob.access})); len(gl) != 1 || gl[0]["type"] != "group" {
		t.Fatalf("group list: %v", gl)
	}

	if r := e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid, token: carol.access}); r.Code != http.StatusForbidden {
		t.Fatalf("member deletes: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid, token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/groups/" + gid, token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("deleted group: %d", r.Code)
	}
}

func mustID(t *testing.T, s string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
