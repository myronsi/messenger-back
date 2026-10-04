package realtime

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeConn struct {
	mu     sync.Mutex
	code   int
	reason string
	block  chan struct{}
}

func (c *fakeConn) Close(code int, reason string) error {
	if c.block != nil {
		<-c.block
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.code, c.reason = code, reason
	return nil
}

type fakeGauge struct{ open int }

func (g *fakeGauge) WebSocketOpened() { g.open++ }
func (g *fakeGauge) WebSocketClosed() { g.open-- }

func newHub(g Gauge) *Hub { return NewHub(slog.New(slog.DiscardHandler), g) }

func TestShutdownClosesConnectionsWithReconnectCode(t *testing.T) {
	g := &fakeGauge{}
	h := newHub(g)
	a, b := &fakeConn{}, &fakeConn{}
	h.Add(a)
	h.Add(b)
	if h.Len() != 2 || g.open != 2 {
		t.Fatalf("len=%d gauge=%d", h.Len(), g.open)
	}

	h.Shutdown(context.Background())

	for _, c := range []*fakeConn{a, b} {
		if c.code != CloseServiceRestart || c.reason != ReconnectReason {
			t.Fatalf("close = %d %q", c.code, c.reason)
		}
	}
	if h.Len() != 0 || g.open != 0 {
		t.Fatalf("len=%d gauge=%d", h.Len(), g.open)
	}
	if h.Add(&fakeConn{}) {
		t.Fatal("Add must refuse connections after shutdown")
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	g := &fakeGauge{}
	h := newHub(g)
	c := &fakeConn{}
	h.Add(c)
	h.Remove(c)
	h.Remove(c)
	if g.open != 0 {
		t.Fatalf("gauge = %d", g.open)
	}
}

func TestShutdownHonoursContext(t *testing.T) {
	h := newHub(nil)
	c := &fakeConn{block: make(chan struct{})}
	defer close(c.block)
	h.Add(c)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { h.Shutdown(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown ignored the context deadline")
	}
}
