// Command api serves the REST API and the realtime gateway.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/app"
	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/httpapi"
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

	pg, err := postgres.New(p.Ctx, cfg.DatabaseURL.Reveal(), postgres.Options{
		MaxConns:     cfg.Postgres.MaxConns,
		QueryTimeout: cfg.Postgres.QueryTimeout,
	})
	if err != nil {
		return err
	}
	defer pg.Close()
	rd, err := redis.New(cfg.RedisURL.Reveal(), redis.Options{
		Timeout: cfg.Redis.Timeout,
		OnError: func() { p.Metrics.StoreError("redis") },
	})
	if err != nil {
		return err
	}
	defer func() { _ = rd.Close() }()
	sc := scylla.New(cfg.ScyllaHosts, cfg.ScyllaKeyspace, scylla.Options{
		Consistency:    gocql.ParseConsistency(cfg.Scylla.Consistency),
		RequestTimeout: cfg.Scylla.Timeout,
	})
	defer func() { _ = sc.Close() }()
	es, err := elastic.New(cfg.ElasticsearchURL.Reveal())
	if err != nil {
		return err
	}
	defer es.Close()

	key, err := base64.StdEncoding.DecodeString(cfg.EncryptionKey.Reveal())
	if err != nil {
		return fmt.Errorf("decode ENCRYPTION_KEY: %w", err)
	}
	trustedProxies, err := cfg.Auth.TrustedProxyPrefixes()
	if err != nil {
		return err
	}
	var rt *realtimeStack
	authSvc, err := auth.NewService(pg, rd.Client(), auth.Config{
		JWTSecret:         []byte(cfg.JWTSecret.Reveal()),
		EncryptionKey:     key,
		RecoveryPepper:    []byte(cfg.RecoveryPepper.Reveal()),
		SessionCacheTTL:   cfg.Auth.SessionCacheTTL,
		RefreshReuseGrace: cfg.Auth.RefreshReuseGrace,
		HashConcurrency:   cfg.Auth.PasswordHashConcurrency,
		// Revoked sessions lose their WebSockets on every instance.
		OnRevoked: func(ctx context.Context, sessions []uuid.UUID) { rt.closeSessions(ctx, sessions) },
	}, log)
	if err != nil {
		return err
	}
	rt, err = startRealtime(p, pg, rd, sc, authSvc)
	if err != nil {
		return err
	}

	router := httpapi.NewRouter(httpapi.Options{
		HTTP:       cfg.HTTP,
		Production: cfg.Env == "production",
		Tracing:    cfg.Tracing.Enabled,
		Log:        log,
		Metrics:    p.Metrics,
		API: httpapi.NewAuthServer(httpapi.AuthOptions{
			Service:        authSvc,
			Log:            log,
			BasePath:       cfg.HTTP.BasePath,
			CookiePath:     cfg.Auth.RefreshCookiePath,
			CookieSecure:   cfg.Auth.CookieSecure,
			TrustedProxies: trustedProxies,
		}),
		Authenticator: authSvc,
		WebSocket:     rt.gateway,
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
	if err := shutdown(log, cfg.HTTP, srv, router, rt); err != nil {
		return err
	}
	return p.Err()
}

// shutdown drains the server: /readyz turns 503, the listener closes, in-flight requests finish
// and open WebSockets get the "reconnect" close code.
func shutdown(log *slog.Logger, cfg config.HTTP, srv *http.Server, router *httpapi.Router, rt *realtimeStack) error {
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
		rt.stop(ctx)
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
