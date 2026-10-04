package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerRedactsSensitiveAttributes(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo, "test")
	ctx := WithRequestID(context.Background(), "req-12345678")

	log.InfoContext(ctx, "event",
		"user_id", "42",
		"password", "hunter2",
		"refresh_token", "tok-123",
		"Authorization", "Bearer abc",
		"X-Api-Key", "key-1",
		"content", "hello private message",
		"nested", slog.GroupValue(slog.String("secret", "s3"), slog.String("ok", "visible")),
	)

	out := buf.String()
	for _, leak := range []string{"hunter2", "tok-123", "Bearer abc", "key-1", "hello private message", "s3"} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q: %s", leak, out)
		}
	}

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log is not JSON: %v", err)
	}
	if rec["request_id"] != "req-12345678" || rec["service"] != "test" || rec["user_id"] != "42" {
		t.Fatalf("missing fields: %v", rec)
	}
	if rec["password"] != "[redacted]" {
		t.Fatalf("password = %v", rec["password"])
	}
	if nested := rec["nested"].(map[string]any); nested["ok"] != "visible" || nested["secret"] != "[redacted]" {
		t.Fatalf("nested = %v", nested)
	}
}

func TestLoggerKeepsAttributesFromWith(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo, "test").With("token", "abc").WithGroup("g")
	log.Info("x", "k", "v")
	if strings.Contains(buf.String(), `"abc"`) {
		t.Fatalf("With() attribute not redacted: %s", buf.String())
	}
}
