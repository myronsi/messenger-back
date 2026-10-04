package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/observability"
)

type testEnv struct {
	router *Router
	logs   *bytes.Buffer
}

type panicAPI struct{ Unimplemented }

func (panicAPI) GetMeta(http.ResponseWriter, *http.Request) { panic("secret-in-panic-value") }

func newTestEnv(t *testing.T, mutate func(*Options)) testEnv {
	t.Helper()
	logs := &bytes.Buffer{}
	m := observability.NewMetrics()
	o := Options{
		HTTP: config.HTTP{
			BasePath:         "/api/v2",
			RequestTimeout:   time.Second,
			ReadinessTimeout: 200 * time.Millisecond,
			MaxBodyBytes:     64,
			CORSOrigins:      []string{"https://app.example.com"},
		},
		Log:     observability.NewLogger(logs, slog.LevelDebug, "test"),
		Metrics: m,
		Checks: []Check{
			{Name: "postgres", Ping: func(context.Context) error { return nil }},
			{Name: "redis", Ping: func(context.Context) error { return nil }},
		},
	}
	if mutate != nil {
		mutate(&o)
	}
	return testEnv{router: NewRouter(o), logs: logs}
}

func (e testEnv) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e testEnv) get(path string) *httptest.ResponseRecorder {
	return e.do(httptest.NewRequest(http.MethodGet, path, http.NoBody))
}

func TestHealthz(t *testing.T) {
	e := newTestEnv(t, func(o *Options) {
		o.Checks = []Check{{Name: "postgres", Ping: func(context.Context) error { return errors.New("down") }}}
	})
	if rec := e.get("/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("healthz must not depend on stores, got %d", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	e := newTestEnv(t, nil)
	rec := e.get("/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	e = newTestEnv(t, func(o *Options) {
		o.Checks = append(o.Checks,
			Check{Name: "scylla", Ping: func(context.Context) error { return errors.New("dial tcp 10.0.0.5:9042: refused") }},
			Check{Name: "elasticsearch", Ping: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }},
		)
	})
	rec = e.get("/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Status string
		Checks map[string]string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Checks["postgres"] != "ok" || body.Checks["scylla"] != "unavailable" || body.Checks["elasticsearch"] != "unavailable" {
		t.Fatalf("checks = %v", body.Checks)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatal("readyz must not expose error text")
	}
	if m := e.get("/metrics").Body.String(); !strings.Contains(m, `messenger_store_errors_total{store="scylla"} 1`) {
		t.Fatalf("store error not counted:\n%s", m)
	}
}

func TestReadyzDraining(t *testing.T) {
	e := newTestEnv(t, nil)
	e.router.Drain()
	if rec := e.get("/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("draining readyz = %d", rec.Code)
	}
	if rec := e.get("/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("draining healthz = %d", rec.Code)
	}
}

func TestContractIsServedWithProblems(t *testing.T) {
	e := newTestEnv(t, nil)

	rec := e.get("/api/v2/meta")
	if rec.Code != http.StatusNotImplemented || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unimplemented operation: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("body = %s", rec.Body)
	}

	rec = e.get("/api/v2/nope")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unknown route: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	rec = e.do(httptest.NewRequest(http.MethodDelete, "/api/v2/meta", http.NoBody))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") == "" {
		t.Fatalf("wrong method: %d allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
	if rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("405 content type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestMetrics(t *testing.T) {
	e := newTestEnv(t, nil)
	e.get("/api/v2/meta")
	e.get("/some/unknown/path/123")
	rec := e.get("/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`messenger_http_requests_total{method="GET",route="/api/v2/meta",status="501"} 1`,
		`messenger_http_requests_total{method="GET",route="unmatched",status="404"} 1`,
		"messenger_http_request_duration_seconds_bucket",
		"messenger_http_requests_in_flight",
		"messenger_websocket_connections_open",
		"messenger_messages_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	if strings.Contains(body, "/123") {
		t.Error("raw paths must not become label values")
	}
}

func TestRequestID(t *testing.T) {
	e := newTestEnv(t, nil)

	rec := e.get("/healthz")
	if id := rec.Header().Get(RequestIDHeader); len(id) != 32 {
		t.Fatalf("generated id = %q", id)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
	req.Header.Set(RequestIDHeader, "client-id-12345")
	if id := e.do(req).Header().Get(RequestIDHeader); id != "client-id-12345" {
		t.Fatalf("client id not kept: %q", id)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
	req.Header.Set(RequestIDHeader, "bad id\nwith newline")
	if id := e.do(req).Header().Get(RequestIDHeader); strings.Contains(id, "bad") {
		t.Fatalf("malformed id kept: %q", id)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newTestEnv(t, nil)
	h := e.get("/healthz").Header()
	for k, v := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	} {
		if h.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, h.Get(k), v)
		}
	}
	if h.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must only be set in production")
	}
	e = newTestEnv(t, func(o *Options) { o.Production = true })
	if e.get("/healthz").Header().Get("Strict-Transport-Security") == "" {
		t.Error("HSTS missing in production")
	}
}

