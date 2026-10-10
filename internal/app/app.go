// Package app holds the process plumbing shared by the commands in cmd/: configuration, logging,
// tracing, signal handling and the HTTP server lifecycle.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/observability"
)

const tracingFlushTimeout = 5 * time.Second

// Process is the common state of a running command.
type Process struct {
	Cfg     config.Config
	Log     *slog.Logger
	Metrics *observability.Metrics
	// Ctx is cancelled on SIGINT or SIGTERM.
	Ctx context.Context

	stop            context.CancelFunc
	shutdownTracing func(context.Context) error

	failMu  sync.Mutex
	failure error
}

// Fail stops the process because of err: it shuts down like on SIGTERM, and Err returns err so the command
// exits with a failure (for example after losing the lease of its ID node number).
func (p *Process) Fail(err error) {
	p.failMu.Lock()
	if p.failure == nil {
		p.failure = err
	}
	p.failMu.Unlock()
	p.stop()
}

// Err returns the error passed to Fail, if any.
func (p *Process) Err() error {
	p.failMu.Lock()
	defer p.failMu.Unlock()
	return p.failure
}

// Start loads the configuration, installs the JSON logger, tracing and signal handling.
// name labels the logs; the OpenTelemetry service name is the configured one plus tracingSuffix.
func Start(name, tracingSuffix string) (*Process, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	level, _ := config.ParseLogLevel(cfg.LogLevel)
	log := observability.NewLogger(os.Stdout, level, name)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	shutdownTracing, err := observability.SetupTracing(ctx, cfg.Tracing.Enabled, cfg.Tracing.ServiceName+tracingSuffix)
	if err != nil {
		stop()
		return nil, err
	}
	return &Process{
		Cfg:             cfg,
		Log:             log,
		Metrics:         observability.NewMetrics(),
		Ctx:             ctx,
		stop:            stop,
		shutdownTracing: shutdownTracing,
	}, nil
}

// Close releases the signal handler and flushes pending traces.
func (p *Process) Close() {
	p.stop()
	ctx, cancel := context.WithTimeout(context.Background(), tracingFlushTimeout)
	defer cancel()
	if err := p.shutdownTracing(ctx); err != nil {
		p.Log.Warn("flush traces", "error", err)
	}
}

// InstanceID returns the configured id, or the host name with a random suffix (a restarted process is a new
// instance).
func InstanceID(configured string) string {
	if configured != "" {
		return configured
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "messenger"
	}
	if len(host) > 48 {
		host = host[:48]
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return host + "-" + hex.EncodeToString(b)
}

// NewServer builds an http.Server with the timeouts from the configuration.
func NewServer(addr string, handler http.Handler, cfg config.HTTP) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 16,
	}
}

// Serve runs srv in the background and blocks until it fails or the process is told to stop.
// It returns nil on a requested stop.
func (p *Process) Serve(srv *http.Server) error {
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()
	select {
	case err, ok := <-serveErr:
		if !ok {
			return nil
		}
		return fmt.Errorf("listen: %w", err)
	case <-p.Ctx.Done():
		p.stop()
		return nil
	}
}

// Exit runs fn and terminates the process with status 1 and the message "name: err" on failure.
func Exit(name string, fn func() error) {
	if err := fn(); err != nil {
		fmt.Fprintln(os.Stderr, name+":", err)
		os.Exit(1)
	}
}
