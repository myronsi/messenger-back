package httpapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/myronsi/messenger-back/internal/observability"
)

// RequestIDHeader carries the request ID in requests and responses.
const RequestIDHeader = "X-Request-ID"

const unmatchedRoute = "unmatched"

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

type middleware func(http.Handler) http.Handler

func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// requestState is shared between the middleware and the router: the router records the matched
// route pattern, which the access log and the metrics use instead of the raw path.
type requestState struct{ route string }

type stateKey struct{}

// routeMux records the matched pattern of every registered handler. Using the pattern as the
// route label keeps the metric cardinality bounded and keeps IDs out of the logs.
type routeMux struct{ *http.ServeMux }

func (m routeMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	m.Handle(pattern, http.HandlerFunc(h))
}

func (m routeMux) Handle(pattern string, h http.Handler) {
	route := pattern
	if _, path, ok := strings.Cut(pattern, " "); ok {
		route = path
	}
	m.ServeMux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st, ok := r.Context().Value(stateKey{}).(*requestState); ok {
			st.route = route
		}
		trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("http.route", route))
		h.ServeHTTP(w, r)
	}))
}

// statusRecorder captures the status and size of a response and keeps Flush, Hijack and
// http.ResponseController working for streaming and WebSocket upgrades.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) Flush() {
	// Flushing sends the headers, with an implicit 200 when the handler wrote none.
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	_ = http.NewResponseController(s.ResponseWriter).Flush()
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(s.ResponseWriter).Hijack()
	if err == nil && !s.wrote {
		s.status = http.StatusSwitchingProtocols
		s.wrote = true
	}
	return conn, rw, err
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// requestID assigns an ID to every request: a well-formed one from the client or proxy is kept,
// anything else is replaced, so the logs cannot be polluted through this header.
func requestID() middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if !validRequestID.MatchString(id) {
				id = newRequestID()
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(observability.WithRequestID(r.Context(), id)))
		})
	}
}

// observe records metrics and writes the access log. The log has the method, route pattern,
// status, size and duration only: no query string, headers, cookies, path values or body.
func observe(log *slog.Logger, m *observability.Metrics) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			st := &requestState{route: unmatchedRoute}
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			ctx := context.WithValue(r.Context(), stateKey{}, st)
			m.RequestStarted()
			defer func() {
				m.RequestFinished()
				d := time.Since(start)
				m.ObserveRequest(r.Method, st.route, rec.status, d)
				level := slog.LevelInfo
				switch {
				case isOperational(st.route):
					level = slog.LevelDebug
				case rec.status >= 500:
					level = slog.LevelError
				}
				log.Log(ctx, level, "request",
					"method", r.Method, "route", st.route, "status", rec.status,
					"bytes", rec.bytes, "duration_ms", float64(d.Microseconds())/1000)
			}()
			next.ServeHTTP(rec, r.WithContext(ctx))
		})
	}
}

func isOperational(route string) bool {
	return route == "/healthz" || route == "/readyz" || route == "/metrics"
}

// recoverPanic turns a panic in a handler into a 500 problem. Only the type of the panic value and
// the stack are logged: the value itself can contain request data.
func recoverPanic(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				log.ErrorContext(r.Context(), "panic in handler", "panic_type", fmt.Sprintf("%T", rec), "stack", string(debug.Stack()))
				if sr, ok := w.(*statusRecorder); !ok || !sr.wrote {
					WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders sets the headers that make sense for a JSON API. Handlers that serve files
// (attachments) can override Cache-Control.
func securityHeaders(hsts bool) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			h.Set("Cache-Control", "no-store")
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

var (
	corsAllowedHeaders = strings.Join([]string{"Authorization", "Content-Type", "X-Client-Version", "X-Client-Api-Version", RequestIDHeader}, ", ")
	corsAllowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	corsExposedHeaders = strings.Join([]string{RequestIDHeader, "Retry-After"}, ", ")
)

// cors allows cross-origin requests from the configured origins only (with credentials, because
// the refresh token is an HttpOnly cookie). Without configured origins nothing is added and
// browsers keep to same-origin.
func cors(origins []string) middleware {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[strings.TrimRight(o, "/")] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Add("Vary", "Origin")
			if _, ok := allowed[origin]; !ok {
				if isPreflight(r) {
					WriteProblem(w, http.StatusForbidden, ErrorCodeForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Expose-Headers", corsExposedHeaders)
			if isPreflight(r) {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				h.Set("Access-Control-Allow-Methods", corsAllowedMethods)
				h.Set("Access-Control-Allow-Headers", corsAllowedHeaders)
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

// bodyLimit rejects bodies above limit bytes: early by Content-Length, and while reading for chunked
// bodies (the generated code then reports *http.MaxBytesError, answered as 413).
// Uploads (POST uploadPath) get uploadLimit instead, plus room for the multipart framing.
func bodyLimit(defaultLimit int64, uploadPath string, uploadLimit int64) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limit := defaultLimit
			if uploadLimit > 0 && r.Method == http.MethodPost && r.URL.Path == uploadPath {
				limit = uploadLimit + 64<<10
			}
			if r.ContentLength > limit {
				WriteProblem(w, http.StatusRequestEntityTooLarge, ErrorCodePayloadTooLarge)
				return
			}
			if r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// timeout puts a deadline on the request context of every request. It is a context deadline, not
// http.TimeoutHandler, so streaming keeps working. A WebSocket handler that has completed the
// upgrade must detach the connection from this deadline itself (context.WithoutCancel).
func timeout(d time.Duration) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
