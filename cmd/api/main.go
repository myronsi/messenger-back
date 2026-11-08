// Command api serves the REST API and the realtime gateway.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/app"
	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/calls"
	"github.com/myronsi/messenger-back/internal/chats"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/geoip"
	"github.com/myronsi/messenger-back/internal/httpapi"
	"github.com/myronsi/messenger-back/internal/linkpreview"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/notifications"
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
	if cfg.Env == "production" && !cfg.Auth.ProxiesSet() && !loopbackListener(cfg.HTTP.Addr) {
		log.Warn("TRUSTED_PROXIES is not set, so only loopback proxies are trusted: clients behind any other proxy share its address and its rate limits", "http_addr", cfg.HTTP.Addr)
	}
	geo := geoip.Open(cfg.GeoIPDBPath, log)
	defer func() { _ = geo.Close() }()
	var (
		rt       *realtimeStack
		callsSvc *calls.Service
	)
	passkeys, err := auth.NewPasskeys(cfg.Auth.RPID, cfg.Auth.RPOrigins)
	if err != nil {
		return fmt.Errorf("passkey configuration is invalid")
	}
	authSvc, err := auth.NewService(pg, rd.Client(), auth.Config{
		Passkeys:            passkeys,
		RecoveryMailEnabled: cfg.RecoveryMail.Host != "",
		RecoveryWebURL:      cfg.RecoveryMail.WebURL,
		OnSecurityEvent: func(ctx context.Context, uid int64, kind string, client auth.Client) {
			if rt == nil || rt.fanout == nil {
				return
			}
			switch kind {
			case "password_changed", "recovery_key_changed", "2fa_enabled", "2fa_disabled", "passkey_added", "passkey_removed", "recovery_email_changed", "recovery_email_removed", "passkey_counter_warning", "login", "login_2fa", "passkey_login", "qr_login_approved":
				alert := map[string]any{"type": kind, "user_agent": client.UserAgent, "created_at": time.Now().UTC()}
				if p, ok := httpapi.PrincipalFrom(ctx); ok {
					alert["session_id"] = p.SessionID
				}
				rt.fanout.NotifyUser(ctx, uid, "security_alert", alert)
			}
		},
		JWTSecret:                     []byte(cfg.JWTSecret.Reveal()),
		EncryptionKey:                 key,
		RecoveryPepper:                []byte(cfg.RecoveryPepper.Reveal()),
		SessionCacheTTL:               cfg.Auth.SessionCacheTTL,
		RefreshReuseGrace:             cfg.Auth.RefreshReuseGrace,
		HashConcurrency:               cfg.Auth.PasswordHashConcurrency,
		RecoveryApprovalMaxAge:        cfg.Auth.RecoveryApprovalMaxAge,
		RecoveryApprovalMinSessionAge: cfg.Auth.RecoveryApprovalMinSessionAge,
		GeoIP:                         geo,
		OnRecoveryRequest: func(ctx context.Context, uid int64, notice auth.RecoveryRequestNotice) {
			if rt != nil && rt.fanout != nil {
				rt.fanout.NotifyUser(ctx, uid, "recovery_request", notice)
			}
		},
		// Revoked sessions lose their WebSockets on every instance.
		OnRevoked: func(ctx context.Context, sessions []uuid.UUID) {
			rt.closeSessions(ctx, sessions)
			if callsSvc != nil {
				callsSvc.SessionsEnded(ctx, sessions)
			}
		},
		OnRecovery: func(ctx context.Context, sessions []uuid.UUID) {
			if rt.fanout != nil {
				rt.fanout.CloseSessionsWithNotice(ctx, sessions)
			}
			if callsSvc != nil {
				callsSvc.SessionsEnded(ctx, sessions)
			}
		},
	}, log)
	if err != nil {
		return err
	}
	rt, err = startRealtime(p, pg, rd, sc, authSvc)
	if err != nil {
		return err
	}

	callsSvc, err = startCalls(p, pg, rd, rt)
	if err != nil {
		return err
	}
	var callHooks chats.CallHooks
	var callWebhook http.Handler
	if callsSvc != nil {
		callHooks, callWebhook = callsSvc, callsSvc.WebhookHandler()
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
				Service: mediaSvc, Store: pg, Directory: rt.directory, Limiter: rt.limiter, Events: rt.events, BasePath: cfg.HTTP.BasePath, Log: log,
			}),
		).WithAccount(httpapi.NewAccountServer(httpapi.AccountOptions{
			Store: pg, Directory: rt.directory, Deleter: authSvc, AfterDeletion: rt.accountDeleted(storage, callsSvc, log),
			OnBlocked: func(ctx context.Context, blocker, blocked int64) {
				if callsSvc != nil {
					callsSvc.Blocked(ctx, blocker, blocked)
				}
			},
			Events: rt.events, Limiter: rt.limiter, BasePath: cfg.HTTP.BasePath, Log: log,
		})).WithChats(httpapi.NewChatServer(httpapi.ChatOptions{
			Service: chats.New(chats.Deps{
				Store: pg, Messages: rt.store, Unread: rt.unread, Members: rt.members, Sender: rt.messages,
				Directory: rt.directory, Notifier: rt.fanout, Events: rt.events, DeleteFile: storage.Delete, Calls: callHooks, Log: log,
			}),
			Limiter: rt.limiter, BasePath: cfg.HTTP.BasePath, Log: log,
		})).WithMessages(httpapi.NewMessageServer(httpapi.MessageOptions{
			Service: rt.messages, Store: pg, Directory: rt.directory, Limiter: rt.limiter, BasePath: cfg.HTTP.BasePath, Log: log,
			Searcher: search.NewSearcher(search.NewIndex(es, cfg.Search.Alias, cfg.Search.Replicas), searchAccess{rt.messages, pg}, rt.store),
		})).WithLinkPreviews(httpapi.NewLinkPreviewServer(httpapi.LinkPreviewOptions{
			Service: linkpreview.New(linkpreview.Options{
				Cache: linkpreview.NewRedisCache(rd.Client()), SignKey: linkpreview.DeriveKey(cfg.JWTSecret.Reveal()),
			}),
			Limiter: rt.limiter, BasePath: cfg.HTTP.BasePath, Log: log,
		})).WithCalls(httpapi.NewCallServer(httpapi.CallOptions{
			Service: callsSvc, Limits: callLimits(cfg.Calls), Limiter: rt.limiter, Log: log,
		})).WithNotifications(httpapi.NotificationOptions{Store: pg.Notifications(), Config: cfg.Notifications, Tokens: notifications.NewActionTokens(cfg.Notifications.VAPIDPrivate.Reveal())}).WithMeta(httpapi.MetaInfo{
			BackendVersion: version.Backend, Commit: os.Getenv("APP_COMMIT"), MinClientAPIVersion: cfg.Realtime.MinClientAPIVersion,
		}),
		Authenticator:       authSvc,
		WebSocket:           rt.gateway,
		CallWebhook:         callWebhook,
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
	if err := shutdown(log, cfg.HTTP, srv, router.Drain, rt, callsSvc); err != nil {
		return err
	}
	return p.Err()
}

// shutdown drains the server: /readyz turns 503, the listener closes, in-flight requests finish
// and open WebSockets get the "reconnect" close code.
func shutdown(log *slog.Logger, cfg config.HTTP, srv *http.Server, drain func(), rt *realtimeStack, callsSvc *calls.Service) error {
	log.Info("shutting down")
	drain()
	if cfg.DrainDelay > 0 {
		time.Sleep(cfg.DrainDelay)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.closeSockets(ctx)
	}()
	err := srv.Shutdown(ctx)
	<-done
	rt.stop()
	if callsSvc != nil {
		callsSvc.Wait(ctx)
	}
	if err != nil {
		_ = srv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("stopped")
	return nil
}

func loopbackListener(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
