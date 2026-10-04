package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp/totp"
	goredis "github.com/redis/go-redis/v9"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/observability"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	apiBase       = "/api/v2"
	migrationsDir = "../../migrations/postgres"
	password      = "correct horse battery"
)

// newPostgres returns a store on a fresh database with the full schema, or skips the test.
func newPostgres(t *testing.T) *postgres.Store {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	name := "test_" + hex.EncodeToString(raw)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = conn.Exec(c, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = conn.Close(c)
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name

	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations: %v", err)
	}
	sort.Strings(files)
	mc, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer mc.Close(ctx)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mc.Exec(ctx, string(sql), pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
	}
	s, err := postgres.New(ctx, u.String(), postgres.Options{MaxConns: 8, QueryTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func newRedis(t *testing.T) *goredis.Client {
	t.Helper()
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("TEST_REDIS_URL is not set")
	}
	opts, err := goredis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	c := goredis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis: %v", err)
	}
	return c
}

type authEnv struct {
	router http.Handler
	logs   *bytes.Buffer
}

// newAuthEnv builds the router with the real service on fresh stores.
func newAuthEnv(t *testing.T, grace time.Duration, mutate func(*AuthOptions)) *authEnv {
	t.Helper()
	store, rdb := newPostgres(t), newRedis(t)
	logs := &bytes.Buffer{}
	log := observability.NewLogger(logs, 0, "test")
	svc, err := auth.NewService(store, rdb, auth.Config{
		JWTSecret:         bytes.Repeat([]byte("j"), 40),
		EncryptionKey:     bytes.Repeat([]byte("k"), 32),
		RecoveryPepper:    bytes.Repeat([]byte("p"), 32),
		SessionCacheTTL:   time.Minute,
		RefreshReuseGrace: grace,
		Namespace:         "t" + uuid.NewString(),
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	opts := AuthOptions{Service: svc, Log: log, BasePath: apiBase, CookieSecure: true}
	if mutate != nil {
		mutate(&opts)
	}
	r := NewRouter(Options{
		HTTP: config.HTTP{
			BasePath:         apiBase,
			RequestTimeout:   10 * time.Second,
			ReadinessTimeout: time.Second,
			MaxBodyBytes:     4096,
		},
		Log:           log,
		Metrics:       observability.NewMetrics(),
		API:           NewAuthServer(opts),
		Authenticator: svc,
	})
	return &authEnv{router: r, logs: logs}
}

type reply struct {
	*httptest.ResponseRecorder
}

func (r reply) json() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &m); err != nil {
		panic("not a JSON object: " + r.Body.String())
	}
	return m
}

func (r reply) code() string { s, _ := r.json()["code"].(string); return s }

func (r reply) refreshCookie() *http.Cookie {
	for _, c := range r.Result().Cookies() {
		if c.Name == RefreshCookieName {
			return c
		}
	}
	return nil
}

type request struct {
	method, path string
	body         any
	token        string
	cookie       string
	remote       string
	header       map[string]string
}

func (e *authEnv) do(t *testing.T, q request) reply {
	t.Helper()
	var rd *bytes.Reader
	switch b := q.body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(q.method, apiBase+q.path, rd)
	if q.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if q.token != "" {
		req.Header.Set("Authorization", "Bearer "+q.token)
	}
	if q.cookie != "" {
		req.AddCookie(&http.Cookie{Name: RefreshCookieName, Value: q.cookie})
	}
	req.RemoteAddr = "203.0.113.7:4000"
	if q.remote != "" {
		req.RemoteAddr = q.remote
	}
	for k, v := range q.header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return reply{rec}
}

func (e *authEnv) post(t *testing.T, path string, body any) reply {
	return e.do(t, request{method: http.MethodPost, path: path, body: body})
}

type session struct {
	access, refresh string
}

func (e *authEnv) register(t *testing.T, name string) session {
	t.Helper()
	rec := e.post(t, "/auth/register", map[string]any{"username": name, "display_name": "Display " + name, "password": password})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	return session{access: rec.json()["access_token"].(string), refresh: rec.refreshCookie().Value}
}

func (e *authEnv) login(t *testing.T, name string) reply {
	return e.post(t, "/auth/login", map[string]any{"username": name, "password": password})
}

func TestRegisterLoginRefreshOverHTTP(t *testing.T) {
	e := newAuthEnv(t, 0, nil)

	rec := e.post(t, "/auth/register", map[string]any{"username": "alice", "display_name": "Alice", "password": password, "bio": " hello "})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	body := rec.json()
	if body["token_type"] != "Bearer" || body["expires_in"].(float64) < 800 || body["expires_in"].(float64) > 900 {
		t.Fatalf("token response: %v", body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token responses must not be cached")
	}
	c := rec.refreshCookie()
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != apiBase+"/auth/refresh" || c.MaxAge <= 0 {
		t.Fatalf("refresh cookie: %+v", c)
	}
	if strings.Contains(rec.Body.String(), c.Value) {
		t.Fatal("the refresh token must only travel in the cookie")
	}

	me := e.do(t, request{method: http.MethodGet, path: "/me", token: body["access_token"].(string)})
	if me.Code != http.StatusOK || me.json()["username"] != "alice" || me.json()["bio"] != "hello" || me.json()["id"] == "" {
		t.Fatalf("me: %d %s", me.Code, me.Body)
	}

	if rec := e.post(t, "/auth/register", map[string]any{"username": "ALICE", "display_name": "x", "password": password}); rec.Code != http.StatusConflict || rec.code() != "already_exists" {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body)
	}
	rec = e.post(t, "/auth/register", map[string]any{"username": "bob", "display_name": "Bob", "password": "short"})
	if rec.Code != http.StatusUnprocessableEntity || rec.code() != "validation_failed" || !strings.Contains(rec.Body.String(), `"field":"password"`) {
		t.Fatalf("weak password: %d %s", rec.Code, rec.Body)
	}

	if rec := e.post(t, "/auth/login", map[string]any{"username": "alice", "password": "wrong password"}); rec.Code != http.StatusUnauthorized || rec.code() != "invalid_credentials" {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body)
	}
	if rec := e.post(t, "/auth/login", map[string]any{"username": "nobody", "password": password}); rec.Code != http.StatusUnauthorized || rec.code() != "invalid_credentials" {
		t.Fatalf("unknown user: %d %s", rec.Code, rec.Body)
	}
	if rec := e.post(t, "/auth/login", map[string]any{"username": "alice"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing password: %d", rec.Code)
	}
	login := e.login(t, "alice")
	if login.Code != http.StatusOK || login.refreshCookie() == nil {
		t.Fatalf("login: %d %s", login.Code, login.Body)
	}

	// Rotation: the cookie of the login works once.
	old := login.refreshCookie().Value
	first := e.do(t, request{method: http.MethodPost, path: "/auth/refresh", cookie: old})
	if first.Code != http.StatusOK || first.refreshCookie() == nil || first.refreshCookie().Value == old {
		t.Fatalf("refresh: %d %s", first.Code, first.Body)
	}
	if first.json()["access_token"] == nil {
		t.Fatal("refresh returned no access token")
	}

	// Reuse of the rotated token is theft: the session dies, the cookie is cleared.
	reused := e.do(t, request{method: http.MethodPost, path: "/auth/refresh", cookie: old})
	if reused.Code != http.StatusUnauthorized {
		t.Fatalf("reuse: %d %s", reused.Code, reused.Body)
	}
	if c := reused.refreshCookie(); c == nil || c.MaxAge >= 0 {
		t.Fatalf("a refused refresh token must clear the cookie: %+v", c)
	}
	next := e.do(t, request{method: http.MethodPost, path: "/auth/refresh", cookie: first.refreshCookie().Value})
	if next.Code != http.StatusUnauthorized {
		t.Fatalf("the session of a reused token must be revoked, refresh gave %d", next.Code)
	}
	if got := e.do(t, request{method: http.MethodGet, path: "/me", token: first.json()["access_token"].(string)}); got.Code != http.StatusUnauthorized {
		t.Fatalf("access token of a revoked session: %d", got.Code)
	}

	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/refresh"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh without cookie: %d", rec.Code)
	}
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/refresh", cookie: "garbage"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh with garbage: %d", rec.Code)
	}
}

