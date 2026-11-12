package httpapi

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/chats"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/linkpreview"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/notifications"
	"github.com/myronsi/messenger-back/internal/observability"
	"github.com/myronsi/messenger-back/internal/search"
	"github.com/myronsi/messenger-back/internal/stickers"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/testenv"
	"github.com/myronsi/messenger-back/internal/users"
)

// chatEvents records what the chat service told the sockets.
type chatEvents struct {
	mu       sync.Mutex
	created  map[int64][]int64 // chat -> users
	updated  map[int64][]int64
	removed  map[int64][]int64
	requests []int64 // recipients
	hidden   map[int64][]int64
	cleared  []clearedEvent

	groupCreated map[int64][]int64
	groupUpdates map[int64]int
}

func (c *chatEvents) add(m map[int64][]int64, chatID int64, views map[int64]chats.View) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for uid := range views {
		m[chatID] = append(m[chatID], uid)
	}
}

func (c *chatEvents) ChatCreated(_ context.Context, chatID int64, v map[int64]chats.View) {
	c.add(c.created, chatID, v)
}

func (c *chatEvents) ChatUpdated(_ context.Context, chatID int64, v map[int64]chats.View) {
	c.add(c.updated, chatID, v)
}

func (c *chatEvents) ChatRemoved(_ context.Context, chatID int64, userIDs []int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removed[chatID] = append(c.removed[chatID], userIDs...)
}

type clearedEvent struct {
	chatID, before int64
	scope          string
	recipients     []int64
}

func (c *chatEvents) ChatHidden(_ context.Context, chatID int64, userIDs []int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hidden == nil {
		c.hidden = map[int64][]int64{}
	}
	c.hidden[chatID] = append(c.hidden[chatID], userIDs...)
}

func (c *chatEvents) HistoryCleared(_ context.Context, chatID, beforeID int64, scope string, _ int64, recipients []int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleared = append(c.cleared, clearedEvent{chatID, beforeID, scope, recipients})
}

func (c *chatEvents) RequestCreated(_ context.Context, r chats.RequestView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, r.Request.RecipientID)
}

type chatEnv struct {
	*authEnv
	tokens    *notifications.ActionTokens
	store     *postgres.Store
	events    *chatEvents
	index     *search.Index
	cacheDown *atomic.Bool
	cleared   *atomic.Int32
}

type flakyMembers struct {
	*redis.Members
	down    *atomic.Bool
	cleared *atomic.Int32
}

func (m flakyMembers) Invalidate(ctx context.Context, chatIDs ...int64) error {
	if m.down.Load() {
		return errors.New("redis is away")
	}
	m.cleared.Add(1)
	return m.Members.Invalidate(ctx, chatIDs...)
}

func newChatEnv(t *testing.T) *chatEnv { return newChatEnvWith(t, false) }

// indexLog hands the message service's events straight to the search indexer, like the worker would.
type indexLog struct{ in *search.Indexer }

func (l indexLog) Emit(ctx context.Context, e events.Event) error {
	fields := map[string]string{}
	for k, v := range e.Fields() {
		switch x := v.(type) {
		case string:
			fields[k] = x
		case int64:
			fields[k] = strconv.FormatInt(x, 10)
		}
	}
	return l.in.Handle(ctx, redis.Entry{ID: "1-0", Fields: fields})
}

// searchAccess answers the searcher from the message service and the social repository.
type searchAccess struct {
	*messages.Service
	pg *postgres.Store
}

func (a searchAccess) ChatIDs(ctx context.Context, userID int64) ([]int64, error) {
	return a.pg.Social().ChatIDs(ctx, userID)
}

func (a searchAccess) ClearedBounds(ctx context.Context, userID int64, chatIDs []int64) (map[int64]int64, error) {
	return a.pg.Chats().ClearedBounds(ctx, userID, chatIDs)
}

