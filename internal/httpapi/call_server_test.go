package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/calls"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/observability"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type quietMedia struct{}

func (quietMedia) CreateRoom(context.Context, string, time.Duration, time.Duration, int) error {
	return nil
}
func (quietMedia) RemoveParticipant(context.Context, string, string) error    { return nil }
func (quietMedia) DeleteRoom(context.Context, string) error                   { return nil }
func (quietMedia) ListParticipants(context.Context, string) ([]string, error) { return nil, nil }

type quietSystem struct{}

func (quietSystem) SystemMessageID() (int64, error) { return 1, nil }

func (quietSystem) SendSystemAs(_ context.Context, chatID, id int64, _ messages.SystemEvent) (scylla.Message, error) {
	return scylla.Message{ChatID: chatID, ID: id}, nil
}

type callEnv struct {
	*authEnv
	svc *calls.Service
}

var testCallLimits = calls.Config{
	PublicURL: "wss://media.example.test", MaxParticipants: 10, MaxActiveParticipants: 100,
	RingTimeout: 30 * time.Second, EmptyGrace: 20 * time.Second, TokenTTL: 10 * time.Minute,
}

func newCallEnv(t *testing.T, enabled bool) *callEnv {
	t.Helper()
	store, rdb := newPostgres(t), newRedis(t)
	log := observability.NewLogger(&bytes.Buffer{}, 0, "test")
	authSvc, err := auth.NewService(store, rdb, auth.Config{
		JWTSecret: bytes.Repeat([]byte("j"), 40), EncryptionKey: bytes.Repeat([]byte("k"), 32),
		RecoveryPepper: bytes.Repeat([]byte("p"), 32), Namespace: "t" + uuid.NewString(),
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	var svc *calls.Service
	if enabled {
		svc = calls.New(testCallLimits, calls.Deps{
			Store: store, Media: quietMedia{}, System: quietSystem{}, Log: log,
			Grants: calls.NewGrants("key", "0123456789abcdef0123456789abcdef", time.Minute),
		})
		t.Cleanup(func() { svc.Wait(context.Background()) })
	}
	api := NewServer(NewAuthServer(AuthOptions{Service: authSvc, Log: log, BasePath: apiBase, CookieSecure: true}), nil).
		WithCalls(NewCallServer(CallOptions{Service: svc, Limits: testCallLimits, Log: log}))
	return &callEnv{
		authEnv: &authEnv{store: store, router: NewRouter(Options{
			HTTP:    config.HTTP{BasePath: apiBase, RequestTimeout: 10 * time.Second, ReadinessTimeout: time.Second, MaxBodyBytes: 1 << 16},
			Log:     log,
			Metrics: observability.NewMetrics(),
			API:     api, Authenticator: authSvc,
		})},
		svc: svc,
	}
}

func publicKey(t *testing.T) string {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
}

func (e *callEnv) userID(t *testing.T, s session) int64 {
	t.Helper()
	r := e.do(t, request{method: http.MethodGet, path: "/me", token: s.access})
	u, err := e.store.Users().GetByUsername(context.Background(), r.json()["username"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func (e *callEnv) withIdentity(t *testing.T, s session) {
	t.Helper()
	if r := e.do(t, request{method: http.MethodPut, path: "/me/call-identity", token: s.access, body: map[string]any{"public_key": publicKey(t)}}); r.Code != http.StatusNoContent {
		t.Fatalf("identity: %d %s", r.Code, r.Body)
	}
}

func TestCallsOverHTTP(t *testing.T) {
	e := newCallEnv(t, true)
	alice, bob, carol := e.register(t, "alice"), e.register(t, "bob"), e.register(t, "carol")
	aliceID, bobID := e.userID(t, alice), e.userID(t, bob)
	chat, _, err := e.store.Chats().CreateDirect(context.Background(), aliceID, bobID)
	if err != nil {
		t.Fatal(err)
	}
	chatPath := "/chats/" + FormatID(chat.ID) + "/call"

	cfg := e.do(t, request{method: http.MethodGet, path: "/calls/config", token: alice.access})
	if cfg.Code != http.StatusOK || cfg.json()["enabled"] != true || cfg.json()["max_participants"].(float64) != 10 {
		t.Fatalf("config: %d %s", cfg.Code, cfg.Body)
	}
	if r := e.do(t, request{method: http.MethodPut, path: "/me/call-identity", token: alice.access, body: map[string]any{"public_key": "short"}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad identity: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: chatPath, token: alice.access}); r.Code != http.StatusNotFound {
		t.Fatalf("no call yet: %d", r.Code)
	}

	start := e.do(t, request{method: http.MethodPost, path: chatPath, token: alice.access, body: map[string]any{"client_request_id": uuid.NewString()}})
	if start.Code != http.StatusCreated || start.json()["state"] != "ringing" {
		t.Fatalf("start: %d %s", start.Code, start.Body)
	}
	callID := start.json()["id"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: chatPath, token: bob.access, body: map[string]any{"client_request_id": uuid.NewString()}}); r.Code != http.StatusOK || r.json()["id"] != callID {
		t.Fatalf("second start: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: chatPath, token: carol.access, body: map[string]any{"client_request_id": uuid.NewString()}}); r.Code != http.StatusNotFound {
		t.Fatalf("outsider starts: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/calls/" + callID, token: carol.access}); r.Code != http.StatusNotFound {
		t.Fatalf("outsider reads: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/calls", token: bob.access}); r.Code != http.StatusOK || len(r.json()["items"].([]any)) != 1 {
		t.Fatalf("bob's calls: %d %s", r.Code, r.Body)
	}

	join := e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/join", token: alice.access})
	if join.Code != http.StatusUnprocessableEntity || join.code() != "call_identity_required" {
		t.Fatalf("join without identity: %d %s", join.Code, join.Body)
	}
	e.withIdentity(t, alice)
	e.withIdentity(t, bob)
	if join = e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/join", token: alice.access, body: ""}); join.Code != http.StatusOK {
		t.Fatalf("alice joins: %d %s", join.Code, join.Body)
	}
	join = e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/join", token: bob.access, body: map[string]any{"takeover": false}})
	if join.Code != http.StatusOK {
		t.Fatalf("bob joins: %d %s", join.Code, join.Body)
	}
	call := join.json()["call"].(map[string]any)
	grant := join.json()["grant"].(map[string]any)
	if call["state"] != "active" || call["epoch"].(float64) != 2 || grant["url"] != "wss://media.example.test" || grant["token"] == "" {
		t.Fatalf("joined: %s", join.Body)
	}
	var aliceSession, bobSession string
	for _, p := range call["participants"].([]any) {
		pm := p.(map[string]any)
		if pm["user_id"] == FormatID(aliceID) {
			aliceSession = pm["session_id"].(string)
		} else {
			bobSession = pm["session_id"].(string)
		}
	}

	bob2 := e.login(t, "bob")
	bob2Session := session{access: bob2.json()["access_token"].(string)}
	e.withIdentity(t, bob2Session)
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/join", token: bob2Session.access}); r.Code != http.StatusConflict || r.code() != "already_in_call" {
		t.Fatalf("second session: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/grant", token: bob2Session.access}); r.Code != http.StatusConflict {
		t.Fatalf("grant for a session that did not join: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/grant", token: bob.access}); r.Code != http.StatusOK {
		t.Fatalf("renew grant: %d %s", r.Code, r.Body)
	}

	keys := map[string]any{"epoch": 1, "envelopes": []any{map[string]any{"recipient_session_id": bobSession, "enc": "BJCC", "ciphertext": "DDDD"}}}
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/keys", token: alice.access, body: keys}); r.Code != http.StatusConflict || r.code() != "stale_epoch" {
		t.Fatalf("stale epoch: %d %s", r.Code, r.Body)
	}
	keys["epoch"] = 2
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/keys", token: alice.access, body: map[string]any{"epoch": 2, "envelopes": []any{map[string]any{"recipient_session_id": uuid.NewString(), "enc": "BJ", "ciphertext": "DD"}}}}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("recipient off the roster: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/keys", token: alice.access, body: keys}); r.Code != http.StatusNoContent {
		t.Fatalf("keys: %d %s", r.Code, r.Body)
	}
	got := e.do(t, request{method: http.MethodGet, path: "/calls/" + callID + "/keys", token: bob.access})
	items := got.json()["items"].([]any)
	if got.Code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["sender_session_id"] != aliceSession {
		t.Fatalf("bob's keys: %d %s", got.Code, got.Body)
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/calls/" + callID + "/keys", token: bob2Session.access}); r.Code != http.StatusConflict {
		t.Fatalf("keys for a session that did not join: %d", r.Code)
	}

	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/decline", token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("decline an answered call: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/end", token: bob.access}); r.Code != http.StatusNoContent {
		t.Fatalf("end: %d %s", r.Code, r.Body)
	}
	ended := e.do(t, request{method: http.MethodGet, path: "/calls/" + callID, token: alice.access})
	if ended.json()["state"] != "ended" || ended.json()["end_reason"] != "completed" {
		t.Fatalf("ended: %s", ended.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/join", token: alice.access}); r.Code != http.StatusConflict || r.code() != "call_ended" {
		t.Fatalf("join an ended call: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + callID + "/leave", token: alice.access}); r.Code != http.StatusNoContent {
		t.Fatalf("leave an ended call: %d", r.Code)
	}

	off := false
	if _, err := e.store.Social().PatchPrivacy(context.Background(), bobID, postgres.PrivacyPatch{CallsEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, request{method: http.MethodPost, path: chatPath, token: alice.access, body: map[string]any{"client_request_id": uuid.NewString()}}); r.Code != http.StatusForbidden || r.code() != "calls_not_allowed" {
		t.Fatalf("callee has calls off: %d %s", r.Code, r.Body)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/abc/join", token: alice.access}); r.Code != http.StatusBadRequest {
		t.Fatalf("malformed id: %d", r.Code)
	}
}

func TestGroupCallEndNeedsARole(t *testing.T) {
	e := newCallEnv(t, true)
	owner, member, caller := e.register(t, "owner"), e.register(t, "member"), e.register(t, "caller")
	g, err := e.store.Chats().CreateGroup(context.Background(), postgres.NewGroup{OwnerID: e.userID(t, owner), Name: "Team", MemberIDs: []int64{e.userID(t, member), e.userID(t, caller)}})
	if err != nil {
		t.Fatal(err)
	}
	start := e.do(t, request{method: http.MethodPost, path: "/chats/" + FormatID(g.ID) + "/call", token: caller.access, body: map[string]any{"client_request_id": uuid.NewString()}})
	id := start.json()["id"].(string)
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + id + "/end", token: member.access}); r.Code != http.StatusForbidden {
		t.Fatalf("member ends: %d", r.Code)
	}
	if r := e.do(t, request{method: http.MethodPost, path: "/calls/" + id + "/end", token: owner.access}); r.Code != http.StatusNoContent {
		t.Fatalf("owner ends: %d", r.Code)
	}
}

func TestCallsSwitchedOff(t *testing.T) {
	e := newCallEnv(t, false)
	alice := e.register(t, "alice")
	cfg := e.do(t, request{method: http.MethodGet, path: "/calls/config", token: alice.access})
	if cfg.Code != http.StatusOK || cfg.json()["enabled"] != false {
		t.Fatalf("config: %d %s", cfg.Code, cfg.Body)
	}
	for _, q := range []request{
		{method: http.MethodGet, path: "/calls"},
		{method: http.MethodPost, path: "/chats/1/call", body: map[string]any{"client_request_id": uuid.NewString()}},
		{method: http.MethodPost, path: "/calls/1/join"},
		{method: http.MethodPut, path: "/me/call-identity", body: map[string]any{"public_key": publicKey(t)}},
	} {
		q.token = alice.access
		if r := e.do(t, q); r.Code != http.StatusServiceUnavailable || r.code() != "calls_unavailable" {
			t.Errorf("%s %s: %d %s", q.method, q.path, r.Code, r.Body)
		}
	}
	if r := e.do(t, request{method: http.MethodGet, path: "/calls/config"}); r.Code != http.StatusUnauthorized {
		t.Fatalf("config without a token: %d", r.Code)
	}
}
