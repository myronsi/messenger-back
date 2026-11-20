package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func (e *chatEnv) group(t *testing.T, owner session, name string, members ...session) string {
	t.Helper()
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = e.uid(t, m)
	}
	e.openInvites(t, members...)
	r := e.do(t, request{method: http.MethodPost, path: "/groups", token: owner.access, body: map[string]any{"name": name, "member_ids": ids}})
	if r.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", r.Code, r.Body)
	}
	return r.json()["id"].(string)
}

func TestPublicNameShowsAMinimalUserCard(t *testing.T) {
	e := newChatEnv(t)
	anna := e.register(t, "anna")
	if r := e.do(t, request{method: http.MethodPatch, path: "/me", token: anna.access, body: map[string]any{"bio": "Hello there"}}); r.Code != http.StatusOK {
		t.Fatalf("set bio: %d %s", r.Code, r.Body)
	}
	r := e.do(t, request{method: http.MethodGet, path: "/public/names/ANNA"})
	if r.Code != http.StatusOK {
		t.Fatalf("lookup: %d %s", r.Code, r.Body)
	}
	if got := r.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Fatalf("X-Robots-Tag = %q", got)
	}
	body := r.json()
	if body["kind"] != "user" {
		t.Fatalf("kind = %v", body["kind"])
	}
	u := body["user"].(map[string]any)
	if u["username"] != "anna" || u["display_name"] != "Display anna" || u["bio"] != "Hello there" {
		t.Fatalf("user = %v", u)
	}
	for _, k := range []string{"id", "is_online", "last_seen", "birthday", "pronouns"} {
		if _, ok := u[k]; ok {
			t.Fatalf("public card leaks %s: %v", k, u)
		}
	}

	if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: anna.access, body: map[string]any{"profile_visibility": "contacts"}}); r.Code != http.StatusOK {
		t.Fatalf("privacy: %d %s", r.Code, r.Body)
	}
	if u := e.do(t, request{method: http.MethodGet, path: "/public/names/anna"}).json()["user"].(map[string]any); u["bio"] != nil {
		t.Fatalf("bio shown with profile_visibility contacts: %v", u)
	}

	if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: anna.access, body: map[string]any{"web_profile_visibility": "nobody"}}); r.Code != http.StatusOK {
		t.Fatalf("privacy: %d %s", r.Code, r.Body)
	}
	if got := e.do(t, request{method: http.MethodGet, path: "/me/privacy", token: anna.access}).json()["web_profile_visibility"]; got != "nobody" {
		t.Fatalf("web_profile_visibility = %v", got)
	}
	private := e.do(t, request{method: http.MethodGet, path: "/public/names/anna"}).Body.String()
	unknown := e.do(t, request{method: http.MethodGet, path: "/public/names/nobody_here"}).Body.String()
	if private != unknown || !strings.Contains(private, `"unavailable"`) {
		t.Fatalf("private %s and unknown %s must look the same", private, unknown)
	}
}

