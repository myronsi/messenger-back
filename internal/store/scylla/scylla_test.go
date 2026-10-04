package scylla

import (
	"context"
	"testing"
	"time"
)

func TestPingHonoursContextDeadline(t *testing.T) {
	// 192.0.2.0/24 is reserved for documentation: nothing answers there.
	s := New([]string{"192.0.2.1"}, "")
	t.Cleanup(func() { _ = s.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Ping(ctx); err == nil {
		t.Fatal("expected an error")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Ping took %s, longer than the context deadline", d)
	}
}

func TestSessionAfterCloseFailsImmediately(t *testing.T) {
	s := New([]string{"192.0.2.1"}, "")
	_ = s.Close()

	start := time.Now()
	if _, err := s.Session(context.Background()); err == nil {
		t.Fatal("expected store closed")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("Session after Close took %s", d)
	}
}
