package observability

import (
	"context"
	"errors"
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

func TestPerformanceMetrics(t *testing.T) {
	m := NewMetrics()
	m.WebSocketEvent("message", "ack", 3*time.Millisecond)
	m.WebSocketSlowClose()
	m.FanoutPublished(42, time.Millisecond)
	m.FanoutRendered("message", 2*time.Millisecond)
	m.StoreOperation("postgres", "GetUser", time.Millisecond, nil)
	m.StoreOperation("scylla", "select_messages", time.Millisecond, errors.New("timeout"))
	m.RegisterStreamBacklog(func(context.Context) ([]StreamGroupStat, error) {
		return []StreamGroupStat{{Stream: "events:messages", Group: "search", Lag: 7, Pending: 2}}, nil
	})

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	body := rec.Body.String()
	for _, want := range []string{
		`messenger_websocket_event_duration_seconds_count{outcome="ack",type="message"} 1`,
		`messenger_websocket_slow_closes_total 1`,
		`messenger_fanout_recipients_sum 42`,
		`messenger_fanout_render_duration_seconds_count{event="message"} 1`,
		`messenger_store_operation_duration_seconds_count{op="GetUser",store="postgres"} 1`,
		`messenger_store_errors_total{store="scylla"} 1`,
		`messenger_stream_lag{group="search",stream="events:messages"} 7`,
		`messenger_stream_pending{group="search",stream="events:messages"} 2`,
		`messenger_stream_backlog_read_failed 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics lack %s", want)
		}
	}
	if strings.Contains(body, `messenger_store_errors_total{store="postgres"}`) {
		t.Fatal("a successful operation was counted as a store error")
	}
}

func TestStreamBacklogReportsReadFailures(t *testing.T) {
	m := NewMetrics()
	m.RegisterStreamBacklog(func(context.Context) ([]StreamGroupStat, error) { return nil, errors.New("redis down") })
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	if !strings.Contains(rec.Body.String(), `messenger_stream_backlog_read_failed 1`) {
		t.Fatal("a failed backlog read is not reported")
	}
}
