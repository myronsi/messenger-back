package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/testenv"
	"github.com/myronsi/messenger-back/internal/users"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testenv.DropKeyspace()
	os.Exit(code)
}

// tickets is a fake ticket store: the tests mint tickets for a user and session.
type tickets struct {
	mu      sync.Mutex
	m       map[string]auth.Principal
	revoked map[uuid.UUID]bool
}

func (t *tickets) issue(userID int64) (string, uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tk, sid := uuid.NewString(), uuid.New()
	t.m[tk] = auth.Principal{UserID: userID, SessionID: sid}
	return tk, sid
}

func (t *tickets) SessionActive(_ context.Context, p auth.Principal) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.revoked[p.SessionID], nil
}

func (t *tickets) RedeemTicket(_ context.Context, ticket string) (auth.Principal, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.m[ticket]
	if !ok {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	delete(t.m, ticket)
	return p, nil
}

// env is the shared state of a test: the stores and the fake ticket issuer.
type env struct {
	t       *testing.T
	pg      *postgres.Store
	repo    *scylla.Messages
	prefix  string
	tickets *tickets
	node    int
	// ping is the ping (and session re-check) interval of new instances.
	ping time.Duration
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pg := testenv.Postgres(t)
	_, prefix := testenv.Redis(t)
	sc := testenv.Scylla(t)
	return &env{t: t, pg: pg, repo: scylla.NewMessages(sc, 10*time.Second), prefix: prefix, tickets: &tickets{m: map[string]auth.Principal{}, revoked: map[uuid.UUID]bool{}}, ping: time.Hour}
}

// instance is one API process: its own Redis connection, bus, presence, fan-out, service and gateway.
type instance struct {
	hub      *Hub
	gw       *Gateway
	fanout   *Fanout
	svc      *messages.Service
	members  *redis.Members
	server   *httptest.Server
	presence *redis.Presence
}

func (e *env) instance(name string) *instance {
	t := e.t
	t.Helper()
	rd, _ := testenv.Redis(t)
	rdb := rd.Client()
	e.node++
	gen, err := ids.NewGenerator(e.node)
	if err != nil {
		t.Fatal(err)
	}
	in := &instance{}
	bus, err := redis.NewBus(rdb, redis.BusOptions{
		Prefix:        e.prefix,
		Handler:       func(uid int64, p []byte) { in.gw.Deliver(uid, p) },
		OnResubscribe: func(uid int64) { in.gw.Resubscribed(uid) },
		OnBroadcast:   func(p []byte) { in.gw.Control(p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	in.presence, err = redis.NewPresence(rdb, redis.PresenceOptions{Prefix: e.prefix, Instance: name, TTL: time.Minute,
		OnChange: func(cs []redis.Change) { in.gw.PresenceChanged(cs) }})
	if err != nil {
		t.Fatal(err)
	}
	dir := users.NewDirectory(e.pg, in.presence, "/api/v2")
	in.members = redis.NewMembers(rdb, e.prefix, time.Minute)
	in.fanout = NewFanout(FanoutDeps{
		Bus: bus, Directory: dir, Store: e.pg, IDs: gen, BasePath: "/api/v2",
		Members:   func(ctx context.Context, chatID int64) ([]int64, error) { return in.svc.Members(ctx, chatID) },
		Reactions: e.repo.Reactions,
	})
	in.svc = messages.New(messages.Deps{
		Messages: e.repo, Store: e.pg, Members: in.members, Unread: redis.NewUnread(rdb, e.prefix),
		Dedup: redis.NewDedup(rdb, e.prefix, 0), IDs: gen, Notifier: in.fanout,
	})
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("instance %s log:\n%s", name, logs.String())
		}
	})
	hub := NewHub(log, nil)
	in.hub = hub
	in.gw, err = New(Options{
		Auth: e.tickets, Messages: in.svc, Fanout: in.fanout, Bus: bus, Presence: in.presence,
		Limiter: redis.NewRateLimiter(rdb, e.prefix), LastSeen: e.pg.Users(), Hub: hub,
		MinClientAPIVersion: "2.0.0-alpha.1", PingInterval: e.ping, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); bus.Run(ctx) }()
	mux := http.NewServeMux()
	mux.Handle("GET /api/v2/ws", in.gw)
	in.server = httptest.NewServer(mux)
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		hub.Shutdown(sctx)
		in.server.Close()
		in.gw.Close()
		cancel()
		<-done
	})
	return in
}

