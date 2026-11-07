package calls

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/notifications"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/testenv"
)

type keyEvent struct {
	recipient int64
	session   uuid.UUID
	envs      []postgres.CallKeyEnvelope
}

type recorder struct {
	mu      sync.Mutex
	updates []postgres.Call
	keys    []keyEvent
	system  []messages.SystemEvent
	pushes  []notifications.CallPayload
	ttls    []time.Duration
	created []string
	removed []string
	deleted []string
	present map[string][]string
	failMed bool
	sysIDs  []int64
	nextID  int64
	failSys bool
}

func (r *recorder) CallUpdated(_ context.Context, c postgres.Call, _ []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, c)
}

func (r *recorder) CallKeys(_ context.Context, _ postgres.Call, recipient int64, session uuid.UUID, envs []postgres.CallKeyEnvelope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, keyEvent{recipient, session, envs})
}

func (r *recorder) SystemMessageID() (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	return r.nextID, nil
}

func (r *recorder) SendSystemAs(_ context.Context, chatID, id int64, e messages.SystemEvent) (scylla.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failSys {
		return scylla.Message{}, errors.New("message store down")
	}
	r.system = append(r.system, e)
	r.sysIDs = append(r.sysIDs, id)
	return scylla.Message{ChatID: chatID, ID: id}, nil
}

func (r *recorder) SendCall(_ context.Context, _ postgres.NotificationDevice, p notifications.CallPayload, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushes = append(r.pushes, p)
	r.ttls = append(r.ttls, ttl)
	return nil
}

func (r *recorder) CreateRoom(_ context.Context, room string, _, _ time.Duration, _ int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failMed {
		return errors.New("media down")
	}
	r.created = append(r.created, room)
	return nil
}

func (r *recorder) RemoveParticipant(_ context.Context, _, identity string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, identity)
	return nil
}

func (r *recorder) DeleteRoom(_ context.Context, room string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, room)
	return nil
}

func (r *recorder) ListParticipants(_ context.Context, room string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.present[room], nil
}

func (r *recorder) snapshot() recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	return recorder{
		updates: append([]postgres.Call(nil), r.updates...), keys: append([]keyEvent(nil), r.keys...),
		system: append([]messages.SystemEvent(nil), r.system...), pushes: append([]notifications.CallPayload(nil), r.pushes...),
		ttls: append([]time.Duration(nil), r.ttls...), created: append([]string(nil), r.created...),
		removed: append([]string(nil), r.removed...), deleted: append([]string(nil), r.deleted...),
		sysIDs: append([]int64(nil), r.sysIDs...),
	}
}

type env struct {
	t     *testing.T
	pg    *postgres.Store
	svc   *Service
	rec   *recorder
	mu    sync.Mutex
	clock time.Time
}