func TestInsecureCookieAndPathOverride(t *testing.T) {
	e := newAuthEnv(t, 0, func(o *AuthOptions) { o.CookieSecure = false; o.CookiePath = "/auth" })
	rec := e.post(t, "/auth/register", map[string]any{"username": "carol", "display_name": "Carol", "password": password})
	c := rec.refreshCookie()
	if c == nil || c.Secure || c.Path != "/auth" || !c.HttpOnly {
		t.Fatalf("cookie: %+v", c)
	}
}

func TestProtectedRoutesNeedAToken(t *testing.T) {
	e := newAuthEnv(t, 0, nil)
	s := e.register(t, "dave")

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/me"},
		{http.MethodGet, "/me/sessions"},
		{http.MethodGet, "/chats"}, // not implemented yet, but still protected
		{http.MethodPost, "/me/2fa/setup"},
		{http.MethodPut, "/me/password"},
	} {
		rec := e.do(t, request{method: tc.method, path: tc.path})
		if rec.Code != http.StatusUnauthorized || rec.code() != "unauthenticated" || rec.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("%s %s without token: %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
	if rec := e.do(t, request{method: http.MethodGet, path: "/chats", token: s.access}); rec.Code != http.StatusNotImplemented {
		t.Fatalf("authenticated but unimplemented: %d", rec.Code)
	}
	if rec := e.do(t, request{method: http.MethodGet, path: "/meta"}); rec.Code != http.StatusNotImplemented {
		t.Fatalf("public route: %d", rec.Code)
	}

	for name, header := range map[string]string{
		"garbage":         "Bearer not-a-token",
		"empty":           "Bearer ",
		"wrong scheme":    "Basic " + s.access,
		"no scheme":       s.access,
		"truncated token": "Bearer " + s.access[:len(s.access)-4],
	} {
		rec := e.do(t, request{method: http.MethodGet, path: "/me", header: map[string]string{"Authorization": header}})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", name, rec.Code)
		}
	}
	// A refresh token is not an access token.
	if rec := e.do(t, request{method: http.MethodGet, path: "/me", token: s.refresh}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh token used as access token: %d", rec.Code)
	}
	// Neither is an access token a refresh token.
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/refresh", cookie: s.access}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("access token used as refresh token: %d", rec.Code)
	}
}

