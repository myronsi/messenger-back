package internalapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const token = "0123456789abcdef0123456789abcdef"

func call(t *testing.T, h http.Handler, method, path, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPingNeedsTheServiceToken(t *testing.T) {
	h := Handler(Options{Token: token, Version: "1.2.3"})

	for name, auth := range map[string]string{
		"missing":      "",
		"wrong token":  "Bearer nope",
		"wrong scheme": "Basic " + token,
	} {
		if rec := call(t, h, http.MethodGet, BasePath+"/ping", auth); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
	}

	rec := call(t, h, http.MethodGet, BasePath+"/ping", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["service"] != "messenger-api" || body["version"] != "1.2.3" {
		t.Fatalf("body = %v", body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("internal responses must not be cached")
	}
}

func TestEmptyTokenRejectsEverything(t *testing.T) {
	h := Handler(Options{})
	if rec := call(t, h, http.MethodGet, BasePath+"/ping", "Bearer "); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestUnknownRouteAfterAuth(t *testing.T) {
	h := Handler(Options{Token: token})
	if rec := call(t, h, http.MethodGet, BasePath+"/nothing", "Bearer "+token); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
