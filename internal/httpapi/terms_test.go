package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRegisterRecordsTermsAndMeReturnsThem(t *testing.T) {
	e := newAuthEnv(t, 0, nil)

	rec := e.post(t, "/auth/register", map[string]any{"username": "terms_ok", "password": password, "terms_version": "2026-11-19", "age_confirmed": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	me := e.do(t, request{method: http.MethodGet, path: "/me", token: rec.json()["access_token"].(string)})
	if me.Code != http.StatusOK || me.json()["terms_version"] != "2026-11-19" {
		t.Fatalf("me: %d %s", me.Code, me.Body)
	}
	at, err := time.Parse(time.RFC3339Nano, me.json()["terms_accepted_at"].(string))
	if err != nil || time.Since(at) > time.Minute || at.Location() != time.UTC {
		t.Fatalf("terms_accepted_at: %v %s", err, me.Body)
	}

	rec = e.post(t, "/auth/register", map[string]any{"username": "terms_none", "password": password})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register without terms: %d %s", rec.Code, rec.Body)
	}
	me = e.do(t, request{method: http.MethodGet, path: "/me", token: rec.json()["access_token"].(string)})
	body := me.json()
	for _, key := range []string{"terms_version", "terms_accepted_at"} {
		if v, ok := body[key]; !ok || v != nil {
			t.Fatalf("%s must be present and null: %s", key, me.Body)
		}
	}

	rec = e.post(t, "/auth/register", map[string]any{"username": "terms_young", "password": password, "terms_version": "2026-11-19", "age_confirmed": false})
	if rec.Code != http.StatusUnprocessableEntity || rec.code() != "validation_failed" || !strings.Contains(rec.Body.String(), `"field":"age_confirmed"`) {
		t.Fatalf("age not confirmed: %d %s", rec.Code, rec.Body)
	}
}