func TestCORS(t *testing.T) {
	e := newTestEnv(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
	req.Header.Set("Origin", "https://app.example.com")
	h := e.do(req).Header()
	if h.Get("Access-Control-Allow-Origin") != "https://app.example.com" || h.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("allowed origin headers: %v", h)
	}

	req = httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
	req.Header.Set("Origin", "https://evil.example.com")
	if got := e.do(req).Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("disallowed origin got %q", got)
	}

	req = httptest.NewRequest(http.MethodOptions, "/api/v2/chats", http.NoBody)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := e.do(req)
	if rec.Code != http.StatusNoContent || !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}

	req = httptest.NewRequest(http.MethodOptions, "/api/v2/chats", http.NoBody)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	if rec := e.do(req); rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed preflight: %d", rec.Code)
	}
}

func TestBodyLimit(t *testing.T) {
	e := newTestEnv(t, nil)
	big := strings.Repeat("x", 100)

	req := httptest.NewRequest(http.MethodPost, "/api/v2/auth/login", strings.NewReader(big))
	if rec := e.do(req); rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "payload_too_large") {
		t.Fatalf("content-length limit: %d %s", rec.Code, rec.Body)
	}

	// Chunked body without a Content-Length: the reader enforces the limit.
	var reached error
	e = newTestEnv(t, func(o *Options) { o.API = bodyReaderAPI{err: &reached} })
	req = httptest.NewRequest(http.MethodPost, "/api/v2/auth/login", io.NopCloser(strings.NewReader(big)))
	req.ContentLength = -1
	e.do(req)
	var tooLarge *http.MaxBytesError
	if !errors.As(reached, &tooLarge) {
		t.Fatalf("chunked body not limited: %v", reached)
	}
}

type bodyReaderAPI struct {
	Unimplemented
	err *error
}

func (b bodyReaderAPI) Login(_ http.ResponseWriter, r *http.Request, _ LoginParams) {
	_, *b.err = io.ReadAll(r.Body)
}

func TestRequestTimeoutAppliesToContext(t *testing.T) {
	var deadline time.Time
	var ok bool
	e := newTestEnv(t, func(o *Options) { o.API = deadlineAPI{deadline: &deadline, ok: &ok} })
	e.get("/api/v2/meta")
	if !ok || time.Until(deadline) > time.Second {
		t.Fatalf("no request deadline: ok=%v", ok)
	}
}

func TestUpgradeHeaderDoesNotSkipRequestTimeout(t *testing.T) {
	var deadline time.Time
	var ok bool
	e := newTestEnv(t, func(o *Options) { o.API = deadlineAPI{deadline: &deadline, ok: &ok} })
	req := httptest.NewRequest(http.MethodGet, "/api/v2/meta", http.NoBody)
	req.Header.Set("Upgrade", "websocket")
	e.do(req)
	if !ok {
		t.Fatal("a client-supplied Upgrade header removed the request deadline")
	}
}

func TestFlushRecordsImplicitOK(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	rec.Flush()
	if rec.status != http.StatusOK || !rec.wrote {
		t.Fatalf("status=%d wrote=%v after Flush", rec.status, rec.wrote)
	}
}

type deadlineAPI struct {
	Unimplemented
	deadline *time.Time
	ok       *bool
}

func (d deadlineAPI) GetMeta(_ http.ResponseWriter, r *http.Request) {
	*d.deadline, *d.ok = r.Context().Deadline()
}

func TestPanicRecovery(t *testing.T) {
	e := newTestEnv(t, func(o *Options) { o.API = panicAPI{} })
	rec := e.get("/api/v2/meta")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal_error") {
		t.Fatalf("panic response: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(e.logs.String(), "secret-in-panic-value") {
		t.Fatal("the panic value must not be logged")
	}
	if !strings.Contains(e.logs.String(), "panic in handler") {
		t.Fatal("panic not logged")
	}
}

func TestAccessLogHasNoSensitiveData(t *testing.T) {
	e := newTestEnv(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v2/chats/12345/messages?token=query-secret&q=hello+world", http.NoBody)
	req.Header.Set("Authorization", "Bearer bearer-secret")
	req.Header.Set("Cookie", "refresh=cookie-secret")
	e.do(req)

	out := e.logs.String()
	if !strings.Contains(out, `"route":"/api/v2/chats/{chat_id}/messages"`) || !strings.Contains(out, `"request_id"`) {
		t.Fatalf("access log incomplete: %s", out)
	}
	for _, leak := range []string{"query-secret", "bearer-secret", "cookie-secret", "hello", "12345"} {
		if strings.Contains(out, leak) {
			t.Fatalf("access log leaks %q: %s", leak, out)
		}
	}
}