// client is a test WebSocket client that collects every event.
type client struct {
	t      *testing.T
	ws     *websocket.Conn
	events chan map[string]any
	closed chan websocket.CloseError
	userID int64
	// pending holds events that arrived while waiting for another type.
	pending []map[string]any
}

func (e *env) connect(in *instance, userID int64, query string) *client {
	t := e.t
	t.Helper()
	tk, _ := e.tickets.issue(userID)
	return e.dial(in, userID, "ticket="+tk+query)
}

func (e *env) dial(in *instance, userID int64, query string) *client {
	t := e.t
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(in.server.URL, "http")+"/api/v2/ws?"+query, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &client{t: t, ws: ws, events: make(chan map[string]any, 256), closed: make(chan websocket.CloseError, 1), userID: userID}
	go func() {
		for {
			_, b, err := ws.Read(context.Background())
			if err != nil {
				var ce websocket.CloseError
				if errors.As(err, &ce) {
					c.closed <- ce
				}
				close(c.events)
				return
			}
			var ev map[string]any
			if err := json.Unmarshal(b, &ev); err != nil {
				t.Errorf("not JSON: %s", b)
				continue
			}
			if err := eventViolation(b); err != nil {
				t.Errorf("event breaks the contract: %v", err)
			}
			c.events <- ev
		}
	}()
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	return c
}

// ready connects and waits for hello, then until the user's subscription is live.
func (e *env) online(in *instance, userID int64) *client {
	c := e.connect(in, userID, "")
	hello := c.next("hello")
	data := hello["data"].(map[string]any)
	if data["user_id"] != fmt.Sprint(userID) || data["api_version"] == "" || hello["chat_id"] != nil {
		e.t.Fatalf("hello: %v", hello)
	}
	return c
}

func (c *client) send(ev map[string]any) {
	c.t.Helper()
	b, _ := json.Marshal(ev)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// next returns the oldest event of the type. Events of other types stay queued for later calls: the order
// between, say, an ack and the sender's own copy of the message is not fixed.
func (c *client) next(typ string) map[string]any {
	c.t.Helper()
	for i, ev := range c.pending {
		if ev["type"] == typ {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return ev
		}
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				c.t.Fatalf("user %d: connection closed while waiting for %s", c.userID, typ)
			}
			if ev["type"] == typ {
				return ev
			}
			if ev["type"] != "typing" && ev["type"] != "presence" || typ == "presence" || typ == "typing" {
				c.pending = append(c.pending, ev)
			}
		case <-deadline:
			c.t.Fatalf("user %d: no %s event", c.userID, typ)
		}
	}
}

// quiet fails when an event of the type is queued or arrives within a short time.
func (c *client) quiet(typ string) {
	c.t.Helper()
	for _, ev := range c.pending {
		if ev["type"] == typ {
			c.t.Fatalf("user %d: unexpected %s: %v", c.userID, typ, ev)
		}
	}
	deadline := time.After(400 * time.Millisecond)
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				return
			}
			if ev["type"] == typ {
				c.t.Fatalf("user %d: unexpected %s: %v", c.userID, typ, ev)
			}
		case <-deadline:
			return
		}
	}
}

func (c *client) closeCode() websocket.StatusCode {
	c.t.Helper()
	select {
	case ce := <-c.closed:
		return ce.Code
	case <-time.After(5 * time.Second):
		c.t.Fatal("connection not closed")
	}
	return 0
}

func data(ev map[string]any) map[string]any { d, _ := ev["data"].(map[string]any); return d }