func TestRequestBodyChecks(t *testing.T) {
	e := newAuthEnv(t, 0, nil)
	for name, tc := range map[string]struct {
		body        string
		contentType string
		want        int
		code        string
	}{
		"not json":      {`{"username":`, "application/json", http.StatusBadRequest, "invalid_request"},
		"wrong type":    {`{"username":"a","password":"b"}`, "text/plain", http.StatusUnsupportedMediaType, "unsupported_media_type"},
		"no type":       {`{"username":"a","password":"b"}`, "", http.StatusUnsupportedMediaType, "unsupported_media_type"},
		"trailing data": {`{"username":"a","password":"b"} {}`, "application/json", http.StatusBadRequest, "invalid_request"},
		"array":         {`[1]`, "application/json", http.StatusBadRequest, "invalid_request"},
		"too large":     {`{"username":"` + strings.Repeat("a", 5000) + `"}`, "application/json", http.StatusRequestEntityTooLarge, "payload_too_large"},
	} {
		req := httptest.NewRequest(http.MethodPost, apiBase+"/auth/login", strings.NewReader(tc.body))
		if tc.contentType != "" {
			req.Header.Set("Content-Type", tc.contentType)
		}
		rec := reply{httptest.NewRecorder()}
		e.router.ServeHTTP(rec.ResponseRecorder, req)
		if rec.Code != tc.want || rec.code() != tc.code {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestLoginRateLimitSendsRetryAfter(t *testing.T) {
	e := newAuthEnv(t, 0, nil)
	e.register(t, "erin")
	var limited reply
	for range 30 {
		rec := e.post(t, "/auth/login", map[string]any{"username": "erin", "password": "wrong password"})
		if rec.Code == http.StatusTooManyRequests {
			limited = rec
			break
		}
	}
	if limited.ResponseRecorder == nil {
		t.Fatal("no rate limit after 30 failures")
	}
	secs, err := strconv.Atoi(limited.Header().Get("Retry-After"))
	if err != nil || secs < 1 || limited.code() != "rate_limited" {
		t.Fatalf("429: %v %s", limited.Header(), limited.Body)
	}
	// The right password is refused as well while the user is locked out.
	if rec := e.login(t, "erin"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("login during lockout: %d", rec.Code)
	}
}

func TestLogout(t *testing.T) {
	e := newAuthEnv(t, 0, nil)

	s := e.register(t, "frank")
	rec := e.do(t, request{method: http.MethodPost, path: "/auth/logout", token: s.access})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	if c := rec.refreshCookie(); c == nil || c.MaxAge >= 0 || c.Path != apiBase+"/auth/refresh" {
		t.Fatalf("logout must clear the cookie on its path: %+v", c)
	}
	if got := e.do(t, request{method: http.MethodGet, path: "/me", token: s.access}); got.Code != http.StatusUnauthorized {
		t.Fatalf("access token after logout: %d", got.Code)
	}
	if got := e.do(t, request{method: http.MethodPost, path: "/auth/refresh", cookie: s.refresh}); got.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: %d", got.Code)
	}

	// With only the cookie, e.g. after the access token expired.
	s = e.register(t, "gina")
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/logout", cookie: s.refresh, token: "expired-or-bogus"}); rec.Code != http.StatusNoContent {
		t.Fatalf("logout by cookie: %d %s", rec.Code, rec.Body)
	}
	if got := e.do(t, request{method: http.MethodGet, path: "/me", token: s.access}); got.Code != http.StatusUnauthorized {
		t.Fatalf("access token after cookie logout: %d", got.Code)
	}

	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/logout"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("logout without credentials: %d", rec.Code)
	}
}

