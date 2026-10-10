package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/observability"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/users"
	"github.com/myronsi/messenger-back/internal/version"
)

type accountEnv struct {
	*authEnv
	store   *postgres.Store
	mu      sync.Mutex
	deleted []postgres.DeletedAccount
}

func newAccountEnv(t *testing.T) *accountEnv {
	t.Helper()
	store, rdb := newPostgres(t), newRedis(t)
	log := observability.NewLogger(&bytes.Buffer{}, 0, "test")
	svc, err := auth.NewService(store, rdb, auth.Config{
		JWTSecret: bytes.Repeat([]byte("j"), 40), EncryptionKey: bytes.Repeat([]byte("k"), 32),
		RecoveryPepper: bytes.Repeat([]byte("p"), 32), Namespace: "t" + uuid.NewString(),
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	e := &accountEnv{store: store}
	api := NewServer(NewAuthServer(AuthOptions{Service: svc, Log: log, BasePath: apiBase, CookieSecure: true}), nil).
		WithAccount(NewAccountServer(AccountOptions{
			Store: store, Directory: users.NewDirectory(store, nil, apiBase), Deleter: svc,
			AfterDeletion: func(_ context.Context, _ int64, d postgres.DeletedAccount) {
				e.mu.Lock()
				e.deleted = append(e.deleted, d)
				e.mu.Unlock()
			},
			Limiter: redis.NewRateLimiter(rdb, "t"+uuid.NewString()+":"), BasePath: apiBase, Log: log,
		})).
		WithMeta(MetaInfo{BackendVersion: "1.2.3", Commit: "abc1234", MinClientAPIVersion: "2.0.0-alpha.2"})
	e.authEnv = &authEnv{router: NewRouter(Options{
		HTTP:                config.HTTP{BasePath: apiBase, RequestTimeout: 10 * time.Second, ReadinessTimeout: time.Second, MaxBodyBytes: 4096},
		Log:                 log,
		Metrics:             observability.NewMetrics(),
		API:                 api,
		Authenticator:       svc,
		MinClientAPIVersion: "2.0.0-alpha.2",
	})}
	return e
}

func (e *accountEnv) id(t *testing.T, s session) string {
	t.Helper()
	return e.do(t, request{method: http.MethodGet, path: "/me", token: s.access}).json()["id"].(string)
}

func TestMetaAndClientVersions(t *testing.T) {
	e := newAccountEnv(t)
	m := e.do(t, request{method: http.MethodGet, path: "/meta"})
	if m.Code != http.StatusOK || m.json()["api_version"] != version.API || m.json()["backend_version"] != "1.2.3" || m.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("meta: %d %s", m.Code, m.Body)
	}
	alice := e.register(t, "alice")
	cases := map[string]int{"": http.StatusOK, version.API: http.StatusOK, "1.9.0": http.StatusUpgradeRequired, "2.0.0-alpha.1": http.StatusUpgradeRequired, "two": http.StatusBadRequest}
	for v, want := range cases {
		h := map[string]string{}
		if v != "" {
			h[ClientAPIVersionHeader] = v
		}
		r := e.do(t, request{method: http.MethodGet, path: "/me", token: alice.access, header: h})
		if r.Code != want {
			t.Errorf("version %q: %d, want %d", v, r.Code, want)
		}
		if want == http.StatusUpgradeRequired && r.code() != "client_outdated" {
			t.Errorf("426 code %q", r.code())
		}
	}
	// /meta answers outdated clients too: it is how they learn what to upgrade to.
	if r := e.do(t, request{method: http.MethodGet, path: "/meta", header: map[string]string{ClientAPIVersionHeader: "1.0.0"}}); r.Code != http.StatusOK {
		t.Fatalf("meta for an outdated client: %d", r.Code)
	}
}

func TestProfileAndPrivacy(t *testing.T) {
	e := newAccountEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	bobID := e.id(t, bob)

	r := e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: map[string]any{"display_name": " Alice A. ", "bio": "hi"}})
	if r.Code != http.StatusOK || r.json()["display_name"] != "Alice A." || r.json()["bio"] != "hi" {
		t.Fatalf("patch me: %d %s", r.Code, r.Body)
	}
	r = e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: map[string]any{"bio": nil}})
	if r.Code != http.StatusOK || r.json()["bio"] != nil || r.json()["display_name"] != "Alice A." {
		t.Fatalf("clear bio: %d %s", r.Code, r.Body)
	}
	for _, bad := range []map[string]any{{}, {"display_name": ""}, {"username": "x"}} {
		if r := e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: bad}); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("patch %v: %d", bad, r.Code)
		}
	}

	p := e.do(t, request{method: http.MethodGet, path: "/me/privacy", token: alice.access}).json()
	if p["avatar_visibility"] != "everyone" || p["exceptions"].(map[string]any)["avatar_visibility"] == nil {
		t.Fatalf("defaults: %v", p)
	}
	r = e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: map[string]any{"avatar_visibility": "contacts", "profile_visibility": "nobody", "read_receipts_enabled": false}})
	if r.Code != http.StatusOK || r.json()["avatar_visibility"] != "contacts" || r.json()["profile_visibility"] != "nobody" || r.json()["read_receipts_enabled"] != false {
		t.Fatalf("patch privacy: %d %s", r.Code, r.Body)
	}
	// A patch leaves the settings it does not name alone.
	r = e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: map[string]any{"search_visibility": "nobody"}})
	if r.json()["avatar_visibility"] != "contacts" || r.json()["search_visibility"] != "nobody" || r.json()["read_receipts_enabled"] != false {
		t.Fatalf("partial patch: %s", r.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: map[string]any{"avatar_visibility": "friends"}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown value: %d", r.Code)
	}
	r = e.do(t, request{method: http.MethodPut, path: "/me/privacy/exceptions/avatar_visibility/allow", token: alice.access, body: map[string]any{"user_ids": []string{bobID}}})
	if r.Code != http.StatusOK {
		t.Fatalf("exceptions: %d %s", r.Code, r.Body)
	}
	allow := r.json()["exceptions"].(map[string]any)["avatar_visibility"].(map[string]any)["allow"].([]any)
	if len(allow) != 1 || allow[0] != bobID {
		t.Fatalf("allow list: %v", allow)
	}
	// Moving bob to the deny list replaces the allow entry.
	r = e.do(t, request{method: http.MethodPut, path: "/me/privacy/exceptions/avatar_visibility/deny", token: alice.access, body: map[string]any{"user_ids": []string{bobID}}})
	lists := r.json()["exceptions"].(map[string]any)["avatar_visibility"].(map[string]any)
	if len(lists["allow"].([]any)) != 0 || len(lists["deny"].([]any)) != 1 {
		t.Fatalf("after moving: %v", lists)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/me/privacy/exceptions/avatar_visibility/deny", token: alice.access, body: map[string]any{"user_ids": []string{"999999"}}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown user in exceptions: %d", r.Code)
	}
	// Bob sees no bio: alice's profile is visible to nobody.
	alicePublic := e.do(t, request{method: http.MethodGet, path: "/usernames/alice", token: bob.access})
	if alicePublic.Code != http.StatusOK || alicePublic.json()["display_name"] != "Alice A." {
		t.Fatalf("user by name: %d %s", alicePublic.Code, alicePublic.Body)
	}
}

