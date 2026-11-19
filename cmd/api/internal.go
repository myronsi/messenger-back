package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/myronsi/messenger-back/internal/app"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/internalapi"
	"github.com/myronsi/messenger-back/internal/version"
)

func startInternal(log *slog.Logger, cfg config.Config) *http.Server {
	if cfg.Internal.Addr == "" {
		return nil
	}
	handler := internalapi.Handler(internalapi.Options{Token: cfg.Internal.Token.Reveal(), Version: version.Backend, Log: log})
	srv := app.NewServer(cfg.Internal.Addr, handler, cfg.HTTP)
	srv.ErrorLog = slog.NewLogLogger(log.Handler(), slog.LevelWarn)
	go func() {
		log.Info("internal api listening", "addr", cfg.Internal.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("internal api stopped", "error", err)
		}
	}()
	return srv
}

func stopInternal(log *slog.Logger, cfg config.HTTP, srv *http.Server) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Warn("internal api shutdown", "error", err)
		_ = srv.Close()
	}
}