func TestSessionsAndPasswordOverHTTP(t *testing.T) {
	e := newAuthEnv(t, 0, nil)
	laptop := e.register(t, "hank")
	phoneLogin := e.login(t, "hank")
	phone := session{access: phoneLogin.json()["access_token"].(string), refresh: phoneLogin.refreshCookie().Value}

	list := e.do(t, request{method: http.MethodGet, path: "/me/sessions", token: laptop.access})
	var sessions []map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &sessions); err != nil || list.Code != http.StatusOK || len(sessions) != 2 {
		t.Fatalf("sessions: %d %s", list.Code, list.Body)
	}
	var other string
	current := 0
	for _, s := range sessions {
		if s["is_current"] == true {
			current++
		} else {
			other = s["id"].(string)
		}
	}
	if current != 1 || other == "" {
		t.Fatalf("exactly one session is current: %v", sessions)
	}

	if rec := e.do(t, request{method: http.MethodDelete, path: "/me/sessions/not-a-uuid", token: laptop.access}); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed id: %d", rec.Code)
	}
	if rec := e.do(t, request{method: http.MethodDelete, path: "/me/sessions/" + uuid.NewString(), token: laptop.access}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: %d", rec.Code)
	}
	if rec := e.do(t, request{method: http.MethodDelete, path: "/me/sessions/" + other, token: laptop.access}); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, request{method: http.MethodGet, path: "/me", token: phone.access}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session still works: %d", rec.Code)
	}

	// Another user cannot touch these sessions.
	ivy := e.register(t, "ivy")
	if rec := e.do(t, request{method: http.MethodDelete, path: "/me/sessions/" + other, token: ivy.access}); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign session: %d", rec.Code)
	}

	// Password change: a wrong current password is 403, not 401, and a change revokes the other sessions.
	phoneLogin = e.login(t, "hank")
	phone = session{access: phoneLogin.json()["access_token"].(string)}
	change := func(current, next string) reply {
		return e.do(t, request{method: http.MethodPut, path: "/me/password", token: laptop.access, body: map[string]any{"current_password": current, "new_password": next}})
	}
	if rec := change("wrong password", "another long password"); rec.Code != http.StatusForbidden || rec.code() != "invalid_credentials" {
		t.Fatalf("wrong current password: %d %s", rec.Code, rec.Body)
	}
	if rec := change(password, "short"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("weak new password: %d %s", rec.Code, rec.Body)
	}
	if rec := change(password, "another long password"); rec.Code != http.StatusNoContent {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, request{method: http.MethodGet, path: "/me", token: phone.access}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("other session survived a password change: %d", rec.Code)
	}
	if rec := e.do(t, request{method: http.MethodGet, path: "/me", token: laptop.access}); rec.Code != http.StatusOK {
		t.Fatalf("current session must survive: %d", rec.Code)
	}
	if rec := e.login(t, "hank"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password: %d", rec.Code)
	}

	// Revoke the others, then the security settings.
	e.post(t, "/auth/login", map[string]any{"username": "hank", "password": "another long password"})
	if rec := e.do(t, request{method: http.MethodDelete, path: "/me/sessions", token: laptop.access}); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke others: %d", rec.Code)
	}
	list = e.do(t, request{method: http.MethodGet, path: "/me/sessions", token: laptop.access})
	if err := json.Unmarshal(list.Body.Bytes(), &sessions); err != nil || len(sessions) != 1 {
		t.Fatalf("sessions after revoke: %s", list.Body)
	}
	patch := e.do(t, request{method: http.MethodPatch, path: "/me/security", token: laptop.access, body: map[string]any{"session_duration_days": 30}})
	if patch.Code != http.StatusOK || patch.json()["session_duration_days"].(float64) != 30 || patch.json()["two_factor_enabled"] != false {
		t.Fatalf("security: %d %s", patch.Code, patch.Body)
	}
	if rec := e.do(t, request{method: http.MethodPatch, path: "/me/security", token: laptop.access, body: map[string]any{"session_duration_days": 31}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid duration: %d", rec.Code)
	}
}

