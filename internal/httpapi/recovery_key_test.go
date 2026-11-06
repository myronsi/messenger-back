package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestRecoveryKeyOverHTTP(t *testing.T) {
	e := newAuthEnv(t, 0, nil)
	registered := e.post(t, "/auth/register", map[string]any{"username": "recovery_http", "display_name": "Recovery HTTP", "password": password})
	if registered.Code != 201 {
		t.Fatalf("registration status %d", registered.Code)
	}
	key, ok := registered.json()["recovery_key"].(string)
	if !ok || key == "" {
		t.Fatal("one-time registration key missing")
	}
	token := registered.json()["access_token"].(string)
	status := e.do(t, request{method: http.MethodGet, path: "/me/recovery-key", token: token})
	if status.Code != 200 || status.json()["exists"] != true {
		t.Fatal("key status missing")
	}
	if _, ok := status.json()["key_hash"]; ok {
		t.Fatal("status leaks hash")
	}
	for _, name := range []string{"unknown_recovery", "recovery_http"} {
		bad := e.post(t, "/auth/recover/key", map[string]any{"username": name, "recovery_key": "bad", "new_password": "new password"})
		if bad.Code != 400 || bad.code() != "invalid_credentials" {
			t.Fatalf("public possession failure differs: %d %s", bad.Code, bad.code())
		}
	}
	reset := e.post(t, "/auth/recover/key", map[string]any{"username": "recovery_http", "recovery_key": key, "new_password": "new password"})
	if reset.Code != 200 {
		t.Fatalf("reset status %d: %s", reset.Code, reset.Body)
	}
	if reset.Header().Get("Cache-Control") != "no-store" || reset.refreshCookie() == nil {
		t.Fatal("reset must be non-cacheable and set session cookie")
	}
	replacement, ok := reset.json()["recovery_key"].(string)
	if !ok || replacement == key || replacement == "" {
		t.Fatal("replacement key missing")
	}
	old := e.do(t, request{method: http.MethodGet, path: "/me", token: token})
	if old.Code != 401 {
		t.Fatal("old session still valid")
	}
	newToken := reset.json()["access_token"].(string)
	events := e.do(t, request{method: http.MethodGet, path: "/me/security-events", token: newToken})
	if events.Code != 200 {
		t.Fatal("security events unavailable")
	}
	locked := e.do(t, request{method: http.MethodPost, path: "/me/recovery-key", token: newToken, body: map[string]any{"password": "new password"}})
	if locked.Code != 409 {
		t.Fatalf("cooldown status %d", locked.Code)
	}
	unauth := e.do(t, request{method: http.MethodGet, path: "/me/recovery-key"})
	if unauth.Code != 401 {
		t.Fatal("key metadata must require authentication")
	}
}

func TestTwoFactorRequiredOverHTTP(t *testing.T) {
	e := newAuthEnv(t, 0, nil)
	registered := e.post(t, "/auth/register", map[string]any{"username": "tfr_http", "display_name": "TFR HTTP", "password": password})
	if registered.Code != 201 {
		t.Fatalf("registration status %d", registered.Code)
	}
	key := registered.json()["recovery_key"].(string)
	token := registered.json()["access_token"].(string)
	authed := func(path string, body any) reply {
		return e.do(t, request{method: http.MethodPost, path: path, token: token, body: body})
	}
	setup := authed("/me/2fa/setup", map[string]any{"password": password})
	if setup.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", setup.Code, setup.Body)
	}
	code, err := totp.GenerateCode(setup.json()["secret"].(string), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	confirm := authed("/me/2fa/confirm", map[string]any{"code": code})
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", confirm.Code, confirm.Body)
	}
	codes := confirm.json()["recovery_codes"].([]any)

	if rec := authed("/me/recovery-key", map[string]any{"password": "wrong password"}); rec.Code != http.StatusForbidden || rec.code() != "invalid_credentials" {
		t.Fatalf("wrong password without code: %d %s", rec.Code, rec.Body)
	}
	if rec := authed("/me/recovery-key", map[string]any{"password": password}); rec.Code != http.StatusUnauthorized || rec.code() != "two_factor_required" {
		t.Fatalf("missing code: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, request{method: http.MethodGet, path: "/me", token: token}); rec.Code != http.StatusOK {
		t.Fatal("two_factor_required ended the session")
	}
	if rec := authed("/me/recovery-key", map[string]any{"password": password, "code": "000000"}); rec.Code != http.StatusBadRequest || rec.code() != "invalid_credentials" {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body)
	}

	if rec := e.post(t, "/auth/recover/key", map[string]any{"username": "tfr_http", "recovery_key": "bad", "new_password": "new password"}); rec.Code != http.StatusBadRequest || rec.code() != "invalid_credentials" {
		t.Fatalf("invalid key without code: %d %s", rec.Code, rec.Body)
	}
	if rec := e.post(t, "/auth/recover/key", map[string]any{"username": "tfr_http", "recovery_key": key, "new_password": "new password"}); rec.Code != http.StatusUnauthorized || rec.code() != "two_factor_required" {
		t.Fatalf("missing code: %d %s", rec.Code, rec.Body)
	}
	if rec := e.post(t, "/auth/recover/key", map[string]any{"username": "tfr_http", "recovery_key": key, "new_password": "new password", "code": "000000"}); rec.Code != http.StatusBadRequest || rec.code() != "invalid_credentials" {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body)
	}
	if rec := e.post(t, "/auth/recover/key", map[string]any{"username": "tfr_http", "recovery_key": key, "new_password": "new password", "code": codes[0]}); rec.Code != http.StatusOK {
		t.Fatalf("key not usable after two_factor_required: %d %s", rec.Code, rec.Body)
	}
}
