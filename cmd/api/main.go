// Command api serves the REST API and the realtime gateway.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/app"
	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/chats"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/httpapi"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/search"
	"github.com/myronsi/messenger-back/internal/store/elastic"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/version"
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

	storage, storagePing, err := media.StorageFrom(cfg.Media)
	if err != nil {
		return err
	}
	prober := media.NewProber(cfg.Media.FFprobe, cfg.Media.FFmpeg)
	mediaSvc := media.NewService(media.Options{
		Storage: storage, Attachments: pg.Attachments(), Visibility: rt.messages, Membership: rt.messages,
		Prober: prober, SignedURLs: cfg.Media.SignedURLs, ImageWorkers: cfg.Media.ImageWorkers, Log: log,
	})
	if !prober.Available() {
		log.Warn("ffprobe/ffmpeg not found: voice messages keep the duration and waveform the client sends")
	}
	checks := []httpapi.Check{
		{Name: "postgres", Ping: pg.Ping},
		{Name: "redis", Ping: rd.Ping},
		{Name: "scylla", Ping: sc.Ping},
		{Name: "elasticsearch", Ping: es.Ping},
		{Name: "ids", Ping: rt.ids.Ready},
	}
	if storagePing != nil {
		checks = append(checks, httpapi.Check{Name: "object_storage", Ping: storagePing})
	}
	router := httpapi.NewRouter(httpapi.Options{
		HTTP:       cfg.HTTP,
		Production: cfg.Env == "production",
		Tracing:    cfg.Tracing.Enabled,
		Log:        log,
		Metrics:    p.Metrics,
		API: httpapi.NewServer(
			httpapi.NewAuthServer(httpapi.AuthOptions{
				Service:        authSvc,
				Log:            log,
				BasePath:       cfg.HTTP.BasePath,
				CookiePath:     cfg.Auth.RefreshCookiePath,
				CookieSecure:   cfg.Auth.CookieSecure,
				TrustedProxies: trustedProxies,
			}),
			httpapi.NewMediaServer(httpapi.MediaOptions{
				Service: mediaSvc, Store: pg, Directory: rt.directory, Limiter: rt.limiter, BasePath: cfg.HTTP.BasePath, Log: log,
			}),
		).WithAccount(httpapi.NewAccountServer(httpapi.AccountOptions{
			Store: pg, Directory: rt.directory, Deleter: authSvc, AfterDeletion: rt.accountDeleted(storage, log),
			Events: rt.events, Limiter: rt.limiter, BasePath: cfg.HTTP.BasePath, Log: log,
		})).WithChats(httpapi.NewChatServer(httpapi.ChatOptions{
			Service: chats.New(chats.Deps{
				Store: pg, Messages: rt.store, Unread: rt.unread, Members: rt.members, Sender: rt.messages,
				Directory: rt.directory, Notifier: rt.fanout, Events: rt.events, DeleteFile: storage.Delete, Log: log,
			}),
			Limiter: rt.limiter, BasePath: cfg.HTTP.BasePath, Log: log,
		})).WithMessages(httpapi.NewMessageServer(httpapi.MessageOptions{
			Service: rt.messages, Store: pg, Directory: rt.directory, Limiter: rt.limiter, BasePath: cfg.HTTP.BasePath, Log: log,
			Searcher: search.NewSearcher(search.NewIndex(es, cfg.Search.Alias, cfg.Search.Replicas), searchAccess{rt.messages, pg}, rt.store),
		})).WithMeta(httpapi.MetaInfo{
			BackendVersion: version.Backend, Commit: os.Getenv("APP_COMMIT"), MinClientAPIVersion: cfg.Realtime.MinClientAPIVersion,
		}),
		Authenticator:       authSvc,
		WebSocket:           rt.gateway,
		UploadMaxBytes:      mediaSvc.Limits().Max(),
		MinClientAPIVersion: cfg.Realtime.MinClientAPIVersion,
		Checks:              checks,
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
