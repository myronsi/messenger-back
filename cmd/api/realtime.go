package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/app"
	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/realtime"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

// realtimeStack is the gateway with everything it runs on.
type realtimeStack struct {
	hub       *realtime.Hub
	gateway   *realtime.Gateway
	fanout    *realtime.Fanout
	messages  *messages.Service
	store     scylla.MessageRepository
	directory *users.Directory
	members   *redis.Members
	unread    *redis.Unread
	events    *events.Log
	limiter   *redis.RateLimiter
	bus       *redis.Bus
	// ctx runs the background loops; it ends in stop, after the sockets are closed, so their last
	// presence changes still go out.
	ctx    context.Context
	cancel context.CancelFunc
	done   sync.WaitGroup
}

// lazyIDs hands out IDs once a node number is known. Until the lease from Redis succeeds, Next fails and
// the actions that need an ID are refused as temporarily unavailable.
type lazyIDs struct {
	mu  sync.RWMutex
	gen *ids.Generator
}

var errNoNode = errors.New("ids: no node number leased yet")

func (l *lazyIDs) set(g *ids.Generator) {
	l.mu.Lock()
	l.gen = g
	l.mu.Unlock()
}

func (l *lazyIDs) Next() (int64, error) {
	l.mu.RLock()
	g := l.gen
	l.mu.RUnlock()
	if g == nil {
		return 0, errNoNode
	}
	return g.Next()
}