// newChatEnvWith is newChatEnv with message search (Elasticsearch, TEST_ELASTIC_URL) when withSearch.
func newChatEnvWith(t *testing.T, withSearch bool) *chatEnv {
	t.Helper()
	store, rdb := newPostgres(t), newRedis(t)
	sc := testenv.Scylla(t)
	log := observability.NewLogger(&bytes.Buffer{}, 0, "test")
	authSvc, err := auth.NewService(store, rdb, auth.Config{
		JWTSecret: bytes.Repeat([]byte("j"), 40), EncryptionKey: bytes.Repeat([]byte("k"), 32),
		RecoveryPepper: bytes.Repeat([]byte("p"), 32), Namespace: "t" + uuid.NewString(),
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	// Every test has its own database but the ScyllaDB keyspace is shared: chat ids must not repeat across tests,
	// or one test reads another one's messages.
	start := 1 + rand.Int64N(1<<40)
	if _, err := store.Pool().Exec(context.Background(), "ALTER TABLE chats ALTER COLUMN id RESTART WITH "+strconv.FormatInt(start, 10)); err != nil {
		t.Fatal(err)
	}
	prefix := "t" + uuid.NewString() + ":"
	repo := scylla.NewMessages(sc, 10*time.Second)
	gen, _ := ids.NewGenerator(1)
	members := redis.NewMembers(rdb, prefix, time.Minute)
	unread := redis.NewUnread(rdb, prefix)
	deps := messages.Deps{
		Messages: repo, Store: store, Members: members, Unread: unread, Dedup: redis.NewDedup(rdb, prefix, 0), IDs: gen,
	}
	var index *search.Index
	if withSearch {
		es, alias := testenv.Elastic(t)
		index = search.NewIndex(es, alias, 0)
		if err := index.Ensure(context.Background()); err != nil {
			t.Fatal(err)
		}
		deps.Events = indexLog{search.NewIndexer(index, repo, store.Attachments(), nil)}
	}
	msgs := messages.New(deps)
	dir := users.NewDirectory(store, nil, apiBase)
	ev := &chatEvents{created: map[int64][]int64{}, updated: map[int64][]int64{}, removed: map[int64][]int64{},
		groupCreated: map[int64][]int64{}, groupUpdates: map[int64]int{}}
	tokens := notifications.NewActionTokens("test-secret")
	cacheDown, cleared := new(atomic.Bool), new(atomic.Int32)
	svc := chats.New(chats.Deps{Store: store, Messages: repo, Unread: unread, Members: flakyMembers{members, cacheDown, cleared}, Sender: msgs, Directory: dir, Notifier: ev})
	api := NewServer(NewAuthServer(AuthOptions{Service: authSvc, Log: log, BasePath: apiBase, CookieSecure: true}), nil).
		WithAccount(NewAccountServer(AccountOptions{Store: store, Directory: dir, Deleter: authSvc, BasePath: apiBase, Log: log})).
		WithChats(NewChatServer(ChatOptions{Service: svc, BasePath: apiBase, Log: log})).
		WithMessages(NewMessageServer(MessageOptions{Service: msgs, Store: store, Directory: dir, BasePath: apiBase, Log: log, Searcher: searcher(index, msgs, store, repo)})).
		WithNotifications(NotificationOptions{Store: store.Notifications(), Tokens: tokens}).
		WithLinkPreviews(NewLinkPreviewServer(LinkPreviewOptions{
			Service:  linkpreview.New(linkpreview.Options{SignKey: []byte("test-key"), AllowAddr: func(netip.Addr) bool { return true }, AnyPort: true}),
			BasePath: apiBase, Log: log,
		})).
		WithStickers(NewStickerServer(StickerOptions{Service: stickers.New(store.Stickers()), BasePath: apiBase, Log: log}))
	return &chatEnv{
		authEnv: &authEnv{router: NewRouter(Options{
			HTTP:          config.HTTP{BasePath: apiBase, RequestTimeout: 10 * time.Second, ReadinessTimeout: time.Second, MaxBodyBytes: 4096},
			Log:           log,
			Metrics:       observability.NewMetrics(),
			API:           api,
			Authenticator: authSvc,
		})},
		store: store, events: ev, index: index, tokens: tokens, cacheDown: cacheDown, cleared: cleared,
	}
}

func searcher(ix *search.Index, msgs *messages.Service, store *postgres.Store, repo *scylla.Messages) *search.Searcher {
	if ix == nil {
		return nil
	}
	return search.NewSearcher(ix, searchAccess{msgs, store}, repo)
}

func (e *chatEnv) uid(t *testing.T, s session) string {
	t.Helper()
	return e.do(t, request{method: http.MethodGet, path: "/me", token: s.access}).json()["id"].(string)
}

func (e *chatEnv) create(t *testing.T, s session, other string, initial string) reply {
	t.Helper()
	body := map[string]any{"user_id": other}
	if initial != "" {
		body["initial_message"] = initial
	}
	return e.do(t, request{method: http.MethodPost, path: "/chats", token: s.access, body: body})
}

func items(t *testing.T, r reply) []map[string]any {
	t.Helper()
	if r.Code != http.StatusOK {
		t.Fatalf("list: %d %s", r.Code, r.Body)
	}
	var out []map[string]any
	for _, it := range r.json()["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func TestDirectChatsListPinsAndReads(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	bobID, carolID := e.uid(t, bob), e.uid(t, carol)

	r := e.create(t, alice, bobID, "Hello Bob")
	if r.Code != http.StatusCreated || r.json()["type"] != "direct" || r.json()["name"] != "Display bob" {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	chatBob := r.json()["id"].(string)
	last := r.json()["last_message"].(map[string]any)
	if last["content"] != "Hello Bob" {
		t.Fatalf("initial message: %v", last)
	}
	if again := e.create(t, alice, bobID, "again"); again.Code != http.StatusOK || again.json()["id"] != chatBob {
		t.Fatalf("existing chat: %d %s", again.Code, again.Body)
	}
	cb, _ := strconv.ParseInt(chatBob, 10, 64)
	if len(e.events.created[cb]) != 2 {
		t.Fatalf("chat_created went to %v", e.events.created[cb])
	}
	if r := e.create(t, alice, "999999", ""); r.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d", r.Code)
	}

	time.Sleep(5 * time.Millisecond)
	chatCarol := e.create(t, alice, carolID, "Hi Carol").json()["id"].(string)

	// Newest activity first; bob sees one unread message.
	list := items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: alice.access}))
	if len(list) != 2 || list[0]["id"] != chatCarol || list[1]["id"] != chatBob {
		t.Fatalf("order: %v", list)
	}
	bobList := items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: bob.access}))
	if len(bobList) != 1 || bobList[0]["unread_count"].(float64) != 1 || bobList[0]["peer"].(map[string]any)["username"] != "alice" {
		t.Fatalf("bob's list: %v", bobList)
	}

	// Pinned chats come first; pages continue with the cursor.
	if r := e.do(t, request{method: http.MethodPut, path: "/chats/" + chatBob + "/pin", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("pin: %d %s", r.Code, r.Body)
	}
	page := e.do(t, request{method: http.MethodGet, path: "/chats?limit=1", token: alice.access})
	first := items(t, page)
	if len(first) != 1 || first[0]["id"] != chatBob || first[0]["is_pinned"] != true || page.json()["next_cursor"] == nil {
		t.Fatalf("first page: %s", page.Body)
	}
	page2 := e.do(t, request{method: http.MethodGet, path: "/chats?limit=1&after=" + page.json()["next_cursor"].(string), token: alice.access})
	if second := items(t, page2); len(second) != 1 || second[0]["id"] != chatCarol || page2.json()["next_cursor"] != nil {
		t.Fatalf("second page: %s", page2.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/chats/" + chatBob + "/pin", token: carol.access}); r.Code != http.StatusNotFound {
		t.Fatalf("pin someone else's chat: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/chats/" + chatBob + "/pin", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("unpin: %d", r.Code)
	}
	if len(e.events.updated[cb]) != 2 {
		t.Fatalf("chat_list_update for pin and unpin: %v", e.events.updated[cb])
	}

	// Bob reads: his count drops and alice sees the receipt on her last message.
	msgID := last["id"].(string)
	read := e.do(t, request{method: http.MethodPost, path: "/chats/" + chatBob + "/read", token: bob.access, body: map[string]any{"message_id": msgID}})
	if read.Code != http.StatusOK || read.json()["unread_count"].(float64) != 0 {
		t.Fatalf("read: %d %s", read.Code, read.Body)
	}
	got := e.do(t, request{method: http.MethodGet, path: "/chats/" + chatBob, token: alice.access}).json()
	receipts := got["last_message"].(map[string]any)["read_by"].([]any)
	if len(receipts) != 1 || receipts[0].(map[string]any)["user_id"] != bobID {
		t.Fatalf("read receipts: %v", got["last_message"])
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/chats/" + chatBob, token: carol.access}); r.Code != http.StatusNotFound {
		t.Fatalf("outsider: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/chats/abc", token: carol.access}); r.Code != http.StatusBadRequest {
		t.Fatalf("malformed id: %d", r.Code)
	}

	// Deleting a direct chat removes it for both.
	cc, _ := strconv.ParseInt(chatCarol, 10, 64)
	if r := e.do(t, request{method: http.MethodDelete, path: "/chats/" + chatCarol, token: carol.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/chats/" + chatCarol, token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("deleted chat: %d", r.Code)
	}
	if len(e.events.removed[cc]) != 2 {
		t.Fatalf("chat_deleted went to %v", e.events.removed[cc])
	}

	// Once bob blocks alice, she cannot reopen their chat.
	e.do(t, request{method: http.MethodPut, path: "/me/blocked-users/" + e.uid(t, alice), token: bob.access})
	if r := e.create(t, alice, bobID, ""); r.Code != http.StatusForbidden || r.code() != "blocked_by_user" {
		t.Fatalf("blocked: %d %s", r.Code, r.Body)
	}
}

func TestChatUnreadAnchor(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	r := e.create(t, alice, e.uid(t, bob), "first")
	chat := r.json()["id"].(string)
	firstID := r.json()["last_message"].(map[string]any)["id"].(string)
	view := func(s session) map[string]any {
		t.Helper()
		got := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat, token: s.access}).json()
		list := items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: s.access}))
		if len(list) != 1 || list[0]["first_unread_message_id"] != got["first_unread_message_id"] || list[0]["last_read_message_id"] != got["last_read_message_id"] {
			t.Fatalf("list and detail differ: %v %v", list, got)
		}
		return got
	}
	read := func(s session, id string) {
		t.Helper()
		if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/read", token: s.access, body: map[string]any{"message_id": id}}); r.Code != http.StatusOK {
			t.Fatalf("read: %d %s", r.Code, r.Body)
		}
	}

	if v := view(bob); v["unread_count"].(float64) != 1 || v["first_unread_message_id"] != firstID || v["last_read_message_id"] != nil {
		t.Fatalf("never read: %v", v)
	}
	if v, ok := view(alice)["first_unread_message_id"]; !ok || v != nil {
		t.Fatalf("the sender has nothing unread, the field is null: %v", v)
	}

	read(bob, firstID)
	if v := view(bob); v["unread_count"].(float64) != 0 || v["first_unread_message_id"] != nil || v["last_read_message_id"] != firstID {
		t.Fatalf("all read: %v", v)
	}

	var sent []string
	for i := range 15 {
		r := e.send(t, alice, chat, "c-"+strconv.Itoa(i), "message "+strconv.Itoa(i))
		if r.Code != http.StatusCreated {
			t.Fatalf("send: %d %s", r.Code, r.Body)
		}
		sent = append(sent, r.json()["id"].(string))
	}
	e.send(t, bob, chat, "c-bob", "own reply")
	if v := view(bob); v["unread_count"].(float64) != 15 || v["first_unread_message_id"] != sent[0] || v["last_read_message_id"] != firstID {
		t.Fatalf("more unread than one page: %v", v)
	}

	read(bob, sent[11])
	if v := view(bob); v["unread_count"].(float64) != 3 || v["first_unread_message_id"] != sent[12] || v["last_read_message_id"] != sent[11] {
		t.Fatalf("partly read: %v", v)
	}
}