func TestTwoFactorOverHTTP(t *testing.T) {
	e := newAuthEnv(t, 0, nil)
	s := e.register(t, "jane")
	authed := func(path string, body any) reply {
		return e.do(t, request{method: http.MethodPost, path: path, token: s.access, body: body})
	}

	if rec := authed("/me/2fa/setup", map[string]any{"password": "wrong password"}); rec.Code != http.StatusForbidden || rec.code() != "invalid_credentials" {
		t.Fatalf("setup with wrong password: %d %s", rec.Code, rec.Body)
	}
	if rec := authed("/me/2fa/confirm", map[string]any{"code": "123456"}); rec.Code != http.StatusConflict {
		t.Fatalf("confirm without setup: %d %s", rec.Code, rec.Body)
	}
	setup := authed("/me/2fa/setup", map[string]any{"password": password})
	if setup.Code != http.StatusOK || !strings.HasPrefix(setup.json()["otpauth_uri"].(string), "otpauth://totp/") {
		t.Fatalf("setup: %d %s", setup.Code, setup.Body)
	}
	if setup.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("the secret must not be cached")
	}
	if rec := authed("/me/2fa/confirm", map[string]any{"code": "000000"}); rec.Code != http.StatusBadRequest || rec.code() != "invalid_two_factor_code" {
		t.Fatalf("confirm with wrong code: %d %s", rec.Code, rec.Body)
	}
	code, err := totp.GenerateCode(setup.json()["secret"].(string), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	confirm := authed("/me/2fa/confirm", map[string]any{"code": code})
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", confirm.Code, confirm.Body)
	}
	codes, _ := confirm.json()["recovery_codes"].([]any)
	if len(codes) < 8 {
		t.Fatalf("recovery codes: %s", confirm.Body)
	}

	// Login now ends in a challenge.
	login := e.login(t, "jane")
	challenge, _ := login.json()["login_challenge"].(string)
	if login.Code != http.StatusOK || login.json()["two_factor_required"] != true || challenge == "" || login.refreshCookie() != nil || login.json()["access_token"] != nil {
		t.Fatalf("login with 2fa: %d %s", login.Code, login.Body)
	}
	if rec := e.post(t, "/auth/login/2fa", map[string]any{"login_challenge": challenge, "code": "000000"}); rec.Code != http.StatusUnauthorized || rec.code() != "invalid_two_factor_code" {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body)
	}
	if rec := e.post(t, "/auth/login/2fa", map[string]any{"login_challenge": "made-up", "code": codes[0]}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown challenge: %d %s", rec.Code, rec.Body)
	}
	recovery := codes[0].(string)
	done := e.post(t, "/auth/login/2fa", map[string]any{"login_challenge": challenge, "code": recovery})
	if done.Code != http.StatusOK || done.refreshCookie() == nil || done.json()["access_token"] == nil {
		t.Fatalf("2fa login: %d %s", done.Code, done.Body)
	}
	// A recovery code works once.
	challenge = e.login(t, "jane").json()["login_challenge"].(string)
	if rec := e.post(t, "/auth/login/2fa", map[string]any{"login_challenge": challenge, "code": recovery}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("recovery code reused: %d", rec.Code)
	}

	if rec := authed("/me/2fa/disable", map[string]any{"password": "wrong password", "code": codes[1]}); rec.Code != http.StatusForbidden || rec.code() != "invalid_credentials" {
		t.Fatalf("disable with wrong password: %d %s", rec.Code, rec.Body)
	}
	if rec := authed("/me/2fa/disable", map[string]any{"password": password, "code": "000000"}); rec.Code != http.StatusForbidden || rec.code() != "invalid_two_factor_code" {
		t.Fatalf("disable with wrong code: %d %s", rec.Code, rec.Body)
	}
	if rec := authed("/me/2fa/disable", map[string]any{"password": password, "code": codes[1]}); rec.Code != http.StatusNoContent {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	if rec := e.login(t, "jane"); rec.Code != http.StatusOK || rec.json()["access_token"] == nil {
		t.Fatalf("login after disabling: %d %s", rec.Code, rec.Body)
	}
}

