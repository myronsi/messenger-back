package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/auth"
)

type fakeProofs struct {
	calls      []string
	err        error
	thumbprint string
}

func (f *fakeProofs) VerifyDeviceProof(_ context.Context, raw, method, operation string) (auth.DeviceKey, error) {
	f.calls = append(f.calls, raw+"|"+method+"|"+operation)
	if f.err != nil {
		return auth.DeviceKey{}, f.err
	}
	return auth.DeviceKey{Thumbprint: f.thumbprint}, nil
}

func serveWithProof(v DeviceProofVerifier, method, path, proof string) (*httptest.ResponseRecorder, *auth.DeviceKey, bool) {
	var seen *auth.DeviceKey
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		seen = deviceKeyFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(method, path, nil)
	if proof != "" {
		req.Header.Set(auth.DeviceProofHeader, proof)
	}
	rec := httptest.NewRecorder()
	verifyDeviceProof(v, "/api/v2", slog.New(slog.DiscardHandler))(next).ServeHTTP(rec, req)
	return rec, seen, reached
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("problem body %q: %v", rec.Body.String(), err)
	}
	return p.Code
}

func TestDeviceProofMiddlewarePassesTheVerifiedKey(t *testing.T) {
	f := &fakeProofs{thumbprint: strings.Repeat("t", 43)}
	rec, key, reached := serveWithProof(f, http.MethodPost, "/api/v2/auth/refresh", "proof")
	if !reached || rec.Code != http.StatusNoContent || key == nil || key.Thumbprint != f.thumbprint {
		t.Fatalf("reached %v code %d key %+v", reached, rec.Code, key)
	}
	if len(f.calls) != 1 || f.calls[0] != "proof|POST|/auth/refresh" {
		t.Fatalf("verifier calls %v", f.calls)
	}
}

func TestDeviceProofMiddlewareIgnoresOtherRequests(t *testing.T) {
	f := &fakeProofs{thumbprint: strings.Repeat("t", 43)}
	for _, tc := range []struct{ method, path, proof string }{
		{http.MethodPost, "/api/v2/auth/refresh", ""},
		{http.MethodGet, "/api/v2/auth/qr-login/abc", "proof"},
		{http.MethodPost, "/api/v2/chats", "proof"},
		{http.MethodPost, "/auth/refresh", "proof"},
	} {
		rec, key, reached := serveWithProof(f, tc.method, tc.path, tc.proof)
		if !reached || rec.Code != http.StatusNoContent || key != nil {
			t.Fatalf("%s %s: reached %v code %d key %+v", tc.method, tc.path, reached, rec.Code, key)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("verifier must not run: %v", f.calls)
	}
	if _, key, reached := serveWithProof(nil, http.MethodPost, "/api/v2/auth/refresh", "proof"); !reached || key != nil {
		t.Fatal("without a verifier the header is ignored")
	}
}

func TestDeviceProofMiddlewareErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{auth.ErrDeviceProofStale, http.StatusUnauthorized, string(ErrorCodeDeviceProofStale)},
		{auth.ErrInvalidDeviceProof, http.StatusBadRequest, string(ErrorCodeInvalidRequest)},
		{errors.New("redis down"), http.StatusInternalServerError, string(ErrorCodeInternalError)},
	} {
		rec, _, reached := serveWithProof(&fakeProofs{err: tc.err}, http.MethodPost, "/api/v2/auth/login", "proof")
		if reached || rec.Code != tc.status || problemCode(t, rec) != tc.code {
			t.Fatalf("%v: reached %v code %d body %s", tc.err, reached, rec.Code, rec.Body.String())
		}
	}
}

func TestCORSAllowsTheDeviceProofHeaderAndExposesDate(t *testing.T) {
	h := cors([]string{"https://app.example"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodOptions, "/api/v2/auth/refresh", nil)
	req.Header.Set("Origin", "https://app.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), auth.DeviceProofHeader) {
		t.Fatalf("allow headers %q", rec.Header().Get("Access-Control-Allow-Headers"))
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "Date") {
		t.Fatalf("expose headers %q", rec.Header().Get("Access-Control-Expose-Headers"))
	}
}
