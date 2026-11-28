package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"
)

const pprofWriteTimeout = 2 * time.Minute

func PprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}

func StartPprof(log *slog.Logger, addr string) func() {
	if addr == "" {
		return func() {}
	}
	srv := &http.Server{
		Addr: addr, Handler: PprofHandler(),
		ReadHeaderTimeout: 5 * time.Second, WriteTimeout: pprofWriteTimeout, IdleTimeout: time.Minute,
	}
	go func() {
		log.Info("pprof listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("pprof stopped", "error", err)
		}
	}()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}