func newEnv(t *testing.T, mutate func(*Config)) *env {
	t.Helper()
	pg := testenv.Postgres(t)
	e := &env{t: t, pg: pg, rec: &recorder{present: map[string][]string{}}, clock: time.Now()}
	cfg := Config{PublicURL: "wss://media.example.test", MaxParticipants: 3, MaxActiveParticipants: 100,
		RingTimeout: 30 * time.Second, EmptyGrace: 20 * time.Second, TokenTTL: 10 * time.Minute}
	if mutate != nil {
		mutate(&cfg)
	}
	members := func(ctx context.Context, chatID int64) ([]int64, error) {
		ps, err := pg.Chats().Participants(ctx, chatID)
		ids := make([]int64, len(ps))
		for i, p := range ps {
			ids[i] = p.UserID
		}
		return ids, err
	}
	e.svc = New(cfg, Deps{
		Store: pg, Members: members, Notifier: e.rec, System: e.rec, Media: e.rec, Push: e.rec,
		Grants: NewGrants(testKey, testSecret, cfg.TokenTTL), Tokens: notifications.NewActionTokens("test-secret"),
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	e.svc.now = func() time.Time {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.clock
	}
	t.Cleanup(func() { e.svc.Wait(context.Background()) })
	return e
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.clock = e.clock.Add(d)
	e.mu.Unlock()
}

func (e *env) settle() { e.svc.Wait(context.Background()) }

func (e *env) user(name string) int64 {
	e.t.Helper()
	u, err := e.pg.Users().Register(context.Background(), name+"_u", "Display "+name, "hash")
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

func identityKey(t *testing.T) string {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
}

func (e *env) session(uid int64) uuid.UUID {
	e.t.Helper()
	s, err := e.pg.Sessions().Create(context.Background(), postgres.NewSession{UserID: uid, RefreshTokenHash: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.SetIdentity(context.Background(), uid, s.ID, identityKey(e.t)); err != nil {
		e.t.Fatal(err)
	}
	return s.ID
}

func (e *env) direct(a, b int64) int64 {
	e.t.Helper()
	c, _, err := e.pg.Chats().CreateDirect(context.Background(), a, b)
	if err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

func (e *env) group(owner int64, members ...int64) int64 {
	e.t.Helper()
	c, err := e.pg.Chats().CreateGroup(context.Background(), postgres.NewGroup{OwnerID: owner, Name: "Team", MemberIDs: members})
	if err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

func (e *env) start(uid, chatID int64) postgres.Call {
	e.t.Helper()
	c, created, err := e.svc.Start(context.Background(), uid, chatID, uuid.New())
	if err != nil || !created {
		e.t.Fatalf("start: %v created=%v", err, created)
	}
	return c
}

func (e *env) join(uid int64, sid uuid.UUID, callID int64) postgres.Call {
	e.t.Helper()
	c, _, err := e.svc.Join(context.Background(), uid, sid, callID, false)
	if err != nil {
		e.t.Fatalf("join %d: %v", uid, err)
	}
	return c
}

func (e *env) registerDevice(uid int64, sid uuid.UUID) {
	e.t.Helper()
	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	d := postgres.NotificationDevice{
		ID: uuid.New(), Platform: "web", Destination: "https://fcm.googleapis.com/fcm/send/" + uuid.NewString(),
		P256dh: base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
	}
	if err := e.pg.Notifications().Register(context.Background(), uid, sid, d); err != nil {
		e.t.Fatal(err)
	}
	if err := e.pg.Notifications().SetPreferences(context.Background(), uid, postgres.NotificationPreferences{Enabled: true, Previews: true}); err != nil {
		e.t.Fatal(err)
	}
}

func TestDirectCallLifecycle(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	alice, bob := e.user("alice"), e.user("bob")
	chat := e.direct(alice, bob)
	as, bs := e.session(alice), e.session(bob)
	e.registerDevice(bob, bs)

	req := uuid.New()
	c, created, err := e.svc.Start(ctx, alice, chat, req)
	if err != nil || !created || c.State != postgres.CallRinging || len(c.Participants) != 2 || c.Participants[0].UserID != alice {
		t.Fatalf("start: %+v %v %v", c, created, err)
	}
	again, created, err := e.svc.Start(ctx, alice, chat, req)
	if err != nil || !created || again.ID != c.ID {
		t.Fatalf("retried start: %v %v", created, err)
	}
	other, created, err := e.svc.Start(ctx, bob, chat, uuid.New())
	if err != nil || created || other.ID != c.ID {
		t.Fatalf("second start returns the running call: %v %v", created, err)
	}
	if _, _, err := e.svc.Start(ctx, alice, e.direct(alice, e.user("carol")), req); !errors.Is(err, ErrInvalid) {
		t.Fatalf("request id reused for another chat: %v", err)
	}
	e.settle()
	rec := e.rec.snapshot()
	if len(rec.pushes) != 1 || rec.pushes[0].Action != actionRing || rec.pushes[0].Title != "Display alice" || !rec.pushes[0].Preview ||
		rec.pushes[0].CallID != strconv.FormatInt(c.ID, 10) || rec.pushes[0].AvatarToken == "" || rec.ttls[0] <= 0 || rec.ttls[0] > 30*time.Second {
		t.Fatalf("ring push: %+v %v", rec.pushes, rec.ttls)
	}

	if _, _, err := e.svc.Join(ctx, alice, uuid.New(), c.ID, false); !errors.Is(err, ErrIdentityRequired) {
		t.Fatalf("join without identity: %v", err)
	}
	c = e.join(alice, as, c.ID)
	if c.State != postgres.CallRinging || c.Epoch != 1 {
		t.Fatalf("caller joined: %+v", c)
	}
	_, grant, err := e.svc.Join(ctx, bob, bs, c.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	c, _ = e.svc.Get(ctx, bob, c.ID)
	if c.State != postgres.CallActive || c.Epoch != 2 || c.StartedAt == nil || grant.Identity != Identity(bob, bs) || grant.URL != "wss://media.example.test" {
		t.Fatalf("answered: %+v %+v", c, grant)
	}
	claims := parseClaims(t, grant.Token, testSecret)
	if claims["sub"] != Identity(bob, bs) || claims["video"].(map[string]any)["room"] != RoomName(c.ID) {
		t.Fatalf("grant: %v", claims)
	}
	if g, err := e.svc.Grant(ctx, bob, bs, c.ID); err != nil || g.Identity != grant.Identity {
		t.Fatalf("renewed grant: %v", err)
	}
	if _, err := e.svc.Grant(ctx, bob, uuid.New(), c.ID); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("grant for another session: %v", err)
	}

	env := []OutgoingEnvelope{{RecipientSessionID: bs, Enc: "BJCC", Ciphertext: "DDDD"}}
	if err := e.svc.SendKeys(ctx, alice, as, c.ID, 1, env); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("old epoch: %v", err)
	}
	if err := e.svc.SendKeys(ctx, alice, as, c.ID, 2, []OutgoingEnvelope{{RecipientSessionID: uuid.New(), Enc: "BJ", Ciphertext: "DD"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("recipient off the roster: %v", err)
	}
	if err := e.svc.SendKeys(ctx, alice, uuid.New(), c.ID, 2, env); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("sender session not joined: %v", err)
	}
	if err := e.svc.SendKeys(ctx, alice, as, c.ID, 2, []OutgoingEnvelope{{RecipientSessionID: as, Enc: "BJ", Ciphertext: "DD"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("envelope to oneself: %v", err)
	}
	if err := e.svc.SendKeys(ctx, alice, as, c.ID, 2, []OutgoingEnvelope{{RecipientSessionID: bs, Enc: "not base64!", Ciphertext: "DD"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad encoding: %v", err)
	}
	if err := e.svc.SendKeys(ctx, alice, as, c.ID, 2, env); err != nil {
		t.Fatal(err)
	}
	_, got, err := e.svc.Keys(ctx, bob, bs, c.ID)
	if err != nil || len(got) != 1 || got[0].SenderSessionID != as || got[0].Enc != "BJCC" {
		t.Fatalf("bob's envelopes: %+v %v", got, err)
	}
	if _, mine, _ := e.svc.Keys(ctx, alice, as, c.ID); len(mine) != 0 {
		t.Fatal("envelopes went to the wrong session")
	}
	rec = e.rec.snapshot()
	if len(rec.keys) != 1 || rec.keys[0].recipient != bob || rec.keys[0].session != bs {
		t.Fatalf("call_keys: %+v", rec.keys)
	}

	e.advance(65 * time.Second)
	if err := e.svc.Leave(ctx, bob, c.ID); err != nil {
		t.Fatal(err)
	}
	e.settle()
	c, _ = e.svc.Get(ctx, alice, c.ID)
	if c.State != postgres.CallEnded || c.EndReason != reasonCompleted || joinedCount(&c) != 0 {
		t.Fatalf("hung up: %+v", c)
	}
	if _, got, _ := e.svc.Keys(ctx, bob, bs, c.ID); got != nil {
		t.Fatal("envelopes outlived the call")
	}
	rec = e.rec.snapshot()
	if len(rec.system) != 1 || rec.system[0].Kind != messages.SystemCall || rec.system[0].Call.Outcome != reasonCompleted ||
		rec.system[0].Call.DurationSeconds == nil || *rec.system[0].Call.DurationSeconds != 65 || *rec.system[0].ActorID != alice {
		t.Fatalf("system message: %+v", rec.system)
	}
	if len(rec.deleted) != 1 || len(rec.created) == 0 {
		t.Fatalf("media: created %v deleted %v", rec.created, rec.deleted)
	}
	versions := map[int64]bool{}
	for _, u := range rec.updates {
		if versions[u.Version] {
			t.Fatalf("version %d published twice", u.Version)
		}
		versions[u.Version] = true
	}
	if _, _, err := e.svc.Join(ctx, alice, as, c.ID, false); !errors.Is(err, ErrEnded) {
		t.Fatalf("join an ended call: %v", err)
	}
	e.svc.Reconcile(ctx)
	e.settle()
	if n := len(e.rec.snapshot().system); n != 1 {
		t.Fatalf("system messages after reconcile: %d", n)
	}
}

func TestPrivacyAndBlocks(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	alice, bob, carol := e.user("alice"), e.user("bob"), e.user("carol")
	chat := e.direct(alice, bob)
	off := false
	if _, err := e.pg.Social().PatchPrivacy(ctx, bob, postgres.PrivacyPatch{CallsEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Start(ctx, alice, chat, uuid.New()); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("callee has calls off: %v", err)
	}
	if _, _, err := e.svc.Start(ctx, bob, chat, uuid.New()); err != nil {
		t.Fatalf("a user with calls off may still call: %v", err)
	}
	if _, _, err := e.svc.Start(ctx, carol, chat, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider: %v", err)
	}

	chat2 := e.direct(alice, carol)
	if err := e.pg.Social().Block(ctx, carol, alice); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Start(ctx, alice, chat2, uuid.New()); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("blocked: %v", err)
	}
	if err := e.pg.Social().Unblock(ctx, carol, alice); err != nil {
		t.Fatal(err)
	}
	c := e.start(alice, chat2)
	cs := e.session(carol)
	e.join(carol, cs, c.ID)
	if err := e.pg.Social().Block(ctx, alice, carol); err != nil {
		t.Fatal(err)
	}
	e.svc.Blocked(ctx, alice, carol)
	if c, _ := e.svc.Get(ctx, alice, c.ID); c.State != postgres.CallEnded {
		t.Fatalf("a block ends the direct call: %+v", c)
	}
}

func TestGroupCallRules(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	owner, mod, m1, m2, quiet, outsider := e.user("owner"), e.user("mod"), e.user("m1"), e.user("m2"), e.user("quiet"), e.user("outsider")
	chat := e.group(owner, mod, m1, m2, quiet)
	if err := e.pg.Chats().SetRole(ctx, chat, mod, postgres.RoleModerator); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := e.pg.Social().PatchPrivacy(ctx, quiet, postgres.PrivacyPatch{CallsEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	c := e.start(m1, chat)
	if find(&c, quiet) != nil || find(&c, owner).State != postgres.CallParticipantRinging || len(c.Participants) != 4 {
		t.Fatalf("who rings: %+v", c.Participants)
	}
	if _, err := e.svc.Get(ctx, outsider, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider sees the call: %v", err)
	}
	if _, err := e.svc.ChatCall(ctx, owner, chat); err != nil {
		t.Fatal(err)
	}
	e.join(m1, e.session(m1), c.ID)
	e.join(quiet, e.session(quiet), c.ID)
	e.join(m2, e.session(m2), c.ID)
	if _, _, err := e.svc.Join(ctx, owner, e.session(owner), c.ID, false); !errors.Is(err, ErrFull) {
		t.Fatalf("fourth participant: %v", err)
	}
	if err := e.svc.End(ctx, m2, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member ends for everyone: %v", err)
	}
	mine, err := e.svc.Mine(ctx, owner)
	if err != nil || len(mine) != 1 {
		t.Fatalf("ringing calls of the owner: %d %v", len(mine), err)
	}
	if err := e.svc.Decline(ctx, owner, c.ID); err != nil {
		t.Fatal(err)
	}
	if mine, _ := e.svc.Mine(ctx, owner); len(mine) != 0 {
		t.Fatal("declined call still listed")
	}
	if err := e.svc.End(ctx, mod, c.ID); err != nil {
		t.Fatalf("moderator ends for everyone: %v", err)
	}
	e.settle()
	if c, _ := e.svc.Get(ctx, owner, c.ID); c.State != postgres.CallEnded || c.EndReason != reasonCompleted || find(&c, mod).State != postgres.CallParticipantMissed {
		t.Fatalf("ended: %+v", c)
	}
	if err := e.svc.End(ctx, mod, c.ID); err != nil {
		t.Fatalf("ending twice: %v", err)
	}
}

func TestRingTimeoutAndEmptyGrace(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b, c3 := e.user("a"), e.user("b"), e.user("c")
	direct := e.start(a, e.direct(a, b))
	group := e.start(a, e.group(a, b, c3))
	e.join(a, e.session(a), group.ID)
	e.join(b, e.session(b), group.ID)
	e.advance(31 * time.Second)
	e.svc.Sweep(ctx)
	e.settle()
	if d, _ := e.svc.Get(ctx, a, direct.ID); d.State != postgres.CallEnded || d.EndReason != reasonMissed {
		t.Fatalf("unanswered direct call: %+v", d)
	}
	g, _ := e.svc.Get(ctx, a, group.ID)
	if g.State != postgres.CallActive || find(&g, c3).State != postgres.CallParticipantMissed {
		t.Fatalf("answered group call: %+v", g)
	}
	if err := e.svc.Leave(ctx, a, group.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Leave(ctx, b, group.ID); err != nil {
		t.Fatal(err)
	}
	e.advance(10 * time.Second)
	e.svc.Sweep(ctx)
	if g, _ := e.svc.Get(ctx, a, group.ID); g.State != postgres.CallActive {
		t.Fatal("ended within the grace")
	}
	e.advance(11 * time.Second)
	e.svc.Sweep(ctx)
	e.settle()
	if g, _ := e.svc.Get(ctx, a, group.ID); g.State != postgres.CallEnded || g.EndReason != reasonCompleted {
		t.Fatalf("empty group call after the grace: %+v", g)
	}
	if rec := e.rec.snapshot(); len(rec.system) != 2 {
		t.Fatalf("system messages: %+v", rec.system)
	}
}

func TestConcurrentStartsMakeOneCall(t *testing.T) {
	e := newEnv(t, nil)
	a, b := e.user("a"), e.user("b")
	chat := e.direct(a, b)
	var wg sync.WaitGroup
	ids := make(chan int64, 8)
	created := make(chan bool, 8)
	for i := range 8 {
		wg.Go(func() {
			uid := a
			if i%2 == 1 {
				uid = b
			}
			c, ok, err := e.svc.Start(context.Background(), uid, chat, uuid.New())
			if err != nil {
				t.Error(err)
				return
			}
			ids <- c.ID
			created <- ok
		})
	}
	wg.Wait()
	close(ids)
	close(created)
	first := int64(0)
	for id := range ids {
		if first == 0 {
			first = id
		}
		if id != first {
			t.Fatal("two calls in one chat")
		}
	}
	n := 0
	for ok := range created {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d starts reported a new call", n)
	}
}

func TestAnswerFromTwoSessionsAtOnce(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b := e.user("a"), e.user("b")
	c := e.start(a, e.direct(a, b))
	s1, s2 := e.session(b), e.session(b)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, s := range []uuid.UUID{s1, s2} {
		wg.Go(func() {
			_, _, err := e.svc.Join(ctx, b, s, c.ID, false)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	ok, refused := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrAlreadyInCall):
			refused++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("joined %d, refused %d", ok, refused)
	}
	cur, _ := e.svc.Get(ctx, b, c.ID)
	held := *find(&cur, b).SessionID
	other := s1
	if held == s1 {
		other = s2
	}
	moved, _, err := e.svc.Join(ctx, b, other, c.ID, true)
	if err != nil || *find(&moved, b).SessionID != other || moved.Epoch != cur.Epoch+1 {
		t.Fatalf("takeover: %v %+v", err, moved)
	}
	e.settle()
	if rec := e.rec.snapshot(); len(rec.removed) == 0 || rec.removed[len(rec.removed)-1] != Identity(b, held) {
		t.Fatalf("old session stays in the room: %v", rec.removed)
	}

	d := e.start(a, e.group(a, b))
	if _, _, err := e.svc.Join(ctx, b, other, d.ID, false); !errors.Is(err, ErrAlreadyInCall) {
		t.Fatalf("joined two calls: %v", err)
	}
	if _, _, err := e.svc.Join(ctx, b, other, d.ID, true); err != nil {
		t.Fatalf("takeover from another call: %v", err)
	}
	if prev, _ := e.svc.Get(ctx, a, c.ID); prev.State != postgres.CallEnded {
		t.Fatalf("the direct call should have ended when b moved: %+v", prev)
	}
}

func TestEndRacesJoin(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b, c3 := e.user("a"), e.user("b"), e.user("c")
	for range 5 {
		call := e.start(a, e.group(a, b, c3))
		sb, sc := e.session(b), e.session(c3)
		var wg sync.WaitGroup
		wg.Go(func() { _ = e.svc.End(ctx, a, call.ID) })
		wg.Go(func() {
			if _, _, err := e.svc.Join(ctx, b, sb, call.ID, false); err != nil && !errors.Is(err, ErrEnded) {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if _, _, err := e.svc.Join(ctx, c3, sc, call.ID, false); err != nil && !errors.Is(err, ErrEnded) {
				t.Error(err)
			}
		})
		wg.Wait()
		got, _ := e.svc.Get(ctx, a, call.ID)
		if got.State != postgres.CallEnded || joinedCount(&got) != 0 {
			t.Fatalf("after end and join: %+v", got)
		}
		if _, err := e.pg.Calls().JoinedCallOf(ctx, b); !errors.Is(err, postgres.ErrNotFound) {
			t.Fatal("b stayed joined")
		}
	}
}

func TestRemovalFromOutside(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b, c3 := e.user("a"), e.user("b"), e.user("c")
	chat := e.group(a, b, c3)
	call := e.start(a, chat)
	sa, sb, sc := e.session(a), e.session(b), e.session(c3)
	e.join(a, sa, call.ID)
	e.join(b, sb, call.ID)
	cur := e.join(c3, sc, call.ID)

	e.svc.SessionsEnded(ctx, []uuid.UUID{sb})
	e.settle()
	got, _ := e.svc.Get(ctx, a, call.ID)
	if find(&got, b).State != postgres.CallParticipantLeft || got.Epoch != cur.Epoch+1 {
		t.Fatalf("revoked session: %+v", got)
	}
	if _, err := e.pg.Calls().Identity(ctx, sb); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatal("identity of the ended session kept")
	}
	if _, err := e.svc.Grant(ctx, b, sb, call.ID); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("grant after removal: %v", err)
	}
	if rec := e.rec.snapshot(); len(rec.removed) == 0 || rec.removed[len(rec.removed)-1] != Identity(b, sb) {
		t.Fatalf("media removal: %v", rec.removed)
	}

	if err := e.pg.Chats().RemoveMember(ctx, chat, c3); err != nil {
		t.Fatal(err)
	}
	e.svc.MembersRemoved(ctx, chat, []int64{c3})
	got, _ = e.svc.Get(ctx, a, call.ID)
	if find(&got, c3).State != postgres.CallParticipantLeft || got.Epoch != cur.Epoch+2 {
		t.Fatalf("removed member: %+v", got)
	}

	if _, err := e.pg.Chats().Delete(ctx, chat); err != nil {
		t.Fatal(err)
	}
	e.svc.Sweep(ctx)
	e.settle()
	if _, err := e.pg.Calls().Get(ctx, call.ID); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("call of a deleted chat kept: %v", err)
	}
	if _, err := e.pg.Calls().JoinedCallOf(ctx, a); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatal("a stays joined to a call of a deleted chat")
	}
}

func TestAdmissionLimit(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxActiveParticipants = 1 })
	ctx := context.Background()
	a, b := e.user("a"), e.user("b")
	call := e.start(a, e.direct(a, b))
	e.join(a, e.session(a), call.ID)
	if _, _, err := e.svc.Join(ctx, b, e.session(b), call.ID, false); !errors.Is(err, ErrBusy) {
		t.Fatalf("over the server limit: %v", err)
	}
}

func TestMediaFailureRefusesTheGrant(t *testing.T) {
	e := newEnv(t, nil)
	a, b := e.user("a"), e.user("b")
	call := e.start(a, e.direct(a, b))
	e.rec.mu.Lock()
	e.rec.failMed = true
	e.rec.mu.Unlock()
	if _, _, err := e.svc.Join(context.Background(), a, e.session(a), call.ID, false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("join without a media room: %v", err)
	}
}

func TestIdentityChangeBumpsTheEpoch(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b := e.user("a"), e.user("b")
	call := e.start(a, e.direct(a, b))
	sa := e.session(a)
	before := e.join(a, sa, call.ID)
	if err := e.svc.SetIdentity(ctx, a, sa, "short"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad key: %v", err)
	}
	notOnCurve := base64.RawURLEncoding.EncodeToString(append([]byte{4}, bytes.Repeat([]byte{1}, 64)...))
	if err := e.svc.SetIdentity(ctx, a, sa, notOnCurve); !errors.Is(err, ErrInvalid) {
		t.Fatalf("point off the curve: %v", err)
	}
	key := identityKey(t)
	if err := e.svc.SetIdentity(ctx, a, sa, key); err != nil {
		t.Fatal(err)
	}
	after, _ := e.svc.Get(ctx, a, call.ID)
	if after.Epoch != before.Epoch+1 || find(&after, a).IdentityKey != key {
		t.Fatalf("identity change: %+v", after)
	}
	if err := e.svc.SetIdentity(ctx, a, sa, key); err != nil {
		t.Fatal(err)
	}
	if same, _ := e.svc.Get(ctx, a, call.ID); same.Epoch != after.Epoch {
		t.Fatal("the same key bumped the epoch")
	}
	if err := e.svc.SetIdentity(ctx, b, sa, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's session: %v", err)
	}
}

func TestRetention(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b := e.user("a"), e.user("b")
	old := e.start(a, e.direct(a, b))
	if err := e.svc.End(ctx, a, old.ID); err != nil {
		t.Fatal(err)
	}
	recent := e.start(a, e.direct(a, b))
	if err := e.svc.End(ctx, a, recent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pg.Pool().Exec(ctx, `UPDATE calls SET ended_at = now() - interval '91 days', created_at = now() - interval '91 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	sid := e.session(a)
	if _, err := e.pg.Sessions().RevokeByID(ctx, sid); err != nil {
		t.Fatal(err)
	}
	purged, err := e.pg.Calls().Purge(ctx, Retention)
	if err != nil || purged.Calls != 1 || purged.Identities != 1 {
		t.Fatalf("purge: %+v %v", purged, err)
	}
	if _, err := e.pg.Calls().Get(ctx, old.ID); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatal("old call kept")
	}
	if _, err := e.pg.Calls().Get(ctx, recent.ID); err != nil {
		t.Fatal("recent call purged")
	}
}

func TestWebhooks(t *testing.T) {
	e := newEnv(t, nil)
	rd, prefix := testenv.Redis(t)
	e.svc.d.Webhooks = redis.NewOnce(rd.Client(), prefix, "calls_webhook")
	ctx := context.Background()
	a, b, c3 := e.user("a"), e.user("b"), e.user("c")
	call := e.start(a, e.group(a, b, c3))
	sa, sb := e.session(a), e.session(b)
	e.join(a, sa, call.ID)
	cur := e.join(b, sb, call.ID)
	room := RoomName(call.ID)

	send := func(id, event, identity string) int {
		body := `{"id":"` + id + `","event":"` + event + `","room":{"name":"` + room + `"}`
		if identity != "" {
			body += `,"participant":{"identity":"` + identity + `"}`
		}
		body += `,"createdAt":"1762516800"}`
		return deliver(t, e.svc, webhookToken(t, testKey, testSecret, []byte(body), time.Now().Add(5*time.Minute)), body)
	}
	if code := send("EV_1", "participant_left", Identity(b, sb)); code != 200 {
		t.Fatalf("participant_left: %d", code)
	}
	got, _ := e.svc.Get(ctx, a, call.ID)
	if find(&got, b).DisconnectedAt == nil || got.Version != cur.Version {
		t.Fatalf("disconnect must be silent: %+v", got)
	}
	if code := send("EV_2", "participant_joined", Identity(b, sb)); code != 200 {
		t.Fatal(code)
	}
	if got, _ := e.svc.Get(ctx, a, call.ID); find(&got, b).DisconnectedAt != nil {
		t.Fatal("reconnect not recorded")
	}
	stranger := Identity(c3, uuid.New())
	send("EV_3", "participant_joined", stranger)
	e.settle()
	if rec := e.rec.snapshot(); len(rec.removed) == 0 || rec.removed[len(rec.removed)-1] != stranger {
		t.Fatalf("stranger not removed from the room: %v", rec.removed)
	}
	send("EV_3b", "participant_joined", "listener")
	e.settle()
	if rec := e.rec.snapshot(); rec.removed[len(rec.removed)-1] != "listener" {
		t.Fatalf("foreign identity not removed from the room: %v", rec.removed)
	}
	send("EV_4", "participant_left", Identity(b, sb))
	if code := send("EV_4", "participant_joined", Identity(b, sb)); code != 200 {
		t.Fatal(code)
	}
	if got, _ := e.svc.Get(ctx, a, call.ID); find(&got, b).DisconnectedAt == nil {
		t.Fatal("a repeated event id was handled again")
	}
	e.advance(21 * time.Second)
	e.svc.Sweep(ctx)
	if got, _ := e.svc.Get(ctx, a, call.ID); find(&got, b).State != postgres.CallParticipantLeft || got.Epoch != cur.Epoch+1 {
		t.Fatalf("lost participant after the grace: %+v", got)
	}
	send("EV_5", "room_finished", "")
	e.settle()
	if got, _ := e.svc.Get(ctx, a, call.ID); got.State != postgres.CallEnded || got.EndReason != reasonFailed {
		t.Fatalf("room finished: %+v", got)
	}

	body := `{"id":"EV_6","event":"room_finished","room":{"name":"` + room + `"}}`
	token := webhookToken(t, testKey, testSecret, []byte(body), time.Now().Add(time.Minute))
	if code := deliver(t, e.svc, token, strings.Replace(body, "EV_6", "EV_7", 1)); code != 401 {
		t.Fatalf("tampered body: %d", code)
	}
	if code := deliver(t, e.svc, webhookToken(t, "other-key", testSecret, []byte(body), time.Now().Add(time.Minute)), body); code != 401 {
		t.Fatalf("wrong key: %d", code)
	}
	if code := deliver(t, e.svc, token, body); code != 200 {
		t.Fatalf("valid: %d", code)
	}
}

func deliver(t *testing.T, s *Service, authorization, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v2/calls/media-webhook", strings.NewReader(body))
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "application/webhook+json")
	rec := httptest.NewRecorder()
	s.WebhookHandler().ServeHTTP(rec, req)
	return rec.Code
}

func (e *env) holdInTx(query string, args ...any) func() {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.pg.Pool().Begin(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		e.t.Fatal(err)
	}
	return func() {
		if err := tx.Commit(ctx); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) joinAsync(uid int64, sid uuid.UUID, callID int64) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, _, err := e.svc.Join(context.Background(), uid, sid, callID, false)
		done <- err
	}()
	return done
}

func TestJoinRechecksMembershipAndIdentityInsideTheTransaction(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b, c3 := e.user("a"), e.user("b"), e.user("c")
	chat := e.group(a, b, c3)
	call := e.start(a, chat)
	sb, sc := e.session(b), e.session(c3)

	commit := e.holdInTx(`DELETE FROM participants WHERE chat_id = $1 AND user_id = $2`, chat, b)
	done := e.joinAsync(b, sb, call.ID)
	time.Sleep(300 * time.Millisecond)
	commit()
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("join of a member removed meanwhile: %v", err)
	}

	commit = e.holdInTx(`DELETE FROM call_identities WHERE session_id = $1`, sc)
	done = e.joinAsync(c3, sc, call.ID)
	time.Sleep(300 * time.Millisecond)
	commit()
	if err := <-done; !errors.Is(err, ErrIdentityRequired) {
		t.Fatalf("join of a session ended meanwhile: %v", err)
	}
	got, _ := e.svc.Get(ctx, a, call.ID)
	if joinedCount(&got) != 0 {
		t.Fatalf("roster: %+v", got.Participants)
	}
}

func TestStartRingsOnlyCurrentMembers(t *testing.T) {
	e := newEnv(t, nil)
	a, b, c3 := e.user("a"), e.user("b"), e.user("c")
	chat := e.group(a, b, c3)
	commit := e.holdInTx(`DELETE FROM participants WHERE chat_id = $1 AND user_id = $2`, chat, c3)
	type result struct {
		c   postgres.Call
		err error
	}
	done := make(chan result, 1)
	go func() {
		c, _, err := e.svc.Start(context.Background(), a, chat, uuid.New())
		done <- result{c, err}
	}()
	time.Sleep(300 * time.Millisecond)
	commit()
	r := <-done
	if r.err != nil || find(&r.c, b) == nil || find(&r.c, c3) != nil {
		t.Fatalf("ring list: %v %+v", r.err, r.c.Participants)
	}
}

func TestStartReplayNeedsMembership(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b, c3 := e.user("a"), e.user("b"), e.user("c")
	chat := e.group(a, b, c3)
	req := uuid.New()
	if _, _, err := e.svc.Start(ctx, b, chat, req); err != nil {
		t.Fatal(err)
	}
	if err := e.pg.Chats().RemoveMember(ctx, chat, b); err != nil {
		t.Fatal(err)
	}
	if c, _, err := e.svc.Start(ctx, b, chat, req); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replay after removal: %v %+v", err, c)
	}
}

func TestDirectCallEndsWhenTheInitiatorLeavesWithoutJoining(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b := e.user("a"), e.user("b")
	call := e.start(a, e.direct(a, b))
	e.join(b, e.session(b), call.ID)
	if err := e.svc.Leave(ctx, a, call.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(ctx, b, call.ID); got.State != postgres.CallEnded || got.EndReason != reasonCompleted || joinedCount(&got) != 0 {
		t.Fatalf("after the initiator left: %+v", got)
	}
}

func TestJoinNeedsTheMediaRoomFirst(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b := e.user("a"), e.user("b")
	call := e.start(a, e.direct(a, b))
	e.rec.mu.Lock()
	e.rec.failMed = true
	e.rec.mu.Unlock()
	if _, _, err := e.svc.Join(ctx, b, e.session(b), call.ID, false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("join without a media room: %v", err)
	}
	if got, _ := e.svc.Get(ctx, a, call.ID); got.State != postgres.CallRinging || joinedCount(&got) != 0 {
		t.Fatalf("joined although the room is missing: %+v", got)
	}
}

func TestSummaryIsPostedOnceAfterACrash(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b := e.user("a"), e.user("b")
	call := e.start(a, e.direct(a, b))
	e.rec.mu.Lock()
	e.rec.failSys = true
	e.rec.mu.Unlock()
	if err := e.svc.End(ctx, a, call.ID); err != nil {
		t.Fatal(err)
	}
	e.settle()
	if n := len(e.rec.snapshot().system); n != 0 {
		t.Fatalf("posted while the message store is down: %d", n)
	}
	e.rec.mu.Lock()
	e.rec.failSys = false
	e.rec.mu.Unlock()

	first, claimed, err := e.pg.Calls().ClaimSummary(ctx, call.ID, 999)
	if err != nil || !claimed || first == 999 {
		t.Fatalf("a retry keeps the message id of the first attempt: %d %v %v", first, claimed, err)
	}
	e.advance(11 * time.Second)
	e.svc.Reconcile(ctx)
	if n := len(e.rec.snapshot().system); n != 0 {
		t.Fatal("posted while another instance holds the claim")
	}
	if _, err := e.pg.Pool().Exec(ctx, `UPDATE calls SET summary_claimed_at = now() - interval '2 minutes' WHERE id = $1`, call.ID); err != nil {
		t.Fatal(err)
	}
	e.svc.Reconcile(ctx)
	e.svc.Reconcile(ctx)
	rec := e.rec.snapshot()
	if len(rec.system) != 1 || rec.sysIDs[0] != first || rec.system[0].Call.Outcome != reasonCancelled {
		t.Fatalf("summary after the claim expired: %+v %v", rec.system, rec.sysIDs)
	}
}

func TestCallsSwitchedOffEndOpenCalls(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b, c3 := e.user("a"), e.user("b"), e.user("c")
	direct := e.start(a, e.direct(a, b))
	group := e.start(a, e.group(a, b, c3))
	e.join(a, e.session(a), group.ID)
	e.join(b, e.session(b), group.ID)
	e.svc.CloseAll(ctx)
	e.settle()
	for _, id := range []int64{direct.ID, group.ID} {
		if got, _ := e.svc.Get(ctx, a, id); got.State != postgres.CallEnded || got.EndReason != reasonFailed || joinedCount(&got) != 0 {
			t.Fatalf("call %d after calls were switched off: %+v", id, got)
		}
	}
	if n := len(e.rec.snapshot().system); n != 2 {
		t.Fatalf("summaries: %d", n)
	}
	if _, err := e.pg.Calls().JoinedCallOf(ctx, a); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatal("a stays joined")
	}
}
