// Command worker runs the event consumers: search indexing and background jobs.
package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/myronsi/messenger-back/internal/app"
)

func main() { app.Exit("worker", run) }

func run() error {
	p, err := app.Start("worker", "-worker")
	if err != nil {
		return err
	}
	defer p.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
	mux.Handle("GET /metrics", p.Metrics.Handler())
	srv := app.NewServer(p.Cfg.WorkerAddr, mux, p.Cfg.HTTP)

	p.Log.Info("worker started", "addr", p.Cfg.WorkerAddr)
	// Event consumers (search indexing, background jobs) are started here by later features.
	if err := p.Serve(srv); err != nil {
		return err
	}

	p.Log.Info("worker shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), p.Cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
