package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUnknownHTTPMethodsShareOneSeries(t *testing.T) {
	m := NewMetrics()
	m.ObserveRequest("GET", "GET /x", 200, time.Millisecond)
	m.ObserveRequest("BREW", "unmatched", 404, time.Millisecond)
	m.ObserveRequest("PROPFIND", "unmatched", 404, time.Millisecond)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	body := rec.Body.String()
	for _, leaked := range []string{`method="BREW"`, `method="PROPFIND"`} {
		if strings.Contains(body, leaked) {
			t.Fatalf("metrics expose client-chosen method label %s", leaked)
		}
	}
	if !strings.Contains(body, `method="OTHER"`) || !strings.Contains(body, `method="GET"`) {
		t.Fatal("expected GET and OTHER method labels")
	}
}

func TestModerationMetrics(t *testing.T) {
	m := NewMetrics()
	m.ModerationStatusChange("limited")
	m.ModerationStatusChange("limited")
	m.UserReport("spam", "full")
	m.ContentReport("message", "illegal")

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	body := rec.Body.String()
	for _, want := range []string{
		`messenger_moderation_status_changes_total{status="limited"} 2`,
		`messenger_user_reports_total{reason="spam",weight_bucket="full"} 1`,
		`messenger_content_reports_total{kind="message",reason="illegal"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics lack %s", want)
		}
	}
}
