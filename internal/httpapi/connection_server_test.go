package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/connections"
	"github.com/myronsi/messenger-back/internal/observability"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/users"
)

type siteVerifier struct{ published map[string]string }

func (siteVerifier) Name() string { return "website" }

func (siteVerifier) Normalize(input string) (string, error) {
	if !strings.Contains(input, ".") {
		return "", connections.ErrInvalidHandle
	}
	return strings.ToLower(input), nil
}

func (v siteVerifier) Check(_ context.Context, host, code string) (connections.Account, error) {
	if v.published[host] != code {
		return connections.Account{}, connections.ErrNotVerified
	}
	link := "https://" + host
	return connections.Account{ExternalID: host, DisplayName: host, ProfileURL: &link}, nil
}

func fakeGitHub(t *testing.T) *connections.OAuth {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "login": "octo", "html_url": "https://github.com/octo"})
		}
	}))
	t.Cleanup(srv.Close)
	p := connections.GitHub(connections.Credentials{ClientID: "id", ClientSecret: "secret"})
	p.AuthorizeURL, p.ExchangeURL = srv.URL+"/authorize", srv.URL+"/token"
	p.Profile = func(ctx context.Context, c *http.Client, token string) (connections.Account, error) {
		var u struct {
			Login string `json:"login"`
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/user", nil)
		resp, err := c.Do(req)
		if err != nil {
			return connections.Account{}, err
		}
		defer func() { _ = resp.Body.Close() }()
		_ = json.NewDecoder(resp.Body).Decode(&u)
		link := "https://github.com/" + u.Login
		return connections.Account{ExternalID: "7", DisplayName: u.Login, ProfileURL: &link}, nil
	}
	return p
}

