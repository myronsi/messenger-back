package httpapi

import (
	"bytes"
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/chats"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/observability"
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

func (c *chatEvents) RequestCreated(_ context.Context, r chats.RequestView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, r.Request.RecipientID)
}

type chatEnv struct {
	*authEnv
	store  *postgres.Store
	events *chatEvents
}

func newChatEnv(t *testing.T) *chatEnv {
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
	msgs := messages.New(messages.Deps{
		Messages: repo, Store: store, Members: members, Unread: unread, Dedup: redis.NewDedup(rdb, prefix, 0), IDs: gen,
	})
	dir := users.NewDirectory(store, nil, apiBase)
	ev := &chatEvents{created: map[int64][]int64{}, updated: map[int64][]int64{}, removed: map[int64][]int64{},
		groupCreated: map[int64][]int64{}, groupUpdates: map[int64]int{}}
	svc := chats.New(chats.Deps{Store: store, Messages: repo, Unread: unread, Members: members, Sender: msgs, Directory: dir, Notifier: ev})
	api := NewServer(NewAuthServer(AuthOptions{Service: authSvc, Log: log, BasePath: apiBase, CookieSecure: true}), nil).
		WithAccount(NewAccountServer(AccountOptions{Store: store, Directory: dir, Deleter: authSvc, BasePath: apiBase, Log: log})).
		WithChats(NewChatServer(ChatOptions{Service: svc, BasePath: apiBase, Log: log})).
		WithMessages(NewMessageServer(MessageOptions{Service: msgs, Store: store, Directory: dir, BasePath: apiBase, Log: log}))
	return &chatEnv{
		authEnv: &authEnv{router: NewRouter(Options{
			HTTP:          config.HTTP{BasePath: apiBase, RequestTimeout: 10 * time.Second, ReadinessTimeout: time.Second, MaxBodyBytes: 4096},
			Log:           log,
			Metrics:       observability.NewMetrics(),
			API:           api,
			Authenticator: authSvc,
		})},
		store: store, events: ev,
	}
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
	if r := e.create(t, alice, e.uid(t, alice), ""); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("chat with yourself: %d", r.Code)
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
