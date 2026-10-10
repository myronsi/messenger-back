package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/version"
)

// Defaults of the gateway.
const (
	DefaultSendBuffer   = 256
	DefaultPingInterval = 25 * time.Second
	writeTimeout        = 10 * time.Second
	eventTimeout        = 10 * time.Second
)

// TicketRedeemer turns a WebSocket ticket into the session it was issued for, and tells whether a session is
// still active.
type TicketRedeemer interface {
	RedeemTicket(ctx context.Context, ticket string) (auth.Principal, error)
	SessionActive(ctx context.Context, p auth.Principal) (bool, error)
}

// Subscriber is the bus as the gateway uses it.
type Subscriber interface {
	Subscribe(ctx context.Context, userID int64) error
	Unsubscribe(ctx context.Context, userID int64) error
}

// PresenceTracker is presence as the gateway uses it.
type PresenceTracker interface {
	Connect(ctx context.Context, userID int64) (redis.Change, bool, error)
	Disconnect(ctx context.Context, userID int64) (redis.Change, bool, error)
}

// Limiter limits how often a user may do an action across instances.
type Limiter interface {
	Allow(ctx context.Context, r redis.Rate, subject string) (redis.Result, error)
}

// LastSeen records when a user went offline.
type LastSeen interface {
	TouchLastSeen(ctx context.Context, id int64) error
}

// Rates of the client actions, per user across all connections and instances.
var (
	RateSend   = redis.Rate{Name: "ws_send", Rate: 30, Period: 10 * time.Second, Burst: 20}
	RateTyping = redis.Rate{Name: "ws_typing", Rate: 1, Period: time.Second, Burst: 5}
	RateOther  = redis.Rate{Name: "ws_action", Rate: 60, Period: 10 * time.Second, Burst: 40}
)

// Options configures the gateway.
type Options struct {
	Auth     TicketRedeemer
	Messages *messages.Service
	Fanout   *Fanout
	Bus      Subscriber
	Presence PresenceTracker
	Limiter  Limiter
	LastSeen LastSeen
	Hub      *Hub
	// AllowedOrigins are the CORS origins (https://app.example.com); same-origin requests are always allowed.
	AllowedOrigins []string
	// MinClientAPIVersion is the oldest contract version still served.
	MinClientAPIVersion string
	SendBuffer          int
	PingInterval        time.Duration
	Log                 *slog.Logger
}

// Gateway serves /ws: one connection per browser tab, carrying the events of all chats of the user.
type Gateway struct {
	o       Options
	origins []string
	min     version.SemVer
	// ctx outlives the HTTP request that opened a connection; it ends when the gateway shuts down.
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	users map[int64]*userState

	// conns counts the running connection handlers, bg the background work they started; shutdown waits for
	// both, so every socket releases its presence and records last-seen before the stores close.
	conns sync.WaitGroup
	bg    sync.WaitGroup
}

// userState is what this instance knows about one connected user.
type userState struct {
	conns map[*conn]struct{}
	// removed holds the chats the user left or was removed from while connected: their events are dropped
	// until the user is added again.
	removed map[int64]bool
	// presence holds the last presence version forwarded per subject.
	presence map[int64]int64
}