func (e *env) user(name string) int64 {
	e.t.Helper()
	u, err := e.pg.Users().Register(context.Background(), name, strings.ToUpper(name[:1])+name[1:], "hash")
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

// waitDelivery sends probes (typing events) until the other user receives one: SUBSCRIBE is asynchronous,
// so a fresh connection can miss the first events.
func waitDelivery(t *testing.T, from *client, to *client, chatID int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		from.send(map[string]any{"type": "typing", "chat_id": fmt.Sprint(chatID), "data": map[string]any{"is_typing": false}})
		select {
		case ev := <-to.events:
			if ev["type"] == "typing" {
				return
			}
		case <-time.After(150 * time.Millisecond):
		}
	}
	t.Fatal("events never arrived")
}

func text(clientTempID string, chatID int64, content string) map[string]any {
	return map[string]any{"type": "message", "client_temp_id": clientTempID, "chat_id": fmt.Sprint(chatID),
		"data": map[string]any{"type": "text", "content": content}}
}

// A user connected to instance A receives messages sent through instance B.
func TestMessagesReachUsersOnOtherInstances(t *testing.T) {
	e := newEnv(t)
	a, b := e.instance("a"), e.instance("b")
	alice, bob := e.user("alice"), e.user("bob")
	chat, _, err := e.pg.Chats().CreateDirect(context.Background(), alice, bob)
	if err != nil {
		t.Fatal(err)
	}
	ca := e.online(a, alice)
	cb := e.online(b, bob)
	waitDelivery(t, cb, ca, chat.ID)

	cb.send(text("t-1", chat.ID, "hello alice"))
	ack := cb.next("ack")
	if ack["client_temp_id"] != "t-1" || ack["chat_id"] != fmt.Sprint(chat.ID) || data(ack)["message_id"] == nil || data(ack)["created_at"] == nil {
		t.Fatalf("ack: %v", ack)
	}
	id := data(ack)["message_id"].(string)

	got := ca.next("message")
	m := data(got)["message"].(map[string]any)
	if m["id"] != id || m["content"] != "hello alice" || got["chat_id"] != fmt.Sprint(chat.ID) || m["client_temp_id"] != nil {
		t.Fatalf("alice got: %v", got)
	}
	sender := m["sender"].(map[string]any)
	if sender["id"] != fmt.Sprint(bob) || sender["username"] != "bob" || sender["is_online"] != true {
		t.Fatalf("sender as alice sees him: %v", sender)
	}
	// The sender's own copy carries the client_temp_id, so the client can replace its pending message.
	own := data(cb.next("message"))["message"].(map[string]any)
	if own["id"] != id || own["client_temp_id"] != "t-1" {
		t.Fatalf("bob's copy: %v", own)
	}

	// The same client_temp_id again: the same message, no copy.
	cb.send(text("t-1", chat.ID, "hello alice"))
	if again := cb.next("ack"); data(again)["message_id"] != id {
		t.Fatalf("retry created another message: %v", again)
	}
	ca.quiet("message")

	// Edits, reactions, reads and deletes travel the same way.
	cb.send(map[string]any{"type": "edit", "client_temp_id": "t-2", "chat_id": fmt.Sprint(chat.ID), "data": map[string]any{"message_id": id, "content": "hi alice"}})
	cb.next("ack")
	if ed := data(ca.next("edit"))["message"].(map[string]any); ed["content"] != "hi alice" || ed["edited_at"] == nil {
		t.Fatalf("edit: %v", ed)
	}
	ca.send(map[string]any{"type": "reaction_add", "client_temp_id": "t-3", "chat_id": fmt.Sprint(chat.ID), "data": map[string]any{"message_id": id, "emoji": "👍"}})
	ca.next("ack")
	if rx := data(cb.next("reaction_add")); rx["message_id"] != id || rx["reaction"].(map[string]any)["user_id"] != fmt.Sprint(alice) {
		t.Fatalf("reaction: %v", rx)
	}
	ca.send(map[string]any{"type": "read", "client_temp_id": "t-4", "chat_id": fmt.Sprint(chat.ID), "data": map[string]any{"message_id": id}})
	ca.next("ack")
	if rd := data(cb.next("read")); rd["user_id"] != fmt.Sprint(alice) || rd["message_id"] != id {
		t.Fatalf("read: %v", rd)
	}
	cb.send(map[string]any{"type": "delete", "client_temp_id": "t-5", "chat_id": fmt.Sprint(chat.ID), "data": map[string]any{"message_id": id, "scope": "everyone"}})
	cb.next("ack")
	if del := data(ca.next("delete")); del["message_id"] != id || del["scope"] != "everyone" {
		t.Fatalf("delete: %v", del)
	}
}

