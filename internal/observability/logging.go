// Package observability provides structured logging, Prometheus metrics and optional tracing.
package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

const redacted = "[redacted]"

// Attribute names whose values never reach the logs: message content, tokens and secrets
// (MSGC-38). Matching is case-insensitive. Fragments also catch keys such as "refresh_token" or
// "X-Api-Key"; the exact names are too short to match as fragments without false positives.
var sensitiveFragments = []string{
	"password", "passwd", "secret", "token", "authorization", "cookie", "api_key", "apikey", "api-key",
	"pepper", "encryption", "credential", "ticket", "private", "plaintext", "ciphertext", "recovery",
	"dsn", "database_url", "redis_url",
}

var sensitiveNames = map[string]bool{
	"content": true, "message": true, "body": true, "text": true, "payload": true, "message_text": true,
}

// NewLogger returns a JSON logger that writes to w. Attributes with sensitive names are replaced
// by "[redacted]" at any nesting depth, as a second line of defence next to not logging them at
// all.
func NewLogger(w io.Writer, level slog.Level, service string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redactAttr,
	})
	return slog.New(&requestIDHandler{Handler: h}).With("service", service)
}

func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, redacted)
	}
	return a
}

func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	if sensitiveNames[k] {
		return true
	}
	for _, f := range sensitiveFragments {
		if strings.Contains(k, f) {
			return true
		}
	}
	return false
}

type ctxKey struct{}

// WithRequestID returns a context that carries the request ID; log calls that use the *Context
// methods add it as "request_id" automatically.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID returns the request ID stored in ctx, or an empty string.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

type requestIDHandler struct{ slog.Handler }

func (h *requestIDHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h *requestIDHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &requestIDHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *requestIDHandler) WithGroup(name string) slog.Handler {
	return &requestIDHandler{Handler: h.Handler.WithGroup(name)}
}