func TestNotificationReadTokenMarksOnlyItsChatRead(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	aliceID, bobID := e.uid(t, alice), e.uid(t, bob)
	r := e.create(t, alice, bobID, "Hello Bob")
	chat := r.json()["id"].(string)
	msg := r.json()["last_message"].(map[string]any)["id"].(string)
	num := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	unread := func() float64 {
		return e.do(t, request{method: http.MethodGet, path: "/chats/" + chat, token: bob.access}).json()["unread_count"].(float64)
	}
	if unread() != 1 {
		t.Fatal("the message starts unread")
	}
	device := pushDevice(t, e.store, num(bobID))
	good := e.tokens.Read(num(bobID), device.ID, device.SessionID, num(chat), num(msg))
	for _, bad := range []string{"nope", e.tokens.Avatar(num(bobID), device.ID, device.SessionID, num(aliceID)), strings.Replace(good, "."+chat+".", ".1"+chat+".", 1),
		e.tokens.Read(num(bobID), uuid.New(), device.SessionID, num(chat), num(msg)), e.tokens.Read(num(bobID), device.ID, uuid.New(), num(chat), num(msg))} {
		if r := e.do(t, request{method: http.MethodPost, path: "/notifications/read/" + bad}); r.Code != http.StatusNotFound {
			t.Fatalf("token %q: %d %s", bad, r.Code, r.Body)
		}
	}
	if unread() != 1 {
		t.Fatal("a rejected token changed the read state")
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/notifications/read/" + good}); r.Code != http.StatusNoContent {
		t.Fatalf("read with token: %d %s", r.Code, r.Body)
	}
	if unread() != 0 {
		t.Fatal("the token did not mark the chat read")
	}
	if err := e.store.Sessions().Revoke(context.Background(), num(bobID), device.SessionID); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/notifications/read/" + good}); r.Code != http.StatusNotFound {
		t.Fatalf("token after logout: %d", r.Code)
	}
	if r := e.login(t, "bob"); r.Code != http.StatusOK {
		t.Fatalf("login again: %d", r.Code)
	}
	again := pushDevice(t, e.store, num(bobID), device)
	if again.ID != device.ID || again.SessionID == device.SessionID {
		t.Fatalf("the browser did not move to the new session: %v", again)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/notifications/read/" + good}); r.Code != http.StatusNotFound {
		t.Fatalf("an old token came back with the new session: %d", r.Code)
	}
}

