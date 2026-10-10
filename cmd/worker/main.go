// Command worker runs the event consumers (chat cleanup, search indexing) and the maintenance jobs.
//
// "worker reindex" rebuilds the search index from the message store, switches to it and exits.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/gocql/gocql"

	"github.com/myronsi/messenger-back/internal/app"
	"github.com/myronsi/messenger-back/internal/jobs"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/search"
	"github.com/myronsi/messenger-back/internal/store/elastic"
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
	es, err := elastic.New(cfg.ElasticsearchURL.Reveal())
	if err != nil {
		return err
	}
	defer es.Close()
	msgs := scylla.NewMessages(sc, cfg.Scylla.Timeout)
	index := search.NewIndex(es, cfg.Search.Alias, cfg.Search.Replicas)
	indexer := search.NewIndexer(index, msgs, pg.Attachments(), log)
	if len(os.Args) > 1 && os.Args[1] == "reindex" {
		if err := index.Ensure(p.Ctx); err != nil {
			return err
		}
		log.Info("rebuilding the search index")
		name, err := indexer.Rebuild(p.Ctx, pg.Chats())
		if err != nil {
			return err
		}
		log.Info("search index rebuilt", "index", name)
		return nil
	}

	// Background work stops with its own context, after the HTTP endpoints are drained.
	bg, stopBackground := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		stopBackground()
		wg.Wait()
	}()
	wg.Go(func() { maintain(bg, log, pg, maintenanceEvery) })
	storage, _, err := media.StorageFrom(cfg.Media)
	if err != nil {
		return err
	}
	mediaSvc := media.NewService(media.Options{Storage: storage, Attachments: pg.Attachments(), Log: log})
	wg.Go(func() { collectUploads(bg, log, mediaSvc, maintenanceEvery) })
	// Elasticsearch being away must not stop the other consumers: the template and first index are set up as
	// soon as it answers, and index writes fail (and are retried) until then.
	wg.Go(func() { ensureIndex(bg, log, index) })
	if cfg.Search.ReconcileEvery > 0 {
		wg.Go(func() {
			reconcileSearch(bg, log, indexer, pg.Chats(), cfg.Search.ReconcileEvery, cfg.Search.ReconcileWindow)
		})
	}

	instance := app.InstanceID(cfg.Realtime.InstanceID)
	consumers := []redis.ConsumerOptions{{
		Stream: redis.StreamChats, Group: jobs.GroupChatCleanup,
		Handler: jobs.ChatCleanup(msgs, redis.NewUnread(rd.Client(), ""), pg.Chats(), scylla.DeleteGracePeriod, log),
	}, {
		Stream: redis.StreamMessages, Group: search.Group, Handler: indexer.Handle,
	}, {
		Stream: redis.StreamChats, Group: search.Group, Handler: indexer.Handle,
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
