// Command worker runs the event consumers (chat cleanup, later search indexing and push) and the
// maintenance jobs.
package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/gocql/gocql"

	"github.com/myronsi/messenger-back/internal/app"
	"github.com/myronsi/messenger-back/internal/jobs"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func main() { app.Exit("worker", run) }

func run() error {
	p, err := app.Start("worker", "-worker")
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

	// Background work stops with its own context, after the HTTP endpoints are drained.
	bg, stopBackground := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		stopBackground()
		wg.Wait()
	}()
	wg.Go(func() { maintain(bg, log, pg, maintenanceEvery) })

	instance := app.InstanceID(cfg.Realtime.InstanceID)
	consumers := []redis.ConsumerOptions{{
		Stream: redis.StreamChats, Group: jobs.GroupChatCleanup,
		Handler: jobs.ChatCleanup(scylla.NewMessages(sc, cfg.Scylla.Timeout), redis.NewUnread(rd.Client(), ""), pg.Chats(), scylla.DeleteGracePeriod, log),
	}}
	for _, o := range consumers {
		group := o.Group
		o.Name, o.Log = instance, log
		o.OnResult = func(ok, dead bool) { p.Metrics.EventHandled(group, ok, dead) }
		c, err := redis.NewConsumer(rd.Client(), o)
		if err != nil {
			return err
		}
		wg.Go(func() { c.Run(bg) })
		log.Info("consumer started", "stream", o.Stream, "group", group)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
	mux.Handle("GET /metrics", p.Metrics.Handler())
	srv := app.NewServer(cfg.WorkerAddr, mux, cfg.HTTP)

	log.Info("worker started", "addr", cfg.WorkerAddr, "instance", instance)
	if err := p.Serve(srv); err != nil {
		return err
	}

	log.Info("worker shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