func pushDevice(t *testing.T, store *postgres.Store, uid int64, existing ...postgres.NotificationDevice) postgres.NotificationDevice {
	t.Helper()
	sessions, err := store.Sessions().ListActive(context.Background(), uid)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("sessions: %v", err)
	}
	id := uuid.New()
	d := postgres.NotificationDevice{ID: id, Platform: "web", Destination: "https://fcm.googleapis.com/fcm/send/" + id.String(), P256dh: "key", Auth: "auth"}
	if len(existing) > 0 {
		d = existing[0]
	}
	if err := store.Notifications().Register(context.Background(), uid, sessions[0].ID, d); err != nil {
		t.Fatal(err)
	}
	d.SessionID = sessions[0].ID
	return d
}

func TestDirectMessageAndSearchExceptions(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol, dave := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol"), e.register(t, "dave")
	aliceID, bobID, carolID := e.uid(t, alice), e.uid(t, bob), e.uid(t, carol)
	setList := func(key, effect string, ids ...string) {
		t.Helper()
		r := e.do(t, request{method: http.MethodPut, path: "/me/privacy/exceptions/" + key + "/" + effect, token: alice.access, body: map[string]any{"user_ids": ids}})
		if r.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", key, effect, r.Code, r.Body)
		}
	}
	e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: map[string]any{"direct_messages": "wait_approval", "search_visibility": "nobody"}})
	setList("direct_messages", "allow", bobID)
	setList("direct_messages", "deny", carolID)
	setList("search_visibility", "allow", bobID)

	if r := e.create(t, bob, aliceID, ""); r.Code != http.StatusCreated {
		t.Fatalf("always allowed skips the approval: %d %s", r.Code, r.Body)
	}
	if r := e.create(t, carol, aliceID, ""); r.Code != http.StatusForbidden {
		t.Fatalf("never allowed may not even ask: %d %s", r.Code, r.Body)
	}
	if r := e.create(t, dave, aliceID, ""); r.Code != http.StatusAccepted {
		t.Fatalf("everyone else still asks: %d %s", r.Code, r.Body)
	}

	found := func(s session) bool {
		t.Helper()
		for _, u := range items(t, e.do(t, request{method: http.MethodGet, path: "/users?q=alice", token: s.access})) {
			if u["id"] == aliceID {
				return true
			}
		}
		return false
	}
	if !found(bob) || found(dave) {
		t.Fatalf("search: bob %v, dave %v", found(bob), found(dave))
	}
	e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: alice.access, body: map[string]any{"search_visibility": "everyone"}})
	setList("search_visibility", "deny", carolID)
	if found(carol) || !found(dave) {
		t.Fatalf("search after deny: carol %v, dave %v", found(carol), found(dave))
	}
}

