package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/myronsi/messenger-back/internal/geoip/geoiptest"
)

func (e *authEnv) startQR(t *testing.T) (id, poll string) {
	t.Helper()
	rec := e.do(t, request{method: http.MethodPost, path: "/auth/qr-login", remote: "198.51.100.4:5000", header: map[string]string{"User-Agent": "QR Laptop/2.0"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	body := rec.json()
	return body["request_id"].(string), body["poll_token"].(string)
}

func (e *authEnv) pollQR(t *testing.T, id, poll string) reply {
	t.Helper()
	return e.do(t, request{method: http.MethodPost, path: "/auth/qr-login/" + id + "/poll", body: map[string]any{"poll_token": poll}, remote: "198.51.100.4:5000", header: map[string]string{"User-Agent": "QR Laptop/2.0"}})
}

func pollStatus(t *testing.T, rec reply) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("poll: %d %s", rec.Code, rec.Body)
	}
	s, _ := rec.json()["status"].(string)
	return s
}

func TestQRLoginOverHTTP(t *testing.T) {
	e := newAuthEnvWithGeoIP(t, 0, nil, geoiptest.Default(t))
	phone := e.register(t, "qr_http")
	stranger := e.register(t, "qr_http_other")

	id, poll := e.startQR(t)
	if st := pollStatus(t, e.pollQR(t, id, poll)); st != "pending" {
		t.Fatalf("status %s", st)
	}
	if rec := e.do(t, request{method: http.MethodGet, path: "/auth/qr-login/" + id}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("scan without a token: %d", rec.Code)
	}
	scan := e.do(t, request{method: http.MethodGet, path: "/auth/qr-login/" + id, token: phone.access})
	if scan.Code != http.StatusOK || scan.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("scan: %d %s", scan.Code, scan.Body)
	}
	info := scan.json()
	loc, _ := json.Marshal(info["location"])
	if info["status"] != "scanned" || info["device"] != "QR Laptop/2.0" || info["request_id"] != id || string(loc) != `{"city":"Paris","country":"France","country_code":"FR"}` {
		t.Fatalf("scan body: %s", scan.Body)
	}
	assertNoAddress(t, scan.Body.String())
	if rec := e.do(t, request{method: http.MethodGet, path: "/auth/qr-login/" + id, token: stranger.access}); rec.Code != http.StatusNotFound || rec.code() != "not_found" {
		t.Fatalf("scan by another account: %d %s", rec.Code, rec.Body)
	}
	if st := pollStatus(t, e.pollQR(t, id, poll)); st != "scanned" {
		t.Fatalf("status %s", st)
	}

	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/qr-login/" + id + "/approve", token: phone.access}); rec.Code != http.StatusNoContent {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/qr-login/" + id + "/approve", token: phone.access, body: map[string]any{}}); rec.Code != http.StatusConflict || rec.code() != "conflict" {
		t.Fatalf("approve twice: %d %s", rec.Code, rec.Body)
	}
	if rec := e.pollQR(t, id, "wrong-poll-token-wrong-poll-token-wrong-poll"); pollStatus(t, rec) != "expired" || rec.refreshCookie() != nil {
		t.Fatalf("wrong poll token: %s", rec.Body)
	}

	done := e.pollQR(t, id, poll)
	if pollStatus(t, done) != "approved" || done.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("approved poll: %s", done.Body)
	}
	body := done.json()
	if body["token_type"] != "Bearer" || body["expires_in"] == nil || len(body) != 4 {
		t.Fatalf("login fields: %s", done.Body)
	}
	cookie := done.refreshCookie()
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.Path != apiBase+"/auth/refresh" {
		t.Fatalf("refresh cookie: %+v", cookie)
	}
	access := body["access_token"].(string)
	me := e.do(t, request{method: http.MethodGet, path: "/me", token: access})
	if me.Code != http.StatusOK || me.json()["username"] != "qr_http" {
		t.Fatalf("me: %d %s", me.Code, me.Body)
	}
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/refresh", cookie: cookie.Value}); rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body)
	}
	if again := e.pollQR(t, id, poll); pollStatus(t, again) != "expired" || again.refreshCookie() != nil {
		t.Fatalf("second poll: %s", again.Body)
	}
	if rec := e.do(t, request{method: http.MethodGet, path: "/auth/qr-login/" + id, token: phone.access}); rec.Code != http.StatusNotFound {
		t.Fatalf("used request: %d", rec.Code)
	}

	events := e.do(t, request{method: http.MethodGet, path: "/me/security-events", token: phone.access})
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(events.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range page.Items {
		if typ := item["type"].(string); strings.HasPrefix(typ, "qr_login") {
			l, _ := item["location"].(map[string]any)
			if item["user_agent"] != "QR Laptop/2.0" || l["city"] != "Paris" {
				t.Fatalf("%s event: %v", typ, item)
			}
			seen[typ] = true
		}
	}
	if !seen["qr_login_approved"] || !seen["qr_login"] {
		t.Fatalf("events: %s", events.Body)
	}

	denied, deniedPoll := e.startQR(t)
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/qr-login/" + denied + "/deny", token: phone.access}); rec.Code != http.StatusNoContent {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body)
	}
	if st := pollStatus(t, e.pollQR(t, denied, deniedPoll)); st != "denied" {
		t.Fatalf("status after deny %s", st)
	}
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/qr-login/" + denied + "/deny", token: phone.access}); rec.Code != http.StatusConflict {
		t.Fatalf("deny twice: %d", rec.Code)
	}
	unknown := strings.Repeat("Q", 43)
	for _, op := range []request{
		{method: http.MethodGet, path: "/auth/qr-login/" + unknown, token: phone.access},
		{method: http.MethodPost, path: "/auth/qr-login/" + unknown + "/approve", token: phone.access},
		{method: http.MethodPost, path: "/auth/qr-login/" + unknown + "/deny", token: phone.access},
	} {
		if rec := e.do(t, op); rec.Code != http.StatusNotFound || rec.code() != "not_found" {
			t.Fatalf("%s %s: %d %s", op.method, op.path, rec.Code, rec.Body)
		}
	}
	if st := pollStatus(t, e.pollQR(t, unknown, poll)); st != "expired" {
		t.Fatalf("unknown request %s", st)
	}
}

