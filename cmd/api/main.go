// Command api serves the REST API and the realtime gateway.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/myronsi/messenger-back/internal/app"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/httpapi"
	"github.com/myronsi/messenger-back/internal/realtime"
	"github.com/myronsi/messenger-back/internal/store/elastic"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func main() { app.Exit("api", run) }

func run() error {
	p, err := app.Start("api", "")
	if err != nil {
		return err
	}
	defer p.Close()
	cfg, log := p.Cfg, p.Log

	pg, err := postgres.New(p.Ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return err
	}
	defer pg.Close()
	rd, err := redis.New(cfg.RedisURL.Reveal())
	if err != nil {
		return err
	}
	defer func() { _ = rd.Close() }()
	sc := scylla.New(cfg.ScyllaHosts, cfg.ScyllaKeyspace)
	defer func() { _ = sc.Close() }()
	es, err := elastic.New(cfg.ElasticsearchURL)
	if err != nil {
		return err
	}
	defer es.Close()

	hub := realtime.NewHub(log, p.Metrics)
	router := httpapi.NewRouter(httpapi.Options{
		HTTP:       cfg.HTTP,
		Production: cfg.Env == "production",
		Tracing:    cfg.Tracing.Enabled,
		Log:        log,
		Metrics:    p.Metrics,
		Checks: []httpapi.Check{
			{Name: "postgres", Ping: pg.Ping},
			{Name: "redis", Ping: rd.Ping},
			{Name: "scylla", Ping: sc.Ping},
			{Name: "elasticsearch", Ping: es.Ping},
		},
	})

	srv := app.NewServer(cfg.HTTP.Addr, router, cfg.HTTP)
	srv.ErrorLog = slog.NewLogLogger(log.Handler(), slog.LevelWarn)

	log.Info("api listening", "addr", cfg.HTTP.Addr, "env", cfg.Env)
	if err := p.Serve(srv); err != nil {
		return err
	}
	return shutdown(log, cfg.HTTP, srv, router, hub)
}

// shutdown drains the server: /readyz turns 503, the listener closes, in-flight requests finish
// and open WebSockets get the "reconnect" close code.
func shutdown(log *slog.Logger, cfg config.HTTP, srv *http.Server, router *httpapi.Router, hub *realtime.Hub) error {
	log.Info("shutting down")
	router.Drain()
	if cfg.DrainDelay > 0 {
		time.Sleep(cfg.DrainDelay)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.Shutdown(ctx)
	}()
	err := srv.Shutdown(ctx)
	<-done
	if err != nil {
		_ = srv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("stopped")
	return nil
}