func TestDirectMessagePrivacyAndRequests(t *testing.T) {
	e := newChatEnv(t)
	alice, dave, erin, frank := e.register(t, "alice"), e.register(t, "dave"), e.register(t, "erin"), e.register(t, "frank")
	for _, s := range []session{dave, erin} {
		e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: s.access, body: map[string]any{"direct_messages": "wait_approval"}})
	}
	e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: frank.access, body: map[string]any{"direct_messages": "shared_chats"}})

	if r := e.create(t, alice, e.uid(t, frank), ""); r.Code != http.StatusForbidden {
		t.Fatalf("shared_chats without a shared chat: %d %s", r.Code, r.Body)
	}

	// Dave's contact name for alice must not reach alice in the answer to her own request.
	e.do(t, request{method: http.MethodPut, path: "/users/" + e.uid(t, alice) + "/contact-name", token: dave.access, body: map[string]any{"contact_name": "Not her"}})
	r := e.create(t, alice, e.uid(t, dave), "May I?")
	if r.Code != http.StatusAccepted || r.json()["status"] != "pending" || r.json()["preview"] != "May I?" {
		t.Fatalf("request: %d %s", r.Code, r.Body)
	}
	if cn := r.json()["requester"].(map[string]any)["contact_name"]; cn != nil {
		t.Fatalf("the recipient's contact name leaked to the requester: %v", cn)
	}
	if again := e.create(t, alice, e.uid(t, dave), "twice"); again.Code != http.StatusAccepted || again.json()["id"] != r.json()["id"] {
		t.Fatalf("second request: %d %s", again.Code, again.Body)
	}
	if len(e.events.requests) != 1 {
		t.Fatalf("approval_request_created sent %d times", len(e.events.requests))
	}
	inbox := items(t, e.do(t, request{method: http.MethodGet, path: "/requests", token: dave.access}))
	if len(inbox) != 1 || inbox[0]["requester"].(map[string]any)["username"] != "alice" {
		t.Fatalf("inbox: %v", inbox)
	}
	reqID := inbox[0]["id"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: "/requests/" + reqID + "/approve", token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("approve someone else's request: %d", r.Code)
	}
	ok := e.do(t, request{method: http.MethodPost, path: "/requests/" + reqID + "/approve", token: dave.access})
	if ok.Code != http.StatusOK || ok.json()["peer"].(map[string]any)["username"] != "alice" {
		t.Fatalf("approve: %d %s", ok.Code, ok.Body)
	}
	if ok.json()["last_message"] == nil || ok.json()["last_message"].(map[string]any)["content"] != "May I?" {
		t.Fatalf("the request's message opens the chat: %v", ok.json()["last_message"])
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/requests/" + reqID + "/approve", token: dave.access}); r.Code != http.StatusConflict {
		t.Fatalf("approve twice: %d", r.Code)
	}
	if left := items(t, e.do(t, request{method: http.MethodGet, path: "/requests", token: dave.access})); len(left) != 0 {
		t.Fatalf("inbox after approval: %v", left)
	}

	e.create(t, alice, e.uid(t, erin), "")
	reqE := items(t, e.do(t, request{method: http.MethodGet, path: "/requests", token: erin.access}))[0]["id"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: "/requests/" + reqE + "/reject", token: erin.access}); r.Code != http.StatusNoContent {
		t.Fatalf("reject: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/requests/" + reqE + "/reject", token: erin.access}); r.Code != http.StatusConflict {
		t.Fatalf("reject twice: %d", r.Code)
	}
	if list := items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: erin.access})); len(list) != 0 {
		t.Fatalf("a rejected request made a chat: %v", list)
	}
}