// New returns the gateway.
func New(o Options) (*Gateway, error) {
	if o.SendBuffer <= 0 {
		o.SendBuffer = DefaultSendBuffer
	}
	if o.PingInterval <= 0 {
		o.PingInterval = DefaultPingInterval
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.MinClientAPIVersion == "" {
		o.MinClientAPIVersion = version.API
	}
	minVer, err := version.Parse(o.MinClientAPIVersion)
	if err != nil {
		return nil, err
	}
	origins := make([]string, 0, len(o.AllowedOrigins))
	for _, raw := range o.AllowedOrigins {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			origins = append(origins, u.Host)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Gateway{o: o, origins: origins, min: minVer, ctx: ctx, cancel: cancel, users: make(map[int64]*userState)}, nil
}

// ServeHTTP upgrades the request, authenticates it with the ticket and runs the connection until it closes.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.conns.Add(1)
	defer g.conns.Done()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: g.origins})
	if err != nil {
		return // Accept has answered the request
	}
	ws.SetReadLimit(MaxClientEvent)

	ctx, cancel := context.WithTimeout(g.ctx, eventTimeout)
	p, err := g.o.Auth.RedeemTicket(ctx, r.URL.Query().Get("ticket"))
	cancel()
	if err != nil {
		code, reason := websocket.StatusCode(CloseInvalidTicket), "invalid ticket"
		if !errors.Is(err, auth.ErrUnauthenticated) {
			code, reason = websocket.StatusTryAgainLater, "try again"
			g.o.Log.WarnContext(r.Context(), "redeem ticket", "error", err)
		}
		_ = ws.Close(code, reason)
		return
	}

	c := newConn(g, ws, p.UserID, p.SessionID)
	if !g.o.Hub.Add(c) {
		_ = ws.Close(CloseServiceRestart, ReconnectReason)
		return
	}
	defer g.o.Hub.Remove(c)

	c.enqueue(g.hello(p.UserID))
	if v := r.URL.Query().Get("api_version"); v != "" {
		if cv, err := version.Parse(v); err != nil || !version.Supported(cv, g.min) {
			// The client learns the current versions from hello, then the connection ends; it is not
			// registered and none of its events are handled.
			c.closeAfterFlush(CloseClientOutdated, "client outdated")
			c.writeLoop()
			return
		}
	}
	if err := g.register(c); err != nil {
		g.o.Log.WarnContext(r.Context(), "register connection", "error", err)
		g.unregister(c)
		_ = ws.Close(websocket.StatusTryAgainLater, "try again")
		return
	}
	defer g.unregister(c)
	// A revocation broadcast between redeeming the ticket and registering found no socket to close.
	if !g.sessionActive(c) {
		_ = ws.Close(CloseInvalidTicket, "session ended")
		return
	}
	c.run()
}

func (g *Gateway) hello(userID int64) []byte {
	raw, _ := g.o.Fanout.encode("hello", 0, map[string]any{
		"api_version": version.API, "min_client_api_version": g.o.MinClientAPIVersion, "user_id": idString(userID),
	}, "")
	return raw
}

// sessionActive re-checks the connection's session; when the answer is unknown the socket stays (the next
// check decides).
func (g *Gateway) sessionActive(c *conn) bool {
	ctx, cancel := context.WithTimeout(g.ctx, eventTimeout)
	defer cancel()
	ok, err := g.o.Auth.SessionActive(ctx, auth.Principal{UserID: c.userID, SessionID: c.sessionID})
	if err != nil {
		g.o.Log.WarnContext(ctx, "session check", "error", err)
		return true
	}
	return ok
}

// register adds the connection; the user's first connection on this instance subscribes the user's channel
// and claims presence. Without a subscription the socket would silently miss events, so that fails the
// connection (the client reconnects).
func (g *Gateway) register(c *conn) error {
	g.mu.Lock()
	st, ok := g.users[c.userID]
	if !ok {
		st = &userState{conns: map[*conn]struct{}{}, removed: map[int64]bool{}, presence: map[int64]int64{}}
		g.users[c.userID] = st
	}
	st.conns[c] = struct{}{}
	g.mu.Unlock()

	ctx, cancel := context.WithTimeout(g.ctx, eventTimeout)
	defer cancel()
	// A few quick attempts ride out a reconnecting pub/sub connection.
	var err error
	for attempt := range 3 {
		if err = g.o.Bus.Subscribe(ctx, c.userID); err == nil {
			break
		}
		g.o.Log.WarnContext(ctx, "subscribe user", "error", err, "attempt", attempt+1)
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
		}
	}
	if err != nil {
		return err
	}
	change, changed, err := g.o.Presence.Connect(ctx, c.userID)
	if err != nil {
		g.o.Log.WarnContext(ctx, "presence connect", "error", err)
	}
	if changed {
		// In the background: the connection's writer must start before events can pile up.
		g.bg.Go(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(g.ctx), eventTimeout)
			defer cancel()
			g.o.Fanout.Presence(ctx, change, time.Time{})
		})
	}
	return nil
}