// A user removed from a group stops receiving its messages immediately.
func TestRemovedMemberStopsReceiving(t *testing.T) {
	e := newEnv(t)
	a, b := e.instance("a"), e.instance("b")
	ctx := context.Background()
	owner, carol := e.user("owner"), e.user("carol")
	group, err := e.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: owner, Name: "Team", MemberIDs: []int64{carol}})
	if err != nil {
		t.Fatal(err)
	}
	co := e.online(a, owner)
	cc := e.online(b, carol)
	waitDelivery(t, co, cc, group.ID)

	co.send(text("m-1", group.ID, "before"))
	co.next("ack")
	if m := data(cc.next("message"))["message"].(map[string]any); m["content"] != "before" {
		t.Fatalf("carol before removal: %v", m)
	}

	// What removing a member does: commit, invalidate the cache, tell the user.
	if err := e.pg.Chats().RemoveMember(ctx, group.ID, carol); err != nil {
		t.Fatal(err)
	}
	if err := a.members.Invalidate(ctx, group.ID); err != nil {
		t.Fatal(err)
	}
	a.fanout.Removed(ctx, group.ID, []int64{carol})
	if ev := cc.next("chat_deleted"); ev["chat_id"] != fmt.Sprint(group.ID) {
		t.Fatalf("removal event: %v", ev)
	}

	co.send(text("m-2", group.ID, "after"))
	co.next("ack")
	cc.quiet("message")

	// A send that loaded the members just before the removal and publishes late is dropped as well.
	late, _ := json.Marshal(map[string]any{"type": "message", "chat_id": fmt.Sprint(group.ID), "event_id": "1", "data": map[string]any{}})
	a.fanout.publish(ctx, map[int64]frame{carol: {Kind: kindEvent, ChatID: group.ID, Event: late}})
	cc.quiet("message")

	// Carol can no longer act in the group either.
	cc.send(text("m-3", group.ID, "let me in"))
	if ev := cc.next("error"); data(ev)["code"] != "not_found" || ev["client_temp_id"] != "m-3" {
		t.Fatalf("send after removal: %v", ev)
	}
}

func TestInvalidTicketIsRefused(t *testing.T) {
	e := newEnv(t)
	a := e.instance("a")
	c := e.dial(a, 1, "ticket=forged")
	if code := c.closeCode(); code != CloseInvalidTicket {
		t.Fatalf("close code %d", code)
	}
	// A ticket works once.
	tk, _ := e.tickets.issue(e.user("dave"))
	first := e.dial(a, 0, "ticket="+tk)
	first.next("hello")
	second := e.dial(a, 0, "ticket="+tk)
	if code := second.closeCode(); code != CloseInvalidTicket {
		t.Fatalf("reused ticket: close code %d", code)
	}
}

func TestOutdatedClientGetsHelloThenClose(t *testing.T) {
	e := newEnv(t)
	a := e.instance("a")
	c := e.connect(a, e.user("erin"), "&api_version=1.4.0")
	hello := c.next("hello")
	if data(hello)["min_client_api_version"] != "2.0.0-alpha.1" {
		t.Fatalf("hello: %v", hello)
	}
	if code := c.closeCode(); code != CloseClientOutdated {
		t.Fatalf("close code %d", code)
	}
	// A current client stays connected.
	ok := e.connect(a, e.user("frank"), "&api_version=2.0.0-alpha.2")
	ok.next("hello")
	ok.quiet("never")
}

