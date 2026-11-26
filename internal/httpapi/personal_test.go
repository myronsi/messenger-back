package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type note struct {
	uid  int64
	kind string
	data any
}

type noteLog struct {
	mu    sync.Mutex
	notes []note
}

func (l *noteLog) NotifyUser(_ context.Context, uid int64, kind string, data any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.notes = append(l.notes, note{uid, kind, data})
}

func (l *noteLog) count(uid int64, kind string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, x := range l.notes {
		if x.uid == uid && x.kind == kind {
			n++
		}
	}
	return n
}

type testMedia struct {
	store  *postgres.Store
	mu     sync.Mutex
	denied map[uuid.UUID]bool
}

func (m *testMedia) deny(id uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.denied[id] = true
}

func (m *testMedia) Authorize(ctx context.Context, _ int64, id uuid.UUID) (postgres.Attachment, error) {
	m.mu.Lock()
	denied := m.denied[id]
	m.mu.Unlock()
	if denied {
		return postgres.Attachment{}, media.ErrNoAccess
	}
	return m.store.Attachments().Get(ctx, id)
}

func (e *chatEnv) uploadAs(t *testing.T, s session, purpose, kind string) string {
	t.Helper()
	uid, err := strconv.ParseInt(e.uid(t, s), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	side := int32(64)
	a, err := e.store.Attachments().Create(t.Context(), postgres.NewAttachment{
		UploaderID: &uid, Purpose: purpose, Kind: kind, Filename: "f",
		StorageKey: "test/" + uuid.NewString(), MimeType: "image/webp", Size: 100, Width: &side, Height: &side,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID.String()
}

func idOf(t *testing.T, s string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClientSettingsSync(t *testing.T) {
	e := newChatEnv(t)
	alice := e.register(t, "alice")
	aliceID := idOf(t, e.uid(t, alice))
	get := func() reply {
		return e.do(t, request{method: http.MethodGet, path: "/me/client-settings", token: alice.access})
	}
	patch := func(body any) reply {
		return e.do(t, request{method: http.MethodPatch, path: "/me/client-settings", token: alice.access, body: body})
	}

	r := get()
	if r.Code != http.StatusOK || r.json()["appearance"] != nil || r.json()["version"].(float64) != 0 || r.json()["updated_at"] != nil {
		t.Fatalf("fresh settings: %d %s", r.Code, r.Body)
	}
	appearance := map[string]any{"accent": "green", "font_size": 18, "density": "compact", "corners": "round"}
	r = patch(map[string]any{"appearance": appearance})
	if r.Code != http.StatusOK || r.json()["version"].(float64) != 1 || r.json()["appearance"].(map[string]any)["font_size"].(float64) != 18 {
		t.Fatalf("save appearance: %d %s", r.Code, r.Body)
	}
	r = patch(map[string]any{"recent_emoji": []string{"😀", "❤️", "😀"}, "recent_reactions": []string{"👍"}})
	got := r.json()
	if r.Code != http.StatusOK || got["version"].(float64) != 2 || len(got["recent_emoji"].([]any)) != 2 || got["appearance"].(map[string]any)["accent"] != "green" {
		t.Fatalf("recent emoji keep the appearance and drop duplicates: %d %s", r.Code, r.Body)
	}
	if n := e.notes.count(aliceID, EventClientSettingsUpdated); n != 2 {
		t.Fatalf("events: %d", n)
	}
	if r := get(); r.json()["version"].(float64) != 2 || r.json()["recent_reactions"].([]any)[0] != "👍" {
		t.Fatalf("stored settings: %s", r.Body)
	}

	for name, body := range map[string]any{
		"empty":       map[string]any{},
		"font size":   map[string]any{"appearance": map[string]any{"accent": "green", "font_size": 30, "density": "compact", "corners": "round"}},
		"accent":      map[string]any{"appearance": map[string]any{"accent": "Not Valid", "font_size": 16, "density": "compact", "corners": "round"}},
		"density":     map[string]any{"appearance": map[string]any{"accent": "blue", "font_size": 16, "density": "tight", "corners": "round"}},
		"too many":    map[string]any{"recent_reactions": []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}},
		"empty emoji": map[string]any{"recent_emoji": []string{""}},
	} {
		if r := patch(body); r.Code != http.StatusUnprocessableEntity && r.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, r.Code, r.Body)
		}
	}
}

func TestWallpapers(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	aliceID, bobID := e.uid(t, alice), e.uid(t, bob)
	chat := e.create(t, alice, bobID, "hi").json()["id"].(string)
	list := func(s session) []map[string]any {
		return items(t, e.do(t, request{method: http.MethodGet, path: "/me/wallpapers", token: s.access}))
	}
	setChat := func(s session, chatID string, body map[string]any) reply {
		return e.do(t, request{method: http.MethodPut, path: "/chats/" + chatID + "/wallpaper", token: s.access, body: body})
	}

	if r := e.do(t, request{method: http.MethodPut, path: "/me/wallpapers/default", token: alice.access, body: map[string]any{"kind": "preset", "preset": "dots"}}); r.Code != http.StatusOK || r.json()["chat_id"] != nil {
		t.Fatalf("default: %d %s", r.Code, r.Body)
	}
	photo := e.uploadAs(t, alice, postgres.PurposeChatWallpaper, "image")
	r := setChat(alice, chat, map[string]any{"kind": "media", "attachment_id": photo, "blur": 8, "dim": 30})
	wp := r.json()["wallpaper"].(map[string]any)
	if r.Code != http.StatusOK || r.json()["chat_id"] != chat || wp["blur"].(float64) != 8 || wp["attachment"].(map[string]any)["id"] != photo {
		t.Fatalf("own chat background: %d %s", r.Code, r.Body)
	}
	if got := list(alice); len(got) != 2 || got[0]["chat_id"] != nil || got[1]["chat_id"] != chat {
		t.Fatalf("alice's backgrounds: %v", got)
	}
	if got := list(bob); len(got) != 0 {
		t.Fatalf("only me leaks to bob: %v", got)
	}

	r = setChat(alice, chat, map[string]any{"kind": "color", "colors": []string{"#112233", "#445566"}, "pattern": "grid", "for_both": true})
	if r.Code != http.StatusOK || r.json()["set_by"] != aliceID {
		t.Fatalf("for both: %d %s", r.Code, r.Body)
	}
	got := list(bob)
	if len(got) != 1 || got[0]["chat_id"] != chat || got[0]["set_by"] != aliceID || got[0]["wallpaper"].(map[string]any)["pattern"] != "grid" {
		t.Fatalf("bob's copy: %v", got)
	}
	if e.notes.count(idOf(t, bobID), EventWallpaperUpdated) != 1 {
		t.Fatal("bob got no wallpaper_updated")
	}
	var line bool
	for _, ev := range systemEvents(t, e, bob, chat) {
		if ev["kind"] == "chat_wallpaper_set" && ev["actor_id"] == aliceID {
			line = true
		}
	}
	if !line {
		t.Fatal("no system line for a background for both")
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/chats/" + chat + "/wallpaper", token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("bob resets: %d %s", r.Code, r.Body)
	}
	if len(list(bob)) != 0 || len(list(alice)) != 2 {
		t.Fatal("a reset changed the other copy")
	}

	shared := e.uploadAs(t, alice, postgres.PurposeChatWallpaper, "image")
	r = setChat(alice, chat, map[string]any{"kind": "media", "attachment_id": shared, "for_both": true})
	if wp := r.json()["wallpaper"].(map[string]any); r.Code != http.StatusOK || wp["dim"] != nil || wp["blur"].(float64) != 0 {
		t.Fatalf("photo for both with automatic dim: %d %s", r.Code, r.Body)
	}
	used, err := e.store.Attachments().UsedAsWallpaperBy(t.Context(), uuid.MustParse(shared), idOf(t, bobID))
	if err != nil || !used {
		t.Fatalf("bob may load the shared photo: %v %v", used, err)
	}

	if r := setChat(bob, chat, map[string]any{"kind": "media", "attachment_id": photo}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("someone else's upload: %d %s", r.Code, r.Body)
	}
	avatar := e.uploadAs(t, alice, postgres.PurposeAvatar, "image")
	if r := setChat(alice, chat, map[string]any{"kind": "media", "attachment_id": avatar}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong purpose: %d %s", r.Code, r.Body)
	}
	if r := setChat(alice, chat, map[string]any{"kind": "preset", "preset": "dots", "blur": 3}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("extra fields: %d %s", r.Code, r.Body)
	}
	if r := setChat(carol, chat, map[string]any{"kind": "preset", "preset": "dots"}); r.Code != http.StatusNotFound {
		t.Fatalf("outsider: %d %s", r.Code, r.Body)
	}
	group := e.group(t, alice, "G", bob)
	if r := setChat(alice, group, map[string]any{"kind": "preset", "preset": "dots", "for_both": true}); r.Code != http.StatusForbidden {
		t.Fatalf("for both in a group: %d %s", r.Code, r.Body)
	}
	if r := setChat(alice, group, map[string]any{"kind": "preset", "preset": "dots"}); r.Code != http.StatusOK {
		t.Fatalf("own background in a group: %d %s", r.Code, r.Body)
	}
	saved := e.create(t, alice, aliceID, "").json()["id"].(string)
	if r := setChat(alice, saved, map[string]any{"kind": "preset", "preset": "dots", "for_both": true}); r.Code != http.StatusForbidden {
		t.Fatalf("for both in Saved Messages: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/me/blocked-users/" + aliceID, token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("block: %d %s", r.Code, r.Body)
	}
	if r := setChat(alice, chat, map[string]any{"kind": "preset", "preset": "dots", "for_both": true}); r.Code != http.StatusForbidden || r.json()["code"] != "blocked_by_user" {
		t.Fatalf("for both when blocked: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/me/wallpapers/default", token: alice.access}); r.Code != http.StatusNoContent || list(alice)[0]["chat_id"] == nil {
		t.Fatalf("delete default: %d %s", r.Code, r.Body)
	}
}

func TestPlaylists(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID := idOf(t, e.uid(t, alice))
	song1 := e.uploadAs(t, alice, postgres.PurposeMessage, "audio")
	song2 := e.uploadAs(t, alice, postgres.PurposeMessage, "audio")
	path := "/me/playlists"

	r := e.do(t, request{method: http.MethodPost, path: path, token: alice.access, body: map[string]any{
		"name": "  Road trip  ", "tracks": []map[string]any{{"attachment_id": song1, "title": "One", "chat_id": "5", "duration_seconds": 61.5}},
	}})
	if r.Code != http.StatusCreated || r.json()["name"] != "Road trip" || r.json()["track_count"].(float64) != 1 || r.json()["version"].(float64) != 1 {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	id := r.json()["id"].(string)
	track := r.json()["tracks"].([]any)[0].(map[string]any)
	if track["available"] != true || track["attachment"].(map[string]any)["id"] != song1 || track["chat_id"] != "5" {
		t.Fatalf("track: %v", track)
	}
	if got := items(t, e.do(t, request{method: http.MethodGet, path: path, token: alice.access})); len(got) != 1 || got[0]["id"] != id {
		t.Fatalf("list: %v", got)
	}
	if got := items(t, e.do(t, request{method: http.MethodGet, path: path, token: bob.access})); len(got) != 0 {
		t.Fatalf("bob sees alice's playlists: %v", got)
	}
	if r := e.do(t, request{method: http.MethodGet, path: path + "/" + id, token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("bob opens alice's playlist: %d", r.Code)
	}

	replace := func(version float64, tracks ...string) reply {
		list := []map[string]any{}
		for _, s := range tracks {
			list = append(list, map[string]any{"attachment_id": s, "title": "T " + s[:4]})
		}
		return e.do(t, request{method: http.MethodPut, path: path + "/" + id + "/tracks", token: alice.access, body: map[string]any{"version": version, "tracks": list}})
	}
	r = replace(1, song2, song1)
	if r.Code != http.StatusOK || r.json()["version"].(float64) != 2 || r.json()["tracks"].([]any)[0].(map[string]any)["attachment_id"] != song2 {
		t.Fatalf("reorder: %d %s", r.Code, r.Body)
	}
	if r := replace(1, song1); r.Code != http.StatusConflict || r.json()["code"] != "conflict" {
		t.Fatalf("stale version: %d %s", r.Code, r.Body)
	}
	if r := replace(2, song1, song1); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate: %d %s", r.Code, r.Body)
	}

	e.media.deny(uuid.MustParse(song1))
	r = e.do(t, request{method: http.MethodGet, path: path + "/" + id, token: alice.access})
	lost := r.json()["tracks"].([]any)[1].(map[string]any)
	if r.Code != http.StatusOK || lost["available"] != false || lost["attachment"] != nil || lost["title"] == "" {
		t.Fatalf("unavailable track: %d %s", r.Code, r.Body)
	}

	if r := e.do(t, request{method: http.MethodPatch, path: path + "/" + id, token: alice.access, body: map[string]any{"name": "Gym"}}); r.Code != http.StatusOK || r.json()["name"] != "Gym" || r.json()["version"].(float64) != 3 {
		t.Fatalf("rename: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: path + "/" + id, token: alice.access, body: map[string]any{"name": "   "}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("blank name: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: path + "/" + id, token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("bob deletes: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: path + "/" + id, token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	if n := e.notes.count(aliceID, EventPlaylistsUpdated); n != 4 {
		t.Fatalf("events: %d", n)
	}

	for i := range postgres.MaxPlaylists {
		if _, err := e.store.Personal().CreatePlaylist(t.Context(), aliceID, "P"+strconv.Itoa(i), nil); err != nil {
			t.Fatal(err)
		}
	}
	if r := e.do(t, request{method: http.MethodPost, path: path, token: alice.access, body: map[string]any{"name": "one too many"}}); r.Code != http.StatusConflict || r.json()["code"] != "limit_reached" {
		t.Fatalf("limit: %d %s", r.Code, r.Body)
	}
}