func TestCrossingRequestsAndBadCursors(t *testing.T) {
	e := newChatEnv(t)
	ann, ben := e.register(t, "ann"), e.register(t, "ben")
	for _, s := range []session{ann, ben} {
		e.do(t, request{method: http.MethodPatch, path: "/me/privacy", token: s.access, body: map[string]any{"direct_messages": "wait_approval"}})
	}
	// Both ask each other; approving one opens the chat and settles the other, with both messages in it.
	if r := e.create(t, ann, e.uid(t, ben), "from ann"); r.Code != http.StatusAccepted {
		t.Fatalf("ann asks: %d %s", r.Code, r.Body)
	}
	if r := e.create(t, ben, e.uid(t, ann), "from ben"); r.Code != http.StatusAccepted {
		t.Fatalf("ben asks: %d %s", r.Code, r.Body)
	}
	req := items(t, e.do(t, request{method: http.MethodGet, path: "/requests", token: ann.access}))[0]["id"].(string)
	chat := e.do(t, request{method: http.MethodPost, path: "/requests/" + req + "/approve", token: ann.access})
	if chat.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", chat.Code, chat.Body)
	}
	if left := items(t, e.do(t, request{method: http.MethodGet, path: "/requests", token: ben.access})); len(left) != 0 {
		t.Fatalf("the crossing request is still pending: %v", left)
	}
	// Each got the other's message: one unread on both sides.
	for _, s := range []session{ann, ben} {
		v := e.do(t, request{method: http.MethodGet, path: "/chats/" + chat.json()["id"].(string), token: s.access}).json()
		if v["unread_count"].(float64) != 1 {
			t.Fatalf("first messages: %v", v)
		}
	}
	// Asking again once the chat exists returns the chat.
	if r := e.create(t, ann, e.uid(t, ben), "again"); r.Code != http.StatusOK || r.json()["id"] != chat.json()["id"] {
		t.Fatalf("after the approval: %d %s", r.Code, r.Body)
	}
	for _, path := range []string{"/chats?after=nonsense", "/requests?after=x"} {
		if r := e.do(t, request{method: http.MethodGet, path: path, token: ann.access}); r.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", path, r.Code)
		}
	}
}

func TestChatListSkipsMessagesDeletedForEveryone(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	chat := e.create(t, alice, e.uid(t, bob), "keep me").json()
	chatID := chat["id"].(string)
	keptID := chat["last_message"].(map[string]any)["id"].(string)

	var ids []string
	for i := range 60 {
		ids = append(ids, e.send(t, alice, chatID, "t-"+strconv.Itoa(i), "gone "+strconv.Itoa(i)).json()["id"].(string))
	}
	deleteForEveryone := func(id string) {
		t.Helper()
		if r := e.do(t, request{method: http.MethodDelete, path: "/messages/" + id + "?scope=everyone", token: alice.access}); r.Code != http.StatusNoContent {
			t.Fatalf("delete for everyone: %d %s", r.Code, r.Body)
		}
	}
	for _, id := range ids {
		deleteForEveryone(id)
	}

	for _, s := range []session{alice, bob} {
		list := items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: s.access}))
		if len(list) != 1 || list[0]["last_message"].(map[string]any)["content"] != "keep me" {
			t.Fatalf("last message in the list: %v", list)
		}
	}
	if got := e.do(t, request{method: http.MethodGet, path: "/chats/" + chatID, token: bob.access}).json(); got["last_message"].(map[string]any)["id"] != keptID {
		t.Fatalf("last message of the chat: %v", got["last_message"])
	}

	deleteForEveryone(keptID)
	if got := e.do(t, request{method: http.MethodGet, path: "/chats/" + chatID, token: alice.access}).json(); got["last_message"] != nil {
		t.Fatalf("a chat with only deleted messages has no last message: %v", got["last_message"])
	}
}