// startIDs gives the process its Snowflake generator: from NODE_ID, or by leasing a free node number from
// Redis (retried until Redis answers). Losing the lease stops the process, because another instance may
// then generate the same IDs.
func startIDs(p *app.Process, rdb redis.Client, instance string, st *realtimeStack) *lazyIDs {
	l := &lazyIDs{}
	if n := p.Cfg.Realtime.NodeID; n != nil {
		g, err := ids.NewGenerator(*n)
		if err == nil {
			l.set(g)
		}
		return l
	}
	st.done.Go(func() {
		for {
			lease, err := redis.AcquireNode(st.ctx, rdb, "", instance, ids.MaxNode, redis.NodeLeaseTTL)
			if err == nil {
				g, gerr := ids.NewGenerator(lease.Node())
				if gerr != nil {
					p.Fail(gerr)
					return
				}
				// Continue after the previous holder's IDs, and only while the lease is fresh.
				g.Resume(lease.HighWater())
				g.ValidUntil(time.Now().Add(redis.NodeLeaseTTL / 2))
				kept := make(chan error, 1)
				go func() { kept <- lease.Keep(st.ctx, g) }()
				if q := lease.Quarantine(); q > 0 {
					// Redis started empty and may have forgotten a lease that is still in use.
					p.Log.Warn("redis has no lease history, waiting before issuing ids", "wait", q.String())
					select {
					case <-st.ctx.Done():
					case err := <-kept:
						kept <- err
					case <-time.After(q):
					}
				}
				l.set(g)
				p.Log.Info("id node leased", "node", lease.Node())
				if err := <-kept; err != nil {
					l.set(nil)
					p.Log.Error("id node lease lost, stopping", "error", err)
					p.Fail(err)
				}
				return
			}
			p.Log.Warn("lease id node", "error", err)
			select {
			case <-st.ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	})
	return l
}

func startRealtime(p *app.Process, pg *postgres.Store, rd *redis.Store, sc *scylla.Store, ticketAuth realtime.TicketRedeemer) (*realtimeStack, error) {
	cfg, log := p.Cfg, p.Log
	st := &realtimeStack{hub: realtime.NewHub(log, p.Metrics)}
	st.ctx, st.cancel = context.WithCancel(context.Background())
	instance := app.InstanceID(cfg.Realtime.InstanceID)
	rdb := rd.Client()
	idsrc := startIDs(p, rdb, instance, st)

	repo := scylla.NewMessages(sc, cfg.Scylla.Timeout)
	st.events = events.NewLog(redis.NewStreams(rdb, "", 0))
	members := redis.NewMembers(rdb, "", cfg.Redis.MembersCacheTTL)

	var gw *realtime.Gateway
	bus, err := redis.NewBus(rdb, redis.BusOptions{
		Handler:       func(uid int64, payload []byte) { gw.Deliver(uid, payload) },
		OnResubscribe: func(uid int64) { gw.Resubscribed(uid) },
		OnBroadcast:   func(payload []byte) { gw.Control(payload) },
	})
	if err != nil {
		return nil, err
	}
	presence, err := redis.NewPresence(rdb, redis.PresenceOptions{
		Instance: instance, TTL: cfg.Realtime.PresenceTTL, Log: log,
		OnChange: func(cs []redis.Change) { gw.PresenceChanged(cs) },
	})
	if err != nil {
		return nil, err
	}
	dir := users.NewDirectory(pg, presence, cfg.HTTP.BasePath)

	var svc *messages.Service
	fan := realtime.NewFanout(realtime.FanoutDeps{
		Bus: bus, Directory: dir, Store: pg, IDs: idsrc, BasePath: cfg.HTTP.BasePath, Log: log,
		Members:   func(ctx context.Context, chatID int64) ([]int64, error) { return svc.Members(ctx, chatID) },
		Reactions: repo.Reactions,
	})
	svc = messages.New(messages.Deps{
		Messages: repo, Store: pg, Members: members, Unread: redis.NewUnread(rdb, ""),
		Dedup: redis.NewDedup(rdb, "", 0), IDs: idsrc, Notifier: fan, Accepted: p.Metrics.MessageAccepted, Log: log,
		Events: st.events,
	})
	gw, err = realtime.New(realtime.Options{
		Auth: ticketAuth, Messages: svc, Fanout: fan, Bus: bus, Presence: presence,
		Limiter: redis.NewRateLimiter(rdb, ""), LastSeen: pg.Users(), Hub: st.hub,
		AllowedOrigins: cfg.HTTP.CORSOrigins, MinClientAPIVersion: cfg.Realtime.MinClientAPIVersion,
		SendBuffer: cfg.Realtime.SendBuffer, PingInterval: cfg.Realtime.PingInterval, Log: log,
	})
	if err != nil {
		return nil, fmt.Errorf("realtime: %w", err)
	}
	st.gateway, st.fanout, st.messages, st.bus, st.directory = gw, fan, svc, bus, dir
	st.limiter = redis.NewRateLimiter(rdb, "")
	st.members, st.unread, st.store = members, redis.NewUnread(rdb, ""), repo
	st.done.Go(func() { bus.Run(st.ctx) })
	st.done.Go(func() { presence.Run(st.ctx) })
	return st, nil
}

// closeSessions is the auth service's revocation hook.
func (st *realtimeStack) closeSessions(ctx context.Context, sessions []uuid.UUID) {
	if st.fanout != nil {
		st.fanout.CloseSessions(ctx, sessions)
	}
}

// stop closes the open sockets with the reconnect code (each one releases its presence on the way out),
// then stops the gateway and waits for the background loops.
func (st *realtimeStack) stop(ctx context.Context) {
	st.hub.Shutdown(ctx)
	st.gateway.Wait(ctx) // every socket releases its presence and records last-seen
	st.gateway.Close()
	st.cancel()
	st.done.Wait()
}

// accountDeleted cleans up after a deleted account: the chats it took along are announced to their other
// members and handed to the worker (which deletes their messages), the groups it left get a fresh member list,
// and the files of v1 chats are deleted.
func (st *realtimeStack) accountDeleted(storage media.Storage, log *slog.Logger) func(ctx context.Context, userID int64, d postgres.DeletedAccount) {
	return func(ctx context.Context, userID int64, d postgres.DeletedAccount) {
		changed := make([]int64, 0, len(d.DeletedChats)+len(d.LeftGroups))
		for chatID, others := range d.DeletedChats {
			changed = append(changed, chatID)
			st.fanout.Removed(ctx, chatID, others)
			if err := st.unread.Forget(ctx, chatID, others...); err != nil {
				log.WarnContext(ctx, "forget unread", "error", err)
			}
			if err := st.events.Emit(ctx, events.Event{Type: events.ChatDeleted, ChatID: chatID, ActorID: userID}); err != nil {
				log.WarnContext(ctx, "emit chat.deleted", "error", err)
			}
		}
		for _, chatID := range d.LeftGroups {
			changed = append(changed, chatID)
			if err := st.events.Emit(ctx, events.Event{Type: events.ChatMemberRemoved, ChatID: chatID, UserID: userID, ActorID: userID}); err != nil {
				log.WarnContext(ctx, "emit chat.member_removed", "error", err)
			}
		}
		if err := st.members.Invalidate(ctx, changed...); err != nil {
			log.WarnContext(ctx, "invalidate members", "error", err)
		}
		if err := st.unread.Drop(ctx, userID); err != nil {
			log.WarnContext(ctx, "drop unread counters", "error", err)
		}
		for _, key := range d.AttachmentKeys {
			if err := storage.Delete(ctx, key); err != nil {
				log.WarnContext(ctx, "delete file of a deleted chat", "error", err)
			}
		}
	}
}

// searchAccess tells the searcher which chats a user is in and which messages they can see.
type searchAccess struct {
	*messages.Service
	pg *postgres.Store
}

func (a searchAccess) ChatIDs(ctx context.Context, userID int64) ([]int64, error) {
	return a.pg.Social().ChatIDs(ctx, userID)
}