func TestBlocksContactsAndSearch(t *testing.T) {
	e := newAccountEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bobby_tables"), e.register(t, "carol")
	bobID, carolID := e.id(t, bob), e.id(t, carol)

	for _, id := range []string{bobID, carolID} {
		if r := e.do(t, request{method: http.MethodPut, path: "/me/blocked-users/" + id, token: alice.access}); r.Code != http.StatusNoContent {
			t.Fatalf("block: %d", r.Code)
		}
	}
	page := e.do(t, request{method: http.MethodGet, path: "/me/blocked-users?limit=1", token: alice.access}).json()
	if len(page["items"].([]any)) != 1 || page["next_cursor"] == nil {
		t.Fatalf("first page: %v", page)
	}
	page2 := e.do(t, request{method: http.MethodGet, path: "/me/blocked-users?limit=1&after=" + page["next_cursor"].(string), token: alice.access}).json()
	if len(page2["items"].([]any)) != 1 || page2["next_cursor"] != nil {
		t.Fatalf("second page: %v", page2)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/me/blocked-users/" + carolID, token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("unblock: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/me/blocked-users/" + carolID, token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("unblock twice: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/me/blocked-users/999999", token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("unblock an unknown user: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/users/abc", token: alice.access}); r.Code != http.StatusBadRequest {
		t.Fatalf("malformed id: %d", r.Code)
	}
	// The blocked user sees nothing of the blocker that privacy guards.
	e.do(t, request{method: http.MethodPatch, path: "/me", token: alice.access, body: map[string]any{"bio": "hello"}})
	if v := e.do(t, request{method: http.MethodGet, path: "/usernames/alice", token: bob.access}).json(); v["bio"] != nil || v["last_seen"] != nil {
		t.Fatalf("blocker as the blocked user sees them: %v", v)
	}
	if v := e.do(t, request{method: http.MethodGet, path: "/usernames/alice", token: carol.access}).json(); v["bio"] != "hello" {
		t.Fatalf("blocker as others see them: %v", v)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/me/blocked-users/999999", token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("block unknown: %d", r.Code)
	}

	r := e.do(t, request{method: http.MethodPut, path: "/users/" + carolID + "/contact-name", token: alice.access, body: map[string]any{"contact_name": "Caro"}})
	if r.Code != http.StatusOK || r.json()["contact_name"] != "Caro" {
		t.Fatalf("contact name: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/users/" + carolID + "/contact-name", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("remove contact name: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/users/999999", token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d", r.Code)
	}

	search := func(token, q string) []string {
		r := e.do(t, request{method: http.MethodGet, path: "/users?q=" + q, token: token})
		if r.Code != http.StatusOK {
			t.Fatalf("search %q: %d %s", q, r.Code, r.Body)
		}
		var names []string
		for _, it := range r.json()["items"].([]any) {
			names = append(names, it.(map[string]any)["username"].(string))
		}
		return names
	}
	// bob is blocked by alice, so bob does not find alice; carol does.
	if got := search(bob.access, "ali"); len(got) != 0 {
		t.Fatalf("blocked user found the blocker: %v", got)
	}
	if got := search(carol.access, "ali"); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("prefix search: %v", got)
	}
	if got := search(carol.access, "Display%20bobby"); len(got) != 1 {
		t.Fatalf("display name search: %v", got)
	}
	if got := search(carol.access, "%25%25"); len(got) != 0 {
		t.Fatalf("LIKE wildcards are not escaped: %v", got)
	}
	// Opting out of search hides alice from everyone but herself.
	e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: map[string]any{"search_visibility": "nobody"}})
	if got := search(carol.access, "ali"); len(got) != 0 {
		t.Fatalf("opted-out user found: %v", got)
	}
	// Two characters match usernames only (the trigram index needs three).
	if got := search(carol.access, "bo"); len(got) != 1 || got[0] != "bobby_tables" {
		t.Fatalf("two-character search: %v", got)
	}
	if got := search(carol.access, "is"); len(got) != 0 {
		t.Fatalf("two-character display-name match: %v", got)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/users?q=a", token: carol.access}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("short query: %d", r.Code)
	}
}