func TestLogsHoldNoCredentials(t *testing.T) {
	e := newAuthEnv(t, 0, nil)
	s := e.register(t, "kate")
	e.post(t, "/auth/login", map[string]any{"username": "kate", "password": "a-wrong-password-9"})
	e.do(t, request{method: http.MethodPost, path: "/auth/refresh", cookie: s.refresh})
	e.do(t, request{method: http.MethodGet, path: "/me", token: s.access})

	out := e.logs.String()
	for _, secret := range []string{password, "a-wrong-password-9", s.access, s.refresh} {
		if strings.Contains(out, secret) {
			t.Fatalf("a credential reached the logs: %s", out)
		}
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name, remote string
		header       map[string]string
		want         string
	}{
		{"direct client", "203.0.113.9:1000", nil, "203.0.113.9"},
		{"a direct client cannot choose its address", "203.0.113.9:1000", map[string]string{"X-Forwarded-For": "198.51.100.1", "X-Real-IP": "198.51.100.2"}, "203.0.113.9"},
		{"proxy on loopback", "127.0.0.1:1000", map[string]string{"X-Forwarded-For": "198.51.100.4"}, "198.51.100.4"},
		{"proxy chain", "10.0.0.2:1000", map[string]string{"X-Forwarded-For": "198.51.100.4, 10.0.0.9"}, "198.51.100.4"},
		{"forged left entries are ignored", "10.0.0.2:1000", map[string]string{"X-Forwarded-For": "1.2.3.4, 198.51.100.4"}, "198.51.100.4"},
		{"several header lines", "10.0.0.2:1000", map[string]string{"X-Forwarded-For": "198.51.100.4"}, "198.51.100.4"},
		{"real ip", "172.18.0.3:1000", map[string]string{"X-Real-IP": "198.51.100.5"}, "198.51.100.5"},
		{"garbage header", "127.0.0.1:1000", map[string]string{"X-Forwarded-For": "not an ip", "X-Real-IP": "also not"}, "127.0.0.1"},
		{"only private hops", "10.0.0.2:1000", map[string]string{"X-Forwarded-For": "192.168.1.5"}, "10.0.0.2"},
		{"ipv6", "[::1]:1000", map[string]string{"X-Forwarded-For": "2001:db8::7"}, "2001:db8::7"},
		{"mapped ipv4", "[::ffff:203.0.113.9]:1000", nil, "203.0.113.9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			req.RemoteAddr = tc.remote
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			if got := clientIP(req); got != netip.MustParseAddr(tc.want) {
				t.Fatalf("got %v, want %s", got, tc.want)
			}
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.RemoteAddr = "broken"
	if clientIP(req).IsValid() {
		t.Fatal("an unparsable peer has no address")
	}
}

func TestRouteAccess(t *testing.T) {
	for pattern, want := range map[string]access{
		"GET /api/v2/meta":                        accessPublic,
		"POST /api/v2/auth/login":                 accessPublic,
		"POST /api/v2/auth/refresh":               accessPublic,
		"POST /api/v2/auth/logout":                accessOptional,
		"GET /api/v2/me":                          accessRequired,
		"POST /api/v2/me/2fa/setup":               accessRequired,
		"DELETE /api/v2/me/sessions/{session_id}": accessRequired,
		"GET /api/v2/auth/login":                  accessRequired, // wrong method for a public path
		"POST /api/v2/auth/ws-ticket":             accessRequired,
		"POST /other/auth/login":                  accessRequired, // outside the base path
		"":                                        accessRequired,
	} {
		if got := accessFor(pattern, "/api/v2"); got != want {
			t.Errorf("%q: got %d, want %d", pattern, got, want)
		}
	}
}
