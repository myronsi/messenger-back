// Package realtime tracks the open WebSocket connections of the gateway so they can be counted and
// closed gracefully on shutdown. The WebSocket protocol itself (api/websocket.md) is added later.
package realtime

import (
	"context"
	"log/slog"
	"sync"
)

// CloseServiceRestart is the WebSocket close code 1012 ("Service Restart", RFC 6455 registry).
// Clients treat it as the "reconnect" signal and connect again with backoff.
const CloseServiceRestart = 1012

// ReconnectReason is the close reason sent together with CloseServiceRestart.
const ReconnectReason = "reconnect"

// Conn is a WebSocket connection as the hub sees it.
type Conn interface {
	// Close sends a close frame with the given code and reason and closes the connection.
	Close(code int, reason string) error
}

// Gauge is notified when connections open and close (implemented by the metrics).
type Gauge interface {
	WebSocketOpened()
	WebSocketClosed()
}

// Hub is the registry of open connections.
type Hub struct {
	log   *slog.Logger
	gauge Gauge

	mu      sync.Mutex
	conns   map[Conn]struct{}
	closing bool
}

// NewHub creates an empty hub. gauge may be nil.
func NewHub(log *slog.Logger, gauge Gauge) *Hub {
	return &Hub{log: log, gauge: gauge, conns: make(map[Conn]struct{})}
}

// Add registers a connection. It returns false when the hub is shutting down; the caller must
// then close the connection with CloseServiceRestart itself.
func (h *Hub) Add(c Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return false
	}
	h.conns[c] = struct{}{}
	if h.gauge != nil {
		h.gauge.WebSocketOpened()
	}
	return true
}

// Remove unregisters a connection that has closed.
func (h *Hub) Remove(c Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[c]; !ok {
		return
	}
	delete(h.conns, c)
	if h.gauge != nil {
		h.gauge.WebSocketClosed()
	}
}

// Len returns the number of open connections.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// Shutdown stops accepting connections and closes every open one with the "reconnect" code. The
// HTTP server does not wait for hijacked connections, so this is what drains them. It returns when
// all close frames are sent or ctx is done.
func (h *Hub) Shutdown(ctx context.Context) {
	h.mu.Lock()
	h.closing = true
	conns := make([]Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()

	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Close(CloseServiceRestart, ReconnectReason); err != nil {
				h.log.DebugContext(ctx, "close websocket", "error", err)
			}
			h.Remove(c)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		h.log.WarnContext(ctx, "websocket shutdown timed out", "open", h.Len())
	}
}