func (g *Gateway) unregister(c *conn) {
	g.mu.Lock()
	if st, ok := g.users[c.userID]; ok {
		delete(st.conns, c)
		if len(st.conns) == 0 {
			delete(g.users, c.userID)
		}
	}
	g.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(g.ctx), eventTimeout)
	defer cancel()
	if err := g.o.Bus.Unsubscribe(ctx, c.userID); err != nil {
		g.o.Log.WarnContext(ctx, "unsubscribe user", "error", err)
	}
	change, changed, err := g.o.Presence.Disconnect(ctx, c.userID)
	if err != nil {
		g.o.Log.WarnContext(ctx, "presence disconnect", "error", err)
	}
	if changed {
		g.wentOffline(ctx, change)
	}
}

func (g *Gateway) wentOffline(ctx context.Context, change redis.Change) {
	if err := g.o.LastSeen.TouchLastSeen(ctx, change.UserID); err != nil {
		g.o.Log.WarnContext(ctx, "record last seen", "error", err)
	}
	g.o.Fanout.Presence(ctx, change, time.Now())
}

// PresenceChanged handles the transitions presence detects in the background (a crashed instance's users
// going offline, users coming back after Redis lost their claim).
func (g *Gateway) PresenceChanged(changes []redis.Change) {
	g.bg.Go(func() {
		ctx, cancel := context.WithTimeout(g.ctx, time.Minute)
		defer cancel()
		for _, c := range changes {
			if c.Online {
				g.o.Fanout.Presence(ctx, c, time.Time{})
			} else {
				g.wentOffline(ctx, c)
			}
		}
	})
}

// Deliver handles a frame published to a user served here (the bus handler). It must not block.
func (g *Gateway) Deliver(userID int64, payload []byte) {
	var f frame
	if err := json.Unmarshal(payload, &f); err != nil || len(f.Event) == 0 {
		return
	}
	g.mu.Lock()
	st, ok := g.users[userID]
	if !ok {
		g.mu.Unlock()
		return
	}
	switch f.Kind {
	case kindRemoved:
		st.removed[f.ChatID] = true
	case kindPresence:
		if f.Version <= st.presence[f.Subject] {
			g.mu.Unlock()
			return // an older change overtook a newer one on the way
		}
		st.presence[f.Subject] = f.Version
	default:
		if f.ChatID != 0 && st.removed[f.ChatID] {
			if !rejoins(f.Event) {
				g.mu.Unlock()
				return
			}
			delete(st.removed, f.ChatID)
		}
	}
	conns := make([]*conn, 0, len(st.conns))
	for c := range st.conns {
		conns = append(conns, c)
	}
	g.mu.Unlock()
	for _, c := range conns {
		c.enqueue(f.Event)
	}
}

// rejoins reports whether an event puts the user back into a chat they had been removed from.
func rejoins(event json.RawMessage) bool {
	var head struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(event, &head)
	return head.Type == "chat_created" || head.Type == "group_created"
}

// Resubscribed is called when the bus renewed a user's subscription after losing Redis: events may have
// been lost, so the user's connections are closed with the reconnect code and the clients catch up.
func (g *Gateway) Resubscribed(userID int64) {
	for _, c := range g.connsOf(func(c *conn) bool { return c.userID == userID }) {
		go c.Close(CloseServiceRestart, ReconnectReason) //nolint:errcheck // best effort
	}
}

// Control handles a broadcast from another instance.
func (g *Gateway) Control(payload []byte) {
	var ctl control
	if err := json.Unmarshal(payload, &ctl); err != nil {
		return
	}
	if len(ctl.CloseSessions) > 0 {
		revoked := make(map[uuid.UUID]bool, len(ctl.CloseSessions))
		for _, id := range ctl.CloseSessions {
			revoked[id] = true
		}
		for _, c := range g.connsOf(func(c *conn) bool { return revoked[c.sessionID] }) {
			go c.Close(CloseInvalidTicket, "session ended") //nolint:errcheck // best effort
		}
	}
}

func (g *Gateway) connsOf(match func(*conn) bool) []*conn {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []*conn
	for _, st := range g.users {
		for c := range st.conns {
			if match(c) {
				out = append(out, c)
			}
		}
	}
	return out
}

// Wait blocks until every connection handler and its background work has finished, or ctx is done. Call it
// after the hub's Shutdown closed the sockets.
func (g *Gateway) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		g.conns.Wait()
		g.bg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		g.o.Log.WarnContext(ctx, "websocket handlers still running at shutdown")
	}
}

// Close stops the gateway's background work; open connections are closed by the hub's Shutdown.
func (g *Gateway) Close() { g.cancel() }