func TestErrorsReferenceTheClientEvent(t *testing.T) {
	e := newEnv(t)
	a := e.instance("a")
	alice, bob := e.user("alice"), e.user("bob")
	chat, _, _ := e.pg.Chats().CreateDirect(context.Background(), alice, bob)
	c := e.online(a, alice)

	c.send(map[string]any{"type": "shout"})
	if ev := c.next("error"); data(ev)["code"] != "validation_failed" {
		t.Fatalf("unknown event: %v", ev)
	}
	b, _ := json.Marshal(map[string]any{"type": "message", "client_temp_id": "x"})
	_ = c.ws.Write(context.Background(), websocket.MessageText, append(b[:len(b)-1], ',')) // broken JSON
	if ev := c.next("error"); data(ev)["code"] != "invalid_request" || ev["client_temp_id"] != nil {
		t.Fatalf("broken JSON: %v", ev)
	}
	// Somebody else's chat looks like a chat that does not exist.
	other, _, _ := e.pg.Chats().CreateDirect(context.Background(), bob, e.user("carol"))
	c.send(text("y", other.ID, "hi"))
	if ev := c.next("error"); data(ev)["code"] != "not_found" || ev["client_temp_id"] != "y" || ev["chat_id"] != fmt.Sprint(other.ID) {
		t.Fatalf("foreign chat: %v", ev)
	}
	// Blocked users cannot message each other.
	if err := e.pg.Social().Block(context.Background(), bob, alice); err != nil {
		t.Fatal(err)
	}
	c.send(text("z", chat.ID, "hi"))
	if ev := c.next("error"); data(ev)["code"] != "blocked_by_user" {
		t.Fatalf("blocked: %v", ev)
	}
}