func TestOpenGraphPageForAUser(t *testing.T) {
	e := newChatEnv(t)
	e.register(t, "anna")
	r := e.do(t, request{method: http.MethodGet, path: "/og/names/anna"})
	if r.Code != http.StatusOK || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("og: %d %s %s", r.Code, r.Header().Get("Content-Type"), r.Body)
	}
	html := r.Body.String()
	for _, want := range []string{`<meta property="og:title" content="Display anna (@anna)">`, `content="https://chat.example/@anna"`, `name="robots" content="noindex, nofollow"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("og page lacks %s:\n%s", want, html)
		}
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/og/names/missing_one"}); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "<title>Messenger</title>") {
		t.Fatalf("generic og: %d %s", r.Code, r.Body)
	}
}

func TestInviteLinksJoinBanAndUnban(t *testing.T) {
	e := newChatEnv(t)
	owner, member, bob, carol := e.register(t, "owner"), e.register(t, "member"), e.register(t, "bob"), e.register(t, "carol")
	gid := e.group(t, owner, "Team", member)
	links := "/groups/" + gid + "/invite-links"

	if r := e.do(t, request{method: http.MethodPost, path: links, token: member.access, body: map[string]any{}}); r.Code != http.StatusForbidden {
		t.Fatalf("member creates link: %d %s", r.Code, r.Body)
	}
	r := e.do(t, request{method: http.MethodPost, path: links, token: owner.access, body: map[string]any{"name": "first", "max_uses": 1}})
	if r.Code != http.StatusCreated {
		t.Fatalf("create link: %d %s", r.Code, r.Body)
	}
	link := r.json()
	token := link["token"].(string)
	if link["is_active"] != true || link["max_uses"] != float64(1) || link["expires_at"] != nil {
		t.Fatalf("link = %v", link)
	}

	card := e.do(t, request{method: http.MethodGet, path: "/public/invites/" + token})
	if card.Code != http.StatusOK || card.json()["name"] != "Team" || card.json()["member_count"] != float64(2) {
		t.Fatalf("public invite: %d %s", card.Code, card.Body)
	}
	if _, ok := card.json()["members"]; ok {
		t.Fatalf("public invite lists members: %s", card.Body)
	}
	if pv := e.do(t, request{method: http.MethodGet, path: "/invites/" + token, token: bob.access}).json(); pv["chat_id"] != nil {
		t.Fatalf("preview for non-member: %v", pv)
	}

	joined := e.do(t, request{method: http.MethodPost, path: "/invites/" + token + "/join", token: bob.access})
	if joined.Code != http.StatusOK || joined.json()["id"] != gid {
		t.Fatalf("join: %d %s", joined.Code, joined.Body)
	}
	if again := e.do(t, request{method: http.MethodPost, path: "/invites/" + token + "/join", token: bob.access}); again.Code != http.StatusOK {
		t.Fatalf("join again as member: %d %s", again.Code, again.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/invites/" + token + "/join", token: carol.access}); r.Code != http.StatusNotFound {
		t.Fatalf("used-up link: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/public/invites/" + token}); r.Code != http.StatusNotFound {
		t.Fatalf("used-up public card: %d %s", r.Code, r.Body)
	}

	bobID := e.uid(t, bob)
	if r := e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid + "/participants/" + bobID, token: owner.access}); r.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", r.Code, r.Body)
	}
	bans := e.do(t, request{method: http.MethodGet, path: "/groups/" + gid + "/bans", token: owner.access}).json()["items"].([]any)
	if len(bans) != 1 || bans[0].(map[string]any)["user"].(map[string]any)["id"] != bobID {
		t.Fatalf("bans = %v", bans)
	}
	second := e.do(t, request{method: http.MethodPost, path: links, token: owner.access, body: map[string]any{}}).json()["token"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: "/invites/" + second + "/join", token: bob.access}); r.Code != http.StatusForbidden {
		t.Fatalf("banned join: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid + "/bans/" + bobID, token: owner.access}); r.Code != http.StatusNoContent {
		t.Fatalf("unban: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/invites/" + second + "/join", token: bob.access}); r.Code != http.StatusOK {
		t.Fatalf("join after unban: %d %s", r.Code, r.Body)
	}

	linkID := link["id"].(string)
	if r := e.do(t, request{method: http.MethodDelete, path: links + "/" + linkID, token: owner.access}); r.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: links + "/" + linkID, token: owner.access, body: map[string]any{"name": "x"}}); r.Code != http.StatusConflict {
		t.Fatalf("patch revoked: %d %s", r.Code, r.Body)
	}
	listed := e.do(t, request{method: http.MethodGet, path: links, token: owner.access}).json()["items"].([]any)
	if len(listed) != 2 {
		t.Fatalf("links = %v", listed)
	}
}

func TestInviteLinkValidation(t *testing.T) {
	e := newChatEnv(t)
	owner := e.register(t, "owner")
	gid := e.group(t, owner, "Team")
	links := "/groups/" + gid + "/invite-links"
	for name, body := range map[string]map[string]any{
		"past expiry":  {"expires_at": "2001-01-01T00:00:00Z"},
		"zero uses":    {"max_uses": 0},
		"long name":    {"name": strings.Repeat("n", 65)},
		"far ahead":    {"expires_at": "2099-01-01T00:00:00Z"},
		"too many use": {"max_uses": 100001},
	} {
		if r := e.do(t, request{method: http.MethodPost, path: links, token: owner.access, body: body}); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	r := e.do(t, request{method: http.MethodPost, path: links, token: owner.access, body: map[string]any{"max_uses": 5}})
	id := r.json()["id"].(string)
	patched := e.do(t, request{method: http.MethodPatch, path: links + "/" + id, token: owner.access, body: map[string]any{"max_uses": nil}})
	if patched.Code != http.StatusOK || patched.json()["max_uses"] != nil {
		t.Fatalf("clear max_uses: %d %s", patched.Code, patched.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: links + "/" + id, token: owner.access, body: map[string]any{}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty patch: %d %s", r.Code, r.Body)
	}
}

func TestPublicGroupNames(t *testing.T) {
	e := newChatEnv(t)
	owner, bob := e.register(t, "owner"), e.register(t, "bob")
	gid := e.group(t, owner, "Dev Team")
	put := func(name string) reply {
		return e.do(t, request{method: http.MethodPut, path: "/groups/" + gid + "/public-name", token: owner.access, body: map[string]any{"public_name": name}})
	}
	if r := put("BOB"); r.Code != http.StatusConflict || r.code() != "already_exists" {
		t.Fatalf("name of a user: %d %s", r.Code, r.Body)
	}
	if r := put("admin"); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("reserved: %d %s", r.Code, r.Body)
	}
	if r := put("devteam"); r.Code != http.StatusOK || r.json()["public_name"] != "devteam" {
		t.Fatalf("set: %d %s", r.Code, r.Body)
	}
	if a := e.do(t, request{method: http.MethodGet, path: "/public-names/DevTeam/availability", token: bob.access}).json(); a["available"] != false {
		t.Fatalf("availability = %v", a)
	}
	if r := e.post(t, "/auth/register", map[string]any{"username": "DEVTEAM", "display_name": "x", "password": password}); r.Code != http.StatusConflict {
		t.Fatalf("register a group's name: %d %s", r.Code, r.Body)
	}

	card := e.do(t, request{method: http.MethodGet, path: "/public/names/devteam"}).json()
	if card["kind"] != "group" || card["group"].(map[string]any)["name"] != "Dev Team" {
		t.Fatalf("public group card = %v", card)
	}
	found := e.do(t, request{method: http.MethodGet, path: "/public-groups?q=dev", token: bob.access}).json()["items"].([]any)
	if len(found) != 1 || found[0].(map[string]any)["chat_id"] != nil {
		t.Fatalf("search = %v", found)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/public-groups/devteam/join", token: bob.access}); r.Code != http.StatusOK {
		t.Fatalf("join public: %d %s", r.Code, r.Body)
	}
	if pv := e.do(t, request{method: http.MethodGet, path: "/public-groups/devteam", token: bob.access}).json(); pv["chat_id"] != gid {
		t.Fatalf("preview as member = %v", pv)
	}

	if r := e.do(t, request{method: http.MethodDelete, path: "/groups/" + gid + "/public-name", token: owner.access}); r.Code != http.StatusOK || r.json()["public_name"] != nil {
		t.Fatalf("clear: %d %s", r.Code, r.Body)
	}
	if a := e.do(t, request{method: http.MethodGet, path: "/public-names/devteam/availability", token: bob.access}).json(); a["available"] != true {
		t.Fatalf("availability after clear = %v", a)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/public-groups/devteam/join", token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("join a private group by its old name: %d %s", r.Code, r.Body)
	}
}

func TestReservedUsernamesCannotRegister(t *testing.T) {
	e := newChatEnv(t)
	if r := e.post(t, "/auth/register", map[string]any{"username": "Support", "display_name": "x", "password": password}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("register reserved: %d %s", r.Code, r.Body)
	}
}

func TestMentionsAreStoredCountedAndRead(t *testing.T) {
	e := newChatEnv(t)
	owner, bob, carol := e.register(t, "owner"), e.register(t, "bob"), e.register(t, "carol")
	gid := e.group(t, owner, "Team", bob, carol)

	r := e.send(t, owner, gid, "m-1", "hi @BOB and @nobody_here and @all")
	if r.Code != http.StatusCreated {
		t.Fatalf("send: %d %s", r.Code, r.Body)
	}
	msg := r.json()
	bobID := e.uid(t, bob)
	if got := msg["mentions"].([]any); len(got) != 1 || got[0] != bobID || msg["mentions_all"] != true {
		t.Fatalf("mentions = %v, all = %v", msg["mentions"], msg["mentions_all"])
	}

	chatOf := func(s session) map[string]any {
		t.Helper()
		for _, c := range e.do(t, request{method: http.MethodGet, path: "/chats", token: s.access}).json()["items"].([]any) {
			if c.(map[string]any)["id"] == gid {
				return c.(map[string]any)
			}
		}
		t.Fatal("group not in the chat list")
		return nil
	}
	if n := chatOf(carol)["unread_mentions_count"]; n != float64(1) {
		t.Fatalf("carol unread mentions = %v", n)
	}
	list := e.do(t, request{method: http.MethodGet, path: "/chats/" + gid + "/mentions", token: bob.access}).json()["message_ids"].([]any)
	if len(list) != 1 || list[0] != msg["id"] {
		t.Fatalf("bob mentions = %v", list)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + gid + "/read", token: bob.access, body: map[string]any{"message_id": msg["id"]}}); r.Code != http.StatusOK {
		t.Fatalf("read: %d %s", r.Code, r.Body)
	}
	if n := chatOf(bob)["unread_mentions_count"]; n != float64(0) {
		t.Fatalf("bob unread mentions after read = %v", n)
	}

	plain := e.send(t, bob, gid, "m-2", "@all look").json()
	if _, ok := plain["mentions_all"]; ok {
		t.Fatalf("@all from a member counts: %v", plain)
	}

	edited := e.do(t, request{method: http.MethodPatch, path: "/messages/" + msg["id"].(string), token: owner.access, body: map[string]any{"content": "just @carol"}})
	if edited.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", edited.Code, edited.Body)
	}
	if got := edited.json()["mentions"].([]any); len(got) != 1 || got[0] != e.uid(t, carol) {
		t.Fatalf("mentions after edit = %v", got)
	}

	if r := e.do(t, request{method: http.MethodDelete, path: "/messages/" + msg["id"].(string) + "?scope=everyone", token: owner.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	if n := chatOf(carol)["unread_mentions_count"]; n != float64(0) {
		t.Fatalf("carol unread mentions after delete = %v", n)
	}
}