func newConnectionEnv(t *testing.T, published map[string]string) *accountEnv {
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
	dir := users.NewDirectory(store, nil, apiBase)
	limiter := redis.NewRateLimiter(rdb, "t"+uuid.NewString()+":")
	conn := connections.NewService(connections.Options{
		Providers: []connections.Provider{fakeGitHub(t)}, Verifiers: []connections.Verifier{siteVerifier{published}},
		Store: store.Connections(), State: connections.NewRedisState(rdb, "t"+uuid.NewString()+":"),
		CallbackURL: "https://app.example" + apiBase, WebURL: "https://app.example",
	})
	api := NewServer(NewAuthServer(AuthOptions{Service: svc, Log: log, BasePath: apiBase, CookieSecure: true}), nil).
		WithAccount(NewAccountServer(AccountOptions{Store: store, Directory: dir, Deleter: svc, Limiter: limiter, BasePath: apiBase, Log: log})).
		WithConnections(NewConnectionServer(ConnectionOptions{Service: conn, Users: store.Users(), Directory: dir, Limiter: limiter, Log: log})).
		WithMeta(MetaInfo{BackendVersion: "1.2.3", Commit: "abc1234", MinClientAPIVersion: "2.0.0-alpha.2"})
	e := &accountEnv{store: store}
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

func TestConnectionsOverHTTP(t *testing.T) {
	published := map[string]string{}
	e := newConnectionEnv(t, published)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID := e.id(t, alice)

	meta := e.do(t, request{method: http.MethodGet, path: "/meta"}).json()
	if got := meta["connection_providers"].([]any); len(got) != 2 || got[0] != "github" || got[1] != "website" {
		t.Fatalf("meta providers: %v", got)
	}

	r := e.do(t, request{method: http.MethodPost, path: "/me/connections/website/claim", token: alice.access, body: map[string]any{"identifier": "Example.org"}})
	if r.Code != http.StatusOK || r.json()["identifier"] != "example.org" {
		t.Fatalf("claim: %d %s", r.Code, r.Body)
	}
	code := r.json()["code"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: "/me/connections/website/verify", token: alice.access}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("verify before publishing: %d %s", r.Code, r.Body)
	}
	published["example.org"] = code
	r = e.do(t, request{method: http.MethodPost, path: "/me/connections/website/verify", token: alice.access})
	if r.Code != http.StatusCreated || r.json()["profile_url"] != "https://example.org" {
		t.Fatalf("verify: %d %s", r.Code, r.Body)
	}
	siteID := r.json()["id"].(string)

	for _, bad := range []request{
		{method: http.MethodPost, path: "/me/connections/website/start"},
		{method: http.MethodPost, path: "/me/connections/github/claim", body: map[string]any{"identifier": "x.y"}},
		{method: http.MethodPost, path: "/me/connections/website/claim", body: map[string]any{"identifier": "nodot"}},
	} {
		bad.token = alice.access
		if r := e.do(t, bad); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s %s: %d %s", bad.method, bad.path, r.Code, r.Body)
		}
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/me/connections/spotify/start", token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("disabled provider: %d", r.Code)
	}

	r = e.do(t, request{method: http.MethodPost, path: "/me/connections/github/start", token: alice.access})
	if r.Code != http.StatusOK {
		t.Fatalf("start: %d %s", r.Code, r.Body)
	}
	link, _ := url.Parse(r.json()["redirect_url"].(string))
	state := link.Query().Get("state")
	cb := e.do(t, request{method: http.MethodGet, path: "/connections/callback/github?code=c&state=" + url.QueryEscape(state)})
	if cb.Code != http.StatusFound || cb.Header().Get("Location") != "https://app.example/settings/connections?provider=github&result=connected" {
		t.Fatalf("callback: %d %v", cb.Code, cb.Header())
	}
	cb = e.do(t, request{method: http.MethodGet, path: "/connections/callback/github?code=c&state=" + url.QueryEscape(state)})
	if !strings.HasSuffix(cb.Header().Get("Location"), "result=failed") {
		t.Fatalf("replayed state: %v", cb.Header())
	}

	list := e.do(t, request{method: http.MethodGet, path: "/me/connections", token: alice.access}).json()["items"].([]any)
	if len(list) != 2 || list[1].(map[string]any)["provider"] != "github" {
		t.Fatalf("list: %v", list)
	}
	ghID := list[1].(map[string]any)["id"].(string)
	r = e.do(t, request{method: http.MethodPut, path: "/me/connections/order", token: alice.access, body: map[string]any{"ids": []string{ghID, siteID}}})
	if r.Code != http.StatusOK || r.json()["items"].([]any)[0].(map[string]any)["id"] != ghID {
		t.Fatalf("reorder: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/me/connections/order", token: alice.access, body: map[string]any{"ids": []string{ghID}}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("partial order: %d", r.Code)
	}

	seen := e.do(t, request{method: http.MethodGet, path: "/users/" + aliceID + "/connections", token: bob.access})
	if seen.Code != http.StatusOK || len(seen.json()["items"].([]any)) != 2 || seen.Header().Get("ETag") == "" {
		t.Fatalf("others see them: %d %s", seen.Code, seen.Body)
	}
	if r := e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: map[string]any{"profile_visibility": "nobody"}}); r.Code != http.StatusOK {
		t.Fatalf("privacy: %d", r.Code)
	}
	if got := e.do(t, request{method: http.MethodGet, path: "/users/" + aliceID + "/connections", token: bob.access}).json()["items"].([]any); len(got) != 0 {
		t.Fatalf("a hidden profile hides them: %v", got)
	}
	if got := e.do(t, request{method: http.MethodGet, path: "/users/" + aliceID + "/connections", token: alice.access}).json()["items"].([]any); len(got) != 2 {
		t.Fatalf("the owner still sees them: %v", got)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/users/999999999/connections", token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d", r.Code)
	}

	if r := e.do(t, request{method: http.MethodDelete, path: "/me/connections/" + siteID, token: bob.access}); r.Code != http.StatusNotFound {
		t.Fatalf("someone else's: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/me/connections/" + siteID, token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", r.Code)
	}
}