func TestSavedMessagesArchiveAndPinOrder(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	aliceID := e.uid(t, alice)

	saved := e.create(t, alice, aliceID, "")
	if saved.Code != http.StatusCreated || saved.json()["peer"].(map[string]any)["id"] != aliceID || saved.json()["type"] != "direct" {
		t.Fatalf("saved messages: %d %s", saved.Code, saved.Body)
	}
	savedID := saved.json()["id"].(string)
	if again := e.create(t, alice, aliceID, ""); again.Code != http.StatusOK || again.json()["id"] != savedID {
		t.Fatalf("saved messages again: %d %s", again.Code, again.Body)
	}
	sc, _ := strconv.ParseInt(savedID, 10, 64)
	if len(e.events.created[sc]) != 1 {
		t.Fatalf("chat_created went to %v", e.events.created[sc])
	}
	if r := e.send(t, alice, savedID, "note-1", "remember the milk"); r.Code != http.StatusCreated {
		t.Fatalf("send to saved messages: %d %s", r.Code, r.Body)
	}
	got := e.do(t, request{method: http.MethodGet, path: "/chats/" + savedID, token: alice.access}).json()
	if got["unread_count"].(float64) != 0 || got["last_message"].(map[string]any)["content"] != "remember the milk" || got["name"] != "Display alice" {
		t.Fatalf("saved messages view: %v", got)
	}

	chatBob := e.create(t, alice, e.uid(t, bob), "hi").json()["id"].(string)
	chatCarol := e.create(t, alice, e.uid(t, carol), "hi").json()["id"].(string)
	for _, id := range []string{savedID, chatBob, chatCarol} {
		if r := e.do(t, request{method: http.MethodPut, path: "/chats/" + id + "/pin", token: alice.access}); r.Code != http.StatusNoContent {
			t.Fatalf("pin %s: %d %s", id, r.Code, r.Body)
		}
	}
	ids := func(list []map[string]any) []string {
		out := []string{}
		for _, c := range list {
			out = append(out, c["id"].(string))
		}
		return out
	}
	list := items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: alice.access}))
	if got := ids(list); !slices.Equal(got, []string{chatCarol, chatBob, savedID}) || list[0]["pin_position"] == nil {
		t.Fatalf("newest pin on top: %v", got)
	}
	order := map[string]any{"chat_ids": []string{savedID, chatCarol, chatBob}}
	if r := e.do(t, request{method: http.MethodPut, path: "/chats/pins/order", token: alice.access, body: order}); r.Code != http.StatusNoContent {
		t.Fatalf("reorder: %d %s", r.Code, r.Body)
	}
	if got := ids(items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: alice.access}))); !slices.Equal(got, []string{savedID, chatCarol, chatBob}) {
		t.Fatalf("reordered: %v", got)
	}
	bad := map[string]any{"chat_ids": []string{savedID, chatCarol}}
	if r := e.do(t, request{method: http.MethodPut, path: "/chats/pins/order", token: alice.access, body: bad}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("incomplete order: %d %s", r.Code, r.Body)
	}

	if r := e.do(t, request{method: http.MethodPut, path: "/chats/" + chatBob + "/archive", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("archive: %d %s", r.Code, r.Body)
	}
	archived := items(t, e.do(t, request{method: http.MethodGet, path: "/chats?archived=true", token: alice.access}))
	if len(archived) != 1 || archived[0]["id"] != chatBob || archived[0]["is_archived"] != true || archived[0]["is_pinned"] != false || archived[0]["pin_position"] != nil {
		t.Fatalf("archived list: %v", archived)
	}
	if got := ids(items(t, e.do(t, request{method: http.MethodGet, path: "/chats?archived=false", token: alice.access}))); !slices.Equal(got, []string{savedID, chatCarol}) {
		t.Fatalf("main list: %v", got)
	}
	if r := e.send(t, bob, chatBob, "still-archived", "ping"); r.Code != http.StatusCreated {
		t.Fatalf("bob writes: %d %s", r.Code, r.Body)
	}
	if got := e.do(t, request{method: http.MethodGet, path: "/chats/" + chatBob, token: alice.access}).json(); got["is_archived"] != true || got["unread_count"].(float64) != 1 {
		t.Fatalf("a new message keeps the chat archived: %v", got)
	}
	if got := e.do(t, request{method: http.MethodGet, path: "/chats/" + chatBob, token: bob.access}).json(); got["is_archived"] != false {
		t.Fatalf("archive is per user: %v", got)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/chats/" + chatBob + "/archive", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("unarchive: %d %s", r.Code, r.Body)
	}
	if got := items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: alice.access})); len(got) != 3 {
		t.Fatalf("all chats: %v", ids(got))
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/chats/" + chatBob + "/archive", token: carol.access}); r.Code != http.StatusNotFound {
		t.Fatalf("archive someone else's chat: %d", r.Code)
	}
	cb, _ := strconv.ParseInt(chatBob, 10, 64)
	if len(e.events.updated[cb]) < 4 {
		t.Fatalf("chat_list_update for pin, reorder, archive and unarchive: %v", e.events.updated[cb])
	}
}

