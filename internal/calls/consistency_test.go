package calls

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestCallUpdatedSurvivesACancelledRequest(t *testing.T) {
	e := newEnv(t, nil)
	a, b := e.user("a"), e.user("b")
	call := e.start(a, e.direct(a, b))
	before := len(e.rec.snapshot().updates)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.svc.after(ctx, call, effects{changed: true, visible: true}, true)
	if n := len(e.rec.snapshot().updates); n != before+1 {
		t.Fatalf("call_updated after the client went away: %d events, want %d", n, before+1)
	}
}

func TestSweepRepairsMissedHooks(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b, c3, d := e.user("a"), e.user("b"), e.user("c"), e.user("d")
	chat := e.group(a, b, c3)
	call := e.start(a, chat)
	sa, sb, sc := e.session(a), e.session(b), e.session(c3)
	e.join(a, sa, call.ID)
	e.join(b, sb, call.ID)
	cur := e.join(c3, sc, call.ID)

	if _, err := e.pg.Sessions().RevokeByID(ctx, sb); err != nil {
		t.Fatal(err)
	}
	if err := e.pg.Chats().RemoveMember(ctx, chat, c3); err != nil {
		t.Fatal(err)
	}
	envs := []OutgoingEnvelope{{RecipientSessionID: sb, Enc: "BJCC", Ciphertext: "DDDD"}, {RecipientSessionID: sc, Enc: "BJCC", Ciphertext: "DDDD"}}
	if err := e.svc.SendKeys(ctx, a, sa, call.ID, cur.Epoch, envs); err != nil {
		t.Fatal(err)
	}
	if rec := e.rec.snapshot(); len(rec.keys) != 0 {
		t.Fatalf("call_keys went to sessions that are no longer valid members: %+v", rec.keys)
	}

	e.svc.Sweep(ctx)
	e.settle()
	got, _ := e.svc.Get(ctx, a, call.ID)
	if find(&got, b).State != postgres.CallParticipantLeft || find(&got, c3).State != postgres.CallParticipantLeft || got.Epoch != cur.Epoch+2 || got.State != postgres.CallActive {
		t.Fatalf("after the sweep: %+v", got)
	}
	removed := map[string]bool{}
	for _, id := range e.rec.snapshot().removed {
		removed[id] = true
	}
	if !removed[Identity(b, sb)] || !removed[Identity(c3, sc)] {
		t.Fatalf("media removals: %v", removed)
	}

	direct := e.start(d, e.direct(d, b))
	e.join(b, e.session(b), direct.ID)
	if err := e.pg.Social().Block(ctx, d, b); err != nil {
		t.Fatal(err)
	}
	e.svc.Sweep(ctx)
	if got, _ := e.svc.Get(ctx, d, direct.ID); got.State != postgres.CallEnded || joinedCount(&got) != 0 {
		t.Fatalf("direct call of a blocked pair: %+v", got)
	}
}

func TestBlockDuringStartIsCaughtBySweep(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b := e.user("a"), e.user("b")
	chat := e.direct(a, b)
	commit := e.holdInTx(`SELECT 1 FROM participants WHERE chat_id = $1 AND user_id = $2 FOR UPDATE`, chat, b)
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
	if err := e.pg.Social().Block(ctx, b, a); err != nil {
		t.Fatal(err)
	}
	e.svc.Blocked(ctx, b, a)
	commit()
	r := <-done
	if r.err != nil {
		t.Fatalf("start that raced the block: %v", r.err)
	}
	if got, _ := e.svc.Get(ctx, a, r.c.ID); got.State == postgres.CallEnded {
		t.Fatal("the hook was expected to miss the call")
	}
	e.svc.Sweep(ctx)
	e.settle()
	if got, _ := e.svc.Get(ctx, a, r.c.ID); got.State != postgres.CallEnded || got.EndReason != reasonCancelled || find(&got, b).State != postgres.CallParticipantMissed {
		t.Fatalf("one sweep after the block: %+v", got)
	}
}

func TestFailedTakeoverKeepsTheOtherCall(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b, o, m1, m2 := e.user("a"), e.user("b"), e.user("o"), e.user("m1"), e.user("m2")
	direct := e.start(a, e.direct(a, b))
	sb := e.session(b)
	e.join(a, e.session(a), direct.ID)
	before := e.join(b, sb, direct.ID)

	group := e.start(o, e.group(o, m1, m2, b))
	e.join(o, e.session(o), group.ID)
	e.join(m1, e.session(m1), group.ID)
	e.join(m2, e.session(m2), group.ID)
	if _, _, err := e.svc.Join(ctx, b, sb, group.ID, true); !errors.Is(err, ErrFull) {
		t.Fatalf("takeover into a full call: %v", err)
	}
	e.settle()
	got, _ := e.svc.Get(ctx, a, direct.ID)
	if got.State != postgres.CallActive || !joinedSession(find(&got, b), sb) || got.Epoch != before.Epoch || got.Version != before.Version {
		t.Fatalf("the failed takeover changed the other call: %+v", got)
	}

	if err := e.svc.Leave(ctx, m2, group.ID); err != nil {
		t.Fatal(err)
	}
	moved, _, err := e.svc.Join(ctx, b, sb, group.ID, true)
	if err != nil || !joinedSession(find(&moved, b), sb) {
		t.Fatalf("takeover once there is room: %v %+v", err, moved)
	}
	if got, _ := e.svc.Get(ctx, a, direct.ID); got.State != postgres.CallEnded {
		t.Fatalf("the direct call ends when b moves: %+v", got)
	}
}

