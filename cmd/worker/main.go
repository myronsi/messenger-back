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

	"github.com/myronsi/messenger-back/internal/app"
	"github.com/myronsi/messenger-back/internal/contentreports"
	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/inactive"
	"github.com/myronsi/messenger-back/internal/jobs"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/notifications"
	"github.com/myronsi/messenger-back/internal/observability"
	"github.com/myronsi/messenger-back/internal/realtime"
	"github.com/myronsi/messenger-back/internal/search"
	"github.com/myronsi/messenger-back/internal/store/elastic"
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

	pg, err := p.OpenPostgres()
	if err != nil {
		return err
	}
	defer pg.Close()
	if len(os.Args) > 1 && os.Args[1] == "unhide-sticker-pack" {
		return unhideStickerPack(p.Ctx, log, pg, os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "inactive-accounts" {
		return inactiveAccountsReport(p.Ctx, cfg, pg, os.Stdout)
	}
	rd, err := p.OpenRedis()
	if err != nil {
		return err
	}
	defer func() { _ = rd.Close() }()
	sc := p.OpenScylla()
	defer func() { _ = sc.Close() }()
	es, err := elastic.New(cfg.ElasticsearchURL.Reveal())
	if err != nil {
		return err
	}
	defer es.Close()
	msgs := scylla.NewMessages(sc, cfg.Scylla.Timeout)
	index := search.NewIndex(es, cfg.Search.Alias, cfg.Search.Replicas)
	indexer := search.NewIndexer(index, msgs, pg.Attachments(), log).WithDeletions(pg.Chats()).WithPolls(pg.Polls())
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
	pushSender, err := notifications.NewSender(cfg.Notifications)
	if err != nil {
		return err
	}
	actionTokens := notifications.NewActionTokens(cfg.Notifications.VAPIDPrivate.Reveal())
	pushWorker := &notifications.Worker{Queue: pg.Notifications(), Chats: pg.Chats(), Social: pg.Social(), Users: pg.Users(), Attachments: pg.Attachments(), Messages: msgs, Sender: pushSender, Tokens: actionTokens, Log: log}
	pushWorker.OnResult = p.Metrics.Notification
	wg.Go(func() { pushWorker.Run(bg) })
	birthdays := &notifications.Birthdays{Store: pg.Users(), Privacy: pg.Social(), Devices: pg.Notifications(), Sender: pushSender, Tokens: actionTokens, Log: log}
	wg.Go(func() { birthdays.Run(bg, birthdayEvery) })
	storage, _, err := media.StorageFrom(cfg.Media)
	if err != nil {
		return err
	}
	prober := media.NewProber(cfg.Media.FFprobe, cfg.Media.FFmpeg)
	mediaSvc := media.NewService(media.Options{
		Storage: storage, Attachments: pg.Attachments(), Prober: prober, Jobs: pg.MediaJobs(), Sessions: pg.UploadSessions(), Log: log,
	})
	if len(os.Args) > 1 && os.Args[1] == "media-backfill" {
		return mediaBackfill(p.Ctx, log, mediaSvc, os.Args[2:])
	}
	wg.Go(func() { collectUploads(bg, log, mediaSvc, maintenanceEvery) })
	attachmentBus, err := redis.NewBus(rd.Client(), redis.BusOptions{Handler: func(int64, []byte) {}})
	if err != nil {
		return err
	}
	fanout := realtime.NewFanout(realtime.FanoutDeps{Bus: attachmentBus, Log: log, Metrics: p.Metrics})
	authSvc, err := workerAuth(cfg, pg, rd.Client(), log, fanout.CloseSessions)
	if err != nil {
		return err
	}
	mailWorker := recoveryMailWorker(cfg, authSvc)
	wg.Go(func() { mailWorker(bg) })
	streams := redis.NewStreams(rd.Client(), "", 0)
	p.Metrics.RegisterStreamBacklog(func(ctx context.Context) ([]observability.StreamGroupStat, error) {
		groups, err := streams.Backlog(ctx)
		out := make([]observability.StreamGroupStat, 0, len(groups))
		for _, g := range groups {
			out = append(out, observability.StreamGroupStat{Stream: g.Stream, Group: g.Group, Lag: g.Lag, Pending: g.Pending})
		}
		return out, err
	})
	evlog := events.NewLog(streams)
	if cfg.Inactive.Deletion {
		job := inactiveAccounts(inactiveAccountsDeps{
			cfg: cfg, pg: pg, accounts: authSvc, fanout: fanout, rdb: rd.Client(), storage: storage, events: evlog, log: log,
		})
		wg.Go(func() {
			job.RunDaily(bg, inactiveAccountsEvery, redis.NewOnce(rd.Client(), "", "inactive_accounts_daily"))
		})
		log.Info("automatic deletion of inactive accounts is on", "after", inactive.Period)
	}
	notifier := realtime.NewAttachmentNotifier(attachmentBus, pg.Attachments(), cfg.HTTP.BasePath, log)
	modSvc := moderation.New(moderation.Deps{
		Store: pg, Cache: redis.NewModerationCache(rd.Client(), "", 0), Notifier: realtime.NewUserNotifier(attachmentBus, log),
		Metrics: p.Metrics, Log: log, Purge: contentreports.New(contentreports.Deps{Store: pg, Log: log}).Purge,
	})
	wg.Go(func() { modSvc.RunDaily(bg, log, moderationEvery, redis.NewOnce(rd.Client(), "", "moderation_daily")) })
	if !prober.Available() {
		log.Warn("ffprobe/ffmpeg not found: media jobs only mark attachments as done")
	}
	runner := &mediaJobRunner{svc: mediaSvc, jobs: pg.MediaJobs(), notify: notifier.AttachmentUpdated, metrics: p.Metrics.MediaJob, log: log}
	wg.Go(func() { runner.run(bg, cfg.Media.TranscodeWorkers) })
	wg.Go(func() { seedStickers(bg, log, pg, storage) })
	// Elasticsearch being away must not stop the other consumers: the template and first index are set up as
	// soon as it answers, and index writes fail (and are retried) until then.
	if cfg.Search.ReconcileEvery > 0 {
		wg.Go(func() {
			reconcileSearch(bg, log, indexer, pg.Chats(), cfg.Search.ReconcileEvery, cfg.Search.ReconcileWindow)
		})
	}

	instance := app.InstanceID(cfg.Realtime.InstanceID)
	start := func(o redis.ConsumerOptions) error {
		group := o.Group
		o.Name, o.Log = instance, log
		o.OnResult = func(ok, dead bool) { p.Metrics.EventHandled(group, ok, dead) }
		c, err := redis.NewConsumer(rd.Client(), o)
		if err != nil {
			return err
		}
		wg.Go(func() { c.Run(bg) })
		log.Info("consumer started", "stream", o.Stream, "group", group)
		return nil
	}
	wg.Go(func() { resendChatDeletions(bg, log, pg.Chats(), evlog, deletionSweepEvery, deletionLostAfter) })
	if err := start(redis.ConsumerOptions{Stream: redis.StreamUsers, Group: moderation.Group, Handler: modSvc.Handle}); err != nil {
		return err
	}
	if err := start(redis.ConsumerOptions{
		Stream: redis.StreamChats, Group: jobs.GroupChatCleanup,
		Handler: jobs.ChatCleanup(msgs, redis.NewUnread(rd.Client(), ""), pg.Chats(), pg.Attachments(), scylla.DeleteGracePeriod, log),
	}); err != nil {
		return err
	}
	// The search consumers start once the index exists: writes before that would fail (they require the alias)
	// and burn their retries. While Elasticsearch is away the entries wait in the streams.
	wg.Go(func() {
		if !ensureIndex(bg, log, index) {
			return
		}
		for _, stream := range []string{redis.StreamMessages, redis.StreamChats} {
			if err := start(redis.ConsumerOptions{Stream: stream, Group: search.Group, Handler: indexer.Handle}); err != nil {
				log.Error("search consumer", "error", err)
			}
		}
	})

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
	ctx, cancel := context.WithTimeout(context.WithoutCancel(bg), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
