package httpapi

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func (e *chatEnv) avatarUpload(t *testing.T, s session) string {
	t.Helper()
	uid, err := strconv.ParseInt(e.uid(t, s), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	side := int32(64)
	a, err := e.store.Attachments().Create(t.Context(), postgres.NewAttachment{
		UploaderID: &uid, Purpose: postgres.PurposeAvatar, Kind: "image", Filename: "me.png",
		StorageKey: "avatars/" + uuid.NewString(), MimeType: "image/png", Size: 100, Width: &side, Height: &side,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID.String()
}

func systemEvents(t *testing.T, e *chatEnv, s session, chatID string) []map[string]any {
	t.Helper()
	all, _ := e.history(t, s, chatID, "")
	var out []map[string]any
	for _, m := range all {
		if m["type"] == "system" {
			out = append(out, m["system_event"].(map[string]any))
		}
	}
	return out
}

func TestGroupSettingsPostSystemLines(t *testing.T) {
	e := newChatEnv(t)
	owner, bob := e.register(t, "owner"), e.register(t, "bob")
	bobID := e.uid(t, bob)
	gid := e.group(t, owner, "Team", bob)
	base := len(systemEvents(t, e, bob, gid))

	patch := func(name string) {
		t.Helper()
		if r := e.do(t, request{method: http.MethodPatch, path: "/groups/" + gid, token: owner.access, body: map[string]any{"name": name}}); r.Code != http.StatusOK {
			t.Fatalf("rename: %d %s", r.Code, r.Body)
		}
	}
	patch("Team")
	patch("Crew")
	avatar := e.avatarUpload(t, owner)
	if r := e.do(t, request{method: http.MethodPut, path: "/groups/" + gid + "/avatar", token: owner.access, body: map[string]any{"attachment_id": avatar}}); r.Code != http.StatusOK {
		t.Fatalf("set avatar: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid + "/avatar", token: owner.access}); r.Code != http.StatusOK {
		t.Fatalf("remove avatar: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid + "/avatar", token: owner.access}); r.Code != http.StatusOK {
		t.Fatalf("remove missing avatar: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid + "/avatar", token: bob.access}); r.Code != http.StatusForbidden {
		t.Fatalf("member removes avatar: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/groups/" + gid + "/transfer-owner", token: owner.access, body: map[string]any{"user_id": bobID}}); r.Code != http.StatusOK {
		t.Fatalf("transfer: %d %s", r.Code, r.Body)
	}

	events := systemEvents(t, e, bob, gid)[base:]
	if len(events) != 4 {
		t.Fatalf("want 4 new system lines, got %v", events)
	}
	if r := events[0]; r["kind"] != "group_renamed" || r["old_name"] != "Team" || r["new_name"] != "Crew" || r["actor_name"] != "Display owner" {
		t.Fatalf("group_renamed: %v", r)
	}
	if a := events[1]; a["kind"] != "group_avatar_changed" || a["avatar_removed"] != false {
		t.Fatalf("avatar set: %v", a)
	}
	if a := events[2]; a["kind"] != "group_avatar_changed" || a["avatar_removed"] != true {
		t.Fatalf("avatar removed: %v", a)
	}
	if o := events[3]; o["kind"] != "owner_transferred" || o["target_id"] != bobID || o["target_name"] != "Display bob" {
		t.Fatalf("owner_transferred: %v", o)
	}
}