func TestDirectCallEndsWhenTheCallerNeverJoins(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b := e.user("a"), e.user("b")
	call := e.start(a, e.direct(a, b))
	e.join(b, e.session(b), call.ID)
	e.advance(20 * time.Second)
	e.svc.Sweep(ctx)
	if got, _ := e.svc.Get(ctx, b, call.ID); got.State != postgres.CallActive {
		t.Fatalf("ended within the ring window: %+v", got)
	}
	e.advance(11 * time.Second)
	e.svc.Sweep(ctx)
	e.settle()
	if got, _ := e.svc.Get(ctx, b, call.ID); got.State != postgres.CallEnded || got.EndReason != reasonCancelled || joinedCount(&got) != 0 {
		t.Fatalf("callee alone after the caller never joined: %+v", got)
	}
}

func TestRoomDeletionIsRetried(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b := e.user("a"), e.user("b")
	call := e.start(a, e.direct(a, b))
	e.join(a, e.session(a), call.ID)
	e.join(b, e.session(b), call.ID)
	e.rec.mu.Lock()
	e.rec.failDel = true
	e.rec.mu.Unlock()
	if err := e.svc.End(ctx, a, call.ID); err != nil {
		t.Fatal(err)
	}
	e.settle()
	e.advance(11 * time.Second)
	e.svc.Reconcile(ctx)
	if rec := e.rec.snapshot(); len(rec.deleted) != 0 || len(rec.removed) != 0 {
		t.Fatalf("media while the room API fails: deleted %v removed %v", rec.deleted, rec.removed)
	}
	e.rec.mu.Lock()
	e.rec.failDel = false
	e.rec.mu.Unlock()
	e.svc.Reconcile(ctx)
	e.svc.Reconcile(ctx)
	if rec := e.rec.snapshot(); len(rec.deleted) != 1 || rec.deleted[0] != RoomName(call.ID) {
		t.Fatalf("retried room deletion: %v", rec.deleted)
	}
}

func TestSecondStartReturnsTheRunningCall(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	alice, bob := e.user("alice"), e.user("bob")
	chat := e.direct(alice, bob)
	off := false
	if _, err := e.pg.Social().PatchPrivacy(ctx, bob, postgres.PrivacyPatch{CallsEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	first, created, err := e.svc.Start(ctx, bob, chat, uuid.New())
	if err != nil || !created {
		t.Fatalf("bob calls: %v %v", err, created)
	}
	again, created, err := e.svc.Start(ctx, alice, chat, uuid.New())
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("alice starts while bob's call rings: %v %v %+v", err, created, again)
	}
}

func TestIdentityRetryRepairsTheRoster(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b := e.user("a"), e.user("b")
	call := e.start(a, e.direct(a, b))
	sa := e.session(a)
	before := e.join(a, sa, call.ID)
	key := identityKey(t)
	if _, err := e.pg.Calls().SetIdentity(ctx, a, sa, key); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetIdentity(ctx, a, sa, key); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Get(ctx, a, call.ID)
	if got.Epoch != before.Epoch+1 || find(&got, a).IdentityKey != key {
		t.Fatalf("a retry after the roster update failed: %+v", got)
	}
	if err := e.svc.SetIdentity(ctx, a, sa, key); err != nil {
		t.Fatal(err)
	}
	if same, _ := e.svc.Get(ctx, a, call.ID); same.Epoch != got.Epoch {
		t.Fatal("a second retry bumped the epoch again")
	}
}

func TestStartTakesTheChatLockFirst(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, b, c3 := e.user("a"), e.user("b"), e.user("c")
	chat := e.group(a, b, c3)
	tx, err := e.pg.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM chats WHERE id = $1 FOR UPDATE`, chat); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := e.svc.Start(context.Background(), a, chat, uuid.New())
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '500ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM participants WHERE chat_id = $1 FOR UPDATE`, chat); err != nil {
		t.Fatalf("a chat change waits for a start that waits for the chat: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("start after the chat change: %v", err)
	}
}

func TestCancelPushesCarryTheReason(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	a, answered, declined, removed, missed := e.user("a"), e.user("b"), e.user("c"), e.user("d"), e.user("e")
	chat := e.group(a, answered, declined, removed, missed)
	sessions := map[int64]uuid.UUID{}
	for _, uid := range []int64{answered, declined, removed, missed} {
		sessions[uid] = e.session(uid)
		e.registerDevice(uid, sessions[uid])
	}
	call := e.start(a, chat)
	e.join(a, e.session(a), call.ID)
	e.join(answered, sessions[answered], call.ID)
	if err := e.svc.Decline(ctx, declined, call.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.pg.Chats().RemoveMember(ctx, chat, removed); err != nil {
		t.Fatal(err)
	}
	e.svc.MembersRemoved(ctx, chat, []int64{removed})
	e.advance(31 * time.Second)
	e.svc.Sweep(ctx)
	e.settle()
	want := map[string]string{
		strconv.FormatInt(answered, 10): stopAnswered, strconv.FormatInt(declined, 10): stopDeclined,
		strconv.FormatInt(removed, 10): stopEnded, strconv.FormatInt(missed, 10): stopMissed,
	}
	got := map[string]string{}
	for _, p := range e.rec.snapshot().pushes {
		switch p.Action {
		case actionRing:
			if p.Reason != "" {
				t.Fatalf("a ring push carries a reason: %+v", p)
			}
		case actionCancel:
			got[p.UserID] = p.Reason
		}
	}
	if !maps.Equal(got, want) {
		t.Fatalf("cancel reasons: %v, want %v", got, want)
	}
}
