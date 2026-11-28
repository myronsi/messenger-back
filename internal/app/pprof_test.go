package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPprofHandlerServesTheIndexOnly(t *testing.T) {
	h := PprofHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", http.NoBody))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatalf("index: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other paths: %d", rec.Code)
	}
}

func TestStartPprofIsOffWithoutAnAddress(t *testing.T) {
	StartPprof(nil, "")()
}