func TestPresenceAcrossInstances(t *testing.T) {
	e := newEnv(t)
	a, b := e.instance("a"), e.instance("b")
	alice, bob, stranger := e.user("alice"), e.user("bob"), e.user("stranger")
	chat, _, _ := e.pg.Chats().CreateDirect(context.Background(), alice, bob)
	cb := e.online(b, bob)
	cs := e.online(b, stranger)
	_ = chat

	ca := e.online(a, alice)
	on := data(cb.next("presence"))
	if on["user_id"] != fmt.Sprint(alice) || on["is_online"] != true || on["last_seen"] != nil {
		t.Fatalf("online: %v", on)
	}
	// Presence only goes to users who share a chat.
	cs.quiet("presence")

	_ = ca.ws.Close(websocket.StatusNormalClosure, "")
	off := data(cb.next("presence"))
	if off["user_id"] != fmt.Sprint(alice) || off["is_online"] != false || off["last_seen"] == nil {
		t.Fatalf("offline: %v", off)
	}

	// Alice hides her presence from everybody: bob hears nothing.
	p, _ := e.pg.Social().Privacy(context.Background(), []int64{alice})
	settings := p[alice]
	settings.PresenceVisibility = postgres.VisibleNobody
	if _, err := e.pg.Social().UpdatePrivacy(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	e.online(a, alice)
	cb.quiet("presence")
}

func TestRevokedSessionLosesItsSocket(t *testing.T) {
	e := newEnv(t)
	a, b := e.instance("a"), e.instance("b")
	alice := e.user("alice")
	tk, sid := e.tickets.issue(alice)
	c := e.dial(a, alice, "ticket="+tk)
	c.next("hello")
	other := e.online(a, alice) // another session of the same user stays

	// The revocation can be announced from any instance.
	deadline := time.Now().Add(5 * time.Second)
	for {
		b.fanout.CloseSessions(context.Background(), []uuid.UUID{sid})
		select {
		case ce := <-c.closed:
			if ce.Code != CloseInvalidTicket {
				t.Fatalf("close code %d", ce.Code)
			}
			other.quiet("never")
			return
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("socket of the revoked session stayed open")
		}
	}
}

func TestPresenceVersionsAreForwardedInOrder(t *testing.T) {
	e := newEnv(t)
	a := e.instance("a")
	bob := e.user("bob")
	c := e.online(a, bob)
	ctx := context.Background()
	ev := func(online bool) json.RawMessage {
		raw, _ := a.fanout.encode("presence", 0, map[string]any{"user_id": "9", "is_online": online, "last_seen": nil}, "")
		return raw
	}
	// A newer offline change overtakes an older online one: the older one is dropped.
	a.fanout.publish(ctx, map[int64]frame{bob: {Kind: kindPresence, Subject: 9, Version: 20, Event: ev(false)}})
	if d := data(c.next("presence")); d["is_online"] != false {
		t.Fatalf("first change: %v", d)
	}
	a.fanout.publish(ctx, map[int64]frame{bob: {Kind: kindPresence, Subject: 9, Version: 10, Event: ev(true)}})
	c.quiet("presence")
}

// The HTTP server's read and write timeouts must not end an upgraded connection.
func TestConnectionsOutliveServerTimeouts(t *testing.T) {
	e := newEnv(t)
	a := e.instance("a")
	// A second server for the same gateway, with the short timeouts of a production http.Server.
	srv := httptest.NewUnstartedServer(a.gw)
	srv.Config.ReadTimeout = 300 * time.Millisecond
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	alice, bob := e.user("alice"), e.user("bob")
	chat, _, _ := e.pg.Chats().CreateDirect(context.Background(), alice, bob)
	tk, _ := e.tickets.issue(alice)
	c := e.dial(&instance{server: srv}, alice, "ticket="+tk)
	c.next("hello")
	time.Sleep(800 * time.Millisecond)
	c.send(text("late", chat.ID, "still here"))
	if ack := c.next("ack"); ack["client_temp_id"] != "late" {
		t.Fatalf("ack: %v", ack)
	}
}

// Cross-site pages cannot open a socket (cross-site WebSocket hijacking), whatever ticket they hold.
func TestForeignOriginIsRejected(t *testing.T) {
	e := newEnv(t)
	a := e.instance("a")
	tk, _ := e.tickets.issue(e.user("alice"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(a.server.URL, "http")+"/api/v2/ws?ticket="+tk,
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %v %v", resp, err)
	}
}

// A revocation whose broadcast was lost still ends the socket: the session is re-checked periodically.
func TestRevokedSessionIsFoundWithoutBroadcast(t *testing.T) {
	e := newEnv(t)
	e.ping = 100 * time.Millisecond
	a := e.instance("a")
	alice := e.user("alice")
	tk, sid := e.tickets.issue(alice)
	c := e.dial(a, alice, "ticket="+tk)
	c.next("hello")
	e.tickets.mu.Lock()
	e.tickets.revoked[sid] = true
	e.tickets.mu.Unlock()
	if code := c.closeCode(); code != CloseInvalidTicket {
		t.Fatalf("close code %d", code)
	}
}

// On shutdown every socket gets the reconnect code and its user goes offline before the gateway returns.
func TestShutdownReleasesPresence(t *testing.T) {
	e := newEnv(t)
	a, b := e.instance("a"), e.instance("b")
	alice, bob := e.user("alice"), e.user("bob")
	if _, _, err := e.pg.Chats().CreateDirect(context.Background(), alice, bob); err != nil {
		t.Fatal(err)
	}
	cb := e.online(b, bob)
	ca := e.online(a, alice)
	cb.next("presence")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.hub.Shutdown(ctx)
	a.gw.Wait(ctx)
	if code := ca.closeCode(); code != CloseServiceRestart {
		t.Fatalf("close code %d", code)
	}
	if d := data(cb.next("presence")); d["is_online"] != false {
		t.Fatalf("presence after shutdown: %v", d)
	}
	if m, _ := a.presence.Online(context.Background(), alice); m[alice] {
		t.Fatal("alice still online after the shutdown")
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent writers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