func TestDeleteForMeAndClearHistory(t *testing.T) {
	e := newChatEnv(t)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	aliceID, _ := strconv.ParseInt(e.uid(t, alice), 10, 64)
	chat := e.create(t, alice, e.uid(t, bob), "").json()["id"].(string)
	chatID, _ := strconv.ParseInt(chat, 10, 64)
	a, err := e.store.Attachments().Create(t.Context(), postgres.NewAttachment{
		UploaderID: &aliceID, Purpose: "message", Kind: "image", Filename: "p.png",
		StorageKey: "attachments/" + uuid.NewString(), MimeType: "image/png", Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/chats/" + chat + "/messages", token: alice.access,
		body: map[string]any{"client_temp_id": "photo", "type": "file", "attachment_id": a.ID.String()}}); r.Code != http.StatusCreated {
		t.Fatalf("photo: %d %s", r.Code, r.Body)
	}
	e.send(t, alice, chat, "old-1", "old from alice")
	e.send(t, bob, chat, "old-2", "old from bob")
	listed := func(s session) map[string]any {
		t.Helper()
		for _, c := range items(t, e.do(t, request{method: http.MethodGet, path: "/chats", token: s.access})) {
			if c["id"] == chat {
				return c
			}
		}
		return nil
	}
	media := func(s session) int {
		t.Helper()
		return len(items(t, e.do(t, request{method: http.MethodGet, path: "/chats/" + chat + "/media?kind=image", token: s.access})))
	}

	if r := e.do(t, request{method: http.MethodDelete, path: "/chats/" + chat + "?scope=nobody", token: alice.access}); r.Code != http.StatusUnprocessableEntity && r.Code != http.StatusBadRequest {
		t.Fatalf("unknown scope: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/chats/" + chat + "?scope=me", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("delete for me: %d %s", r.Code, r.Body)
	}
	if len(e.events.hidden[chatID]) != 1 || e.events.hidden[chatID][0] != aliceID || len(e.events.removed[chatID]) != 0 {
		t.Fatalf("chat_deleted only for alice: hidden %v removed %v", e.events.hidden, e.events.removed)
	}
	if listed(alice) != nil || listed(bob) == nil {
		t.Fatal("the chat leaves only alice's list")
	}
	if got, _ := e.history(t, bob, chat, ""); len(got) != 3 || media(bob) != 1 {
		t.Fatalf("bob keeps the history: %v", got)
	}

	e.send(t, bob, chat, "new-1", "are you there")
	back := listed(alice)
	if back == nil || back["last_message"].(map[string]any)["content"] != "are you there" || back["unread_count"].(float64) != 1 {
		t.Fatalf("the chat comes back with only the new message: %v", back)
	}
	if got, _ := e.history(t, alice, chat, ""); len(got) != 1 || got[0]["content"] != "are you there" {
		t.Fatalf("alice sees only the new message: %v", got)
	}
	if media(alice) != 0 {
		t.Fatal("cleared media stays hidden")
	}

	clear := func(s session, id, scope string) reply {
		return e.do(t, request{method: http.MethodPost, path: "/chats/" + id + "/clear", token: s.access, body: map[string]any{"scope": scope}})
	}
	if r := clear(alice, chat, "me"); r.Code != http.StatusNoContent {
		t.Fatalf("clear for me: %d %s", r.Code, r.Body)
	}
	if got, _ := e.history(t, alice, chat, ""); len(got) != 0 {
		t.Fatalf("alice's history after clearing: %v", got)
	}
	if c := listed(alice); c == nil || c["unread_count"].(float64) != 0 || c["last_message"] != nil {
		t.Fatalf("a cleared chat stays listed, empty and read: %v", c)
	}
	if got, _ := e.history(t, bob, chat, ""); len(got) != 4 {
		t.Fatalf("bob's history after alice cleared hers: %d", len(got))
	}

	e.send(t, bob, chat, "new-2", "last one")
	if r := clear(bob, chat, "everyone"); r.Code != http.StatusNoContent {
		t.Fatalf("clear for everyone: %d %s", r.Code, r.Body)
	}
	for _, s := range []session{alice, bob} {
		if got, _ := e.history(t, s, chat, ""); len(got) != 0 {
			t.Fatalf("history after clearing for everyone: %v", got)
		}
	}
	last := e.events.cleared[len(e.events.cleared)-1]
	if last.chatID != chatID || last.scope != "everyone" || len(last.recipients) != 2 || last.before == 0 {
		t.Fatalf("history_cleared: %+v", last)
	}
	if r := clear(carol, chat, "me"); r.Code != http.StatusNotFound {
		t.Fatalf("a stranger clears: %d", r.Code)
	}
	if r := clear(alice, chat, "all"); r.Code != http.StatusUnprocessableEntity && r.Code != http.StatusBadRequest {
		t.Fatalf("unknown clear scope: %d", r.Code)
	}

	group := e.do(t, request{method: http.MethodPost, path: "/groups", token: alice.access,
		body: map[string]any{"name": "Clearers", "member_ids": []string{e.uid(t, bob), e.uid(t, carol)}}})
	if group.Code != http.StatusCreated {
		t.Fatalf("group: %d %s", group.Code, group.Body)
	}
	gid := group.json()["id"].(string)
	e.send(t, alice, gid, "g-1", "group news")
	if r := clear(bob, gid, "everyone"); r.Code != http.StatusForbidden {
		t.Fatalf("a member clears a group for everyone: %d %s", r.Code, r.Body)
	}
	if r := clear(bob, gid, "me"); r.Code != http.StatusNoContent {
		t.Fatalf("a member clears a group for himself: %d %s", r.Code, r.Body)
	}
	if got, _ := e.history(t, carol, gid, ""); len(got) == 0 {
		t.Fatal("carol keeps the group history")
	}
	if r := clear(alice, gid, "everyone"); r.Code != http.StatusNoContent {
		t.Fatalf("the owner clears a group for everyone: %d %s", r.Code, r.Body)
	}
	if got, _ := e.history(t, carol, gid, ""); len(got) != 0 {
		t.Fatalf("carol's group history after the owner cleared it: %v", got)
	}
	if r := e.do(t, request{method: http.MethodDelete, path: "/chats/" + gid + "?scope=me", token: bob.access}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("delete a group for me: %d", r.Code)
	}
}
