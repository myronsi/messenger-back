// Command api serves the REST API and the realtime gateway.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/httpapi"
	"github.com/myronsi/messenger-back/internal/observability"
	"github.com/myronsi/messenger-back/internal/realtime"
	"github.com/myronsi/messenger-back/internal/store/elastic"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	level, _ := config.ParseLogLevel(cfg.LogLevel)
	log := observability.NewLogger(os.Stdout, level, "api")
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := observability.SetupTracing(ctx, cfg.Tracing.Enabled, cfg.Tracing.ServiceName)
	if err != nil {
		return err
	}
	defer func() {
		tctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(tctx); err != nil {
			log.Warn("flush traces", "error", err)
		}
	}()

	metrics := observability.NewMetrics()

	pg, err := postgres.New(ctx, cfg.DatabaseURL.Reveal())
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

	hub := realtime.NewHub(log, metrics)
	router := httpapi.NewRouter(httpapi.Options{
		HTTP:       cfg.HTTP,
		Production: cfg.Env == "production",
		Tracing:    cfg.Tracing.Enabled,
		Log:        log,
		Metrics:    metrics,
		Checks: []httpapi.Check{
			{Name: "postgres", Ping: pg.Ping},
			{Name: "redis", Ping: rd.Ping},
			{Name: "scylla", Ping: sc.Ping},
			{Name: "elasticsearch", Ping: es.Ping},
		},
	})

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		MaxHeaderBytes:    1 << 16,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", cfg.HTTP.Addr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}
	stop()
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