func TestDeleteAccountOverHTTP(t *testing.T) {
	e := newAccountEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID, _ := strconv.ParseInt(e.id(t, alice), 10, 64)
	bobID, _ := strconv.ParseInt(e.id(t, bob), 10, 64)
	chat, _, err := e.store.Chats().CreateDirect(context.Background(), aliceID, bobID)
	if err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/me", token: alice.access, body: map[string]any{"password": "wrong password"}}); r.Code != http.StatusForbidden {
		t.Fatalf("wrong password: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/me", token: alice.access, body: map[string]any{"password": password}}); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.deleted) != 1 || len(e.deleted[0].DeletedChats[chat.ID]) != 1 || e.deleted[0].DeletedChats[chat.ID][0] != bobID {
		t.Fatalf("cleanup: %+v", e.deleted)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/me", token: alice.access}); r.Code != http.StatusUnauthorized {
		t.Fatalf("token of a deleted account: %d", r.Code)
	}
	if r := e.login(t, "alice"); r.Code != http.StatusUnauthorized {
		t.Fatalf("login of a deleted account: %d", r.Code)
	}
}

func TestVersionLabels(t *testing.T) {
	for raw, want := range map[string]string{
		"2.0.0": "2.0.0", "2.0.0-alpha.4": "2.0.0-alpha.4", "2.0.0-alpha.4+build.7": "2.0.0-alpha.4",
		"2.1.3+x": "2.1.3", "2.0.0-made.up.value": "other", "2.0.0-alpha.12345": "other",
	} {
		if got := versionLabel(version.MustParse(raw)); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
}