func TestQRLoginTwoFactorOverHTTP(t *testing.T) {
	e := newAuthEnv(t, 0, nil)
	phone := e.register(t, "qr_http_tfa")
	authed := func(path string, body any) reply {
		return e.do(t, request{method: http.MethodPost, path: path, token: phone.access, body: body})
	}
	setup := authed("/me/2fa/setup", map[string]any{"password": password})
	code, err := totp.GenerateCode(setup.json()["secret"].(string), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	confirm := authed("/me/2fa/confirm", map[string]any{"code": code})
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", confirm.Code, confirm.Body)
	}
	codes := confirm.json()["recovery_codes"].([]any)

	id, poll := e.startQR(t)
	path := "/auth/qr-login/" + id + "/approve"
	if rec := authed(path, nil); rec.Code != http.StatusUnauthorized || rec.code() != "two_factor_required" {
		t.Fatalf("missing code: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, request{method: http.MethodGet, path: "/me", token: phone.access}); rec.Code != http.StatusOK {
		t.Fatal("two_factor_required ended the session")
	}
	if st := pollStatus(t, e.pollQR(t, id, poll)); st != "pending" {
		t.Fatalf("two_factor_required consumed the request: %s", st)
	}
	if rec := authed(path, map[string]any{"code": "000000"}); rec.Code != http.StatusBadRequest || rec.code() != "invalid_credentials" {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body)
	}
	if rec := authed(path, map[string]any{"code": codes[0]}); rec.Code != http.StatusNoContent {
		t.Fatalf("approve with code: %d %s", rec.Code, rec.Body)
	}
	if st := pollStatus(t, e.pollQR(t, id, poll)); st != "approved" {
		t.Fatalf("status %s", st)
	}
	if rec := authed(path, map[string]any{"code": "123456"}); rec.Code != http.StatusNotFound {
		t.Fatalf("used request approvable: %d", rec.Code)
	}
}

func TestQRLoginStaleSessionAndRateLimitsOverHTTP(t *testing.T) {
	e := newAuthEnv(t, 0, nil)
	phone := e.register(t, "qr_http_stale")
	id, poll := e.startQR(t)
	if rec := e.do(t, request{method: http.MethodGet, path: "/me", token: phone.access}); rec.Code != http.StatusOK {
		t.Fatalf("me: %d", rec.Code)
	}
	if _, err := e.store.Pool().Exec(context.Background(), `UPDATE user_sessions SET last_active_at=now()-interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/qr-login/" + id + "/approve", token: phone.access}); rec.Code != http.StatusForbidden || rec.code() != "forbidden" {
		t.Fatalf("idle session: %d %s", rec.Code, rec.Body)
	}
	if st := pollStatus(t, e.pollQR(t, id, poll)); st != "pending" {
		t.Fatalf("refused approval changed the request: %s", st)
	}
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/logout", token: phone.access}); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/qr-login/" + id + "/approve", token: phone.access}); rec.Code != http.StatusUnauthorized || rec.code() != "unauthenticated" {
		t.Fatalf("revoked session: %d %s", rec.Code, rec.Body)
	}

	for range 9 {
		e.startQR(t)
	}
	limited := e.do(t, request{method: http.MethodPost, path: "/auth/qr-login", remote: "198.51.100.4:5000"})
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" || limited.code() != "rate_limited" {
		t.Fatalf("create limit: %d %s", limited.Code, limited.Body)
	}
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/qr-login", remote: "198.51.100.5:5000"}); rec.Code != http.StatusCreated {
		t.Fatalf("other address: %d", rec.Code)
	}
	var last reply
	for range 121 {
		last = e.pollQR(t, id, "x")
	}
	if last.Code != http.StatusTooManyRequests || last.code() != "rate_limited" {
		t.Fatalf("poll limit: %d %s", last.Code, last.Body)
	}
}
