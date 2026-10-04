package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/myronsi/messenger-back/internal/observability"
)

// Check is a dependency that /readyz probes.
type Check struct {
	// Name is the store name used in the response and as the metric label.
	Name string
	Ping func(ctx context.Context) error
}

type health struct {
	log      *slog.Logger
	metrics  *observability.Metrics
	checks   []Check
	timeout  time.Duration
	draining atomic.Bool
}

// healthz reports that the process is alive. It does not touch any dependency, so a database
// outage never makes the orchestrator restart the process.
func (h *health) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz reports whether PostgreSQL, Redis, ScyllaDB and Elasticsearch answer. The response names
// the failing stores but never the error text, which can contain host names.
func (h *health) readyz(w http.ResponseWriter, r *http.Request) {
	results := make(map[string]string, len(h.checks))
	ready := true
	if h.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "shutting_down", "checks": results})
		return
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, c := range h.checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
			defer cancel()
			start := time.Now()
			err := c.Ping(ctx)
			h.metrics.ObserveStoreCheck(c.Name, time.Since(start))

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				ready = false
				results[c.Name] = "unavailable"
				h.metrics.StoreError(c.Name)
				h.log.WarnContext(r.Context(), "readiness check failed", "store", c.Name, "reason", failureReason(err))
				return
			}
			results[c.Name] = "ok"
		}()
	}
	wg.Wait()

	status, code := "ready", http.StatusOK
	if !ready {
		status, code = "not_ready", http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"status": status, "checks": results})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// failureReason classifies a dependency error without its text, which may contain connection
// strings or credentials.
func failureReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return fmt.Sprintf("%T", err)
	}
}
