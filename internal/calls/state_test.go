package calls

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

var t0 = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func newCall(kind postgres.ChatType, ring ...int64) postgres.Call {
	c := postgres.Call{
		ID: 7, ChatID: 77, Kind: kind, InitiatorID: 1, State: postgres.CallRinging, Version: 1, MaxParticipants: 3,
		CreatedAt: t0, RingExpiresAt: t0.Add(30 * time.Second),
		Participants: []postgres.CallParticipant{{UserID: 1, State: postgres.CallParticipantLeft}},
	}
	for i, uid := range ring {
		c.Participants = append(c.Participants, postgres.CallParticipant{UserID: uid, Position: i + 1, State: postgres.CallParticipantRinging})
	}
	return c
}

func mustJoin(t *testing.T, c *postgres.Call, uid int64, sid uuid.UUID, now time.Time) effects {
	t.Helper()
	var fx effects
	if err := join(c, joinRequest{userID: uid, session: sid, identityKey: "k", maxParticipants: c.MaxParticipants}, now, &fx); err != nil {
		t.Fatalf("join %d: %v", uid, err)
	}
	return fx
}

func TestDirectCallAnswerAndHangUp(t *testing.T) {
	c := newCall(postgres.ChatDirect, 2)
	a, b := uuid.New(), uuid.New()
	mustJoin(t, &c, 1, a, t0)
	if c.State != postgres.CallRinging || c.Epoch != 1 || c.StartedAt != nil {
		t.Fatalf("initiator joined: %+v", c)
	}
	fx := mustJoin(t, &c, 2, b, t0.Add(5*time.Second))
	if c.State != postgres.CallActive || c.Epoch != 2 || c.StartedAt == nil || len(fx.cancel) != 1 || fx.cancel[0] != 2 {
		t.Fatalf("answered: %+v %+v", c, fx)
	}
	fx = effects{}
	leave(&c, 2, t0.Add(65*time.Second), &fx)
	if c.State != postgres.CallEnded || c.EndReason != reasonCompleted || !fx.ended || !fx.deleteRoom || len(fx.remove) != 2 || joinedCount(&c) != 0 {
		t.Fatalf("hang up: %+v %+v", c, fx)
	}
}

func TestDirectCallEndsWhenTheInitiatorWhoNeverJoinedLeaves(t *testing.T) {
	c := newCall(postgres.ChatDirect, 2)
	b := uuid.New()
	mustJoin(t, &c, 2, b, t0.Add(5*time.Second))
	if c.State != postgres.CallActive {
		t.Fatalf("answered: %+v", c)
	}
	var fx effects
	leave(&c, 1, t0.Add(time.Minute), &fx)
	if c.State != postgres.CallEnded || c.EndReason != reasonCancelled || !fx.ended || len(fx.remove) != 1 || fx.remove[0] != Identity(2, b) {
		t.Fatalf("initiator left: %+v %+v", c, fx)
	}

	c = newCall(postgres.ChatDirect, 2)
	fx = effects{}
	leave(&c, 2, t0, &fx)
	if c.State != postgres.CallEnded || c.EndReason != reasonCancelled {
		t.Fatalf("peer left before anyone joined: %+v", c)
	}

	g := newCall(postgres.ChatGroup, 2, 3)
	mustJoin(t, &g, 2, uuid.New(), t0)
	fx = effects{}
	leave(&g, 3, t0, &fx)
	leave(&g, 1, t0, &fx)
	if g.State != postgres.CallActive || fx.changed {
		t.Fatalf("group call after leaves of members who did not join: %+v", g)
	}
}

func TestEpochRules(t *testing.T) {
	c := newCall(postgres.ChatGroup, 2, 3)
	s1, s2, s3 := uuid.New(), uuid.New(), uuid.New()
	mustJoin(t, &c, 1, s1, t0)
	mustJoin(t, &c, 2, s2, t0)
	if c.Epoch != 2 {
		t.Fatalf("epoch after two joins: %d", c.Epoch)
	}
	fx := mustJoin(t, &c, 2, s2, t0)
	if c.Epoch != 2 || fx.visible {
		t.Fatal("joining again with the same session must change nothing")
	}
	var conflict effects
	if err := join(&c, joinRequest{userID: 2, session: s3, identityKey: "k", maxParticipants: 3}, t0, &conflict); !errors.Is(err, ErrAlreadyInCall) {
		t.Fatalf("second session without takeover: %v", err)
	}
	var take effects
	if err := join(&c, joinRequest{userID: 2, session: s3, identityKey: "k2", takeover: true, maxParticipants: 3}, t0, &take); err != nil {
		t.Fatal(err)
	}
	if c.Epoch != 3 || len(take.remove) != 1 || take.remove[0] != Identity(2, s2) || *find(&c, 2).SessionID != s3 {
		t.Fatalf("takeover: %+v %+v", c, take)
	}
	var gone effects
	removeSessions(&c, map[uuid.UUID]bool{s3: true}, t0, &gone)
	if c.Epoch != 4 || find(&c, 2).State != postgres.CallParticipantLeft || c.State == postgres.CallEnded {
		t.Fatalf("session removal: %+v", c)
	}
}

func TestCallFull(t *testing.T) {
	c := newCall(postgres.ChatGroup, 2, 3, 4)
	c.MaxParticipants = 2
	mustJoin(t, &c, 1, uuid.New(), t0)
	mustJoin(t, &c, 2, uuid.New(), t0)
	var fx effects
	if err := join(&c, joinRequest{userID: 3, session: uuid.New(), identityKey: "k", maxParticipants: 2}, t0, &fx); !errors.Is(err, ErrFull) {
		t.Fatalf("third join: %v", err)
	}
}

func TestDeclineEndsOnlyDirectCalls(t *testing.T) {
	d := newCall(postgres.ChatDirect, 2)
	var fx effects
	decline(&d, 2, t0, &fx)
	if d.State != postgres.CallEnded || d.EndReason != reasonDeclined {
		t.Fatalf("direct decline: %+v", d)
	}
	g := newCall(postgres.ChatGroup, 2, 3)
	fx = effects{}
	decline(&g, 2, t0, &fx)
	if g.State != postgres.CallRinging || find(&g, 2).State != postgres.CallParticipantDeclined || len(fx.cancel) != 1 {
		t.Fatalf("group decline: %+v", g)
	}
	fx = effects{}
	decline(&g, 2, t0, &fx)
	if fx.changed {
		t.Fatal("declining twice changed the call")
	}
}

func TestRingTimeout(t *testing.T) {
	d := newCall(postgres.ChatDirect, 2)
	var fx effects
	sweep(&d, t0.Add(10*time.Second), 20*time.Second, &fx)
	if fx.changed {
		t.Fatal("swept before the ring expired")
	}
	sweep(&d, t0.Add(30*time.Second), 20*time.Second, &fx)
	if d.State != postgres.CallEnded || d.EndReason != reasonMissed || find(&d, 2).State != postgres.CallParticipantMissed {
		t.Fatalf("missed: %+v", d)
	}

	g := newCall(postgres.ChatGroup, 2, 3)
	mustJoin(t, &g, 1, uuid.New(), t0)
	mustJoin(t, &g, 2, uuid.New(), t0)
	fx = effects{}
	sweep(&g, t0.Add(31*time.Second), 20*time.Second, &fx)
	if g.State != postgres.CallActive || find(&g, 3).State != postgres.CallParticipantMissed || len(fx.cancel) != 1 {
		t.Fatalf("answered group call: %+v", g)
	}
}

func TestEmptyGroupCallEndsAfterGrace(t *testing.T) {
	g := newCall(postgres.ChatGroup, 2)
	mustJoin(t, &g, 1, uuid.New(), t0)
	mustJoin(t, &g, 2, uuid.New(), t0)
	var fx effects
	leave(&g, 1, t0.Add(time.Minute), &fx)
	leave(&g, 2, t0.Add(time.Minute), &fx)
	if g.State != postgres.CallActive || g.EmptySince == nil {
		t.Fatalf("empty: %+v", g)
	}
	fx = effects{}
	sweep(&g, t0.Add(time.Minute+10*time.Second), 20*time.Second, &fx)
	if g.State == postgres.CallEnded {
		t.Fatal("ended within the grace")
	}
	sweep(&g, t0.Add(time.Minute+20*time.Second), 20*time.Second, &fx)
	if g.State != postgres.CallEnded || g.EndReason != reasonCompleted {
		t.Fatalf("after the grace: %+v", g)
	}
}

func TestCallerHangsUpBeforeAnswer(t *testing.T) {
	g := newCall(postgres.ChatGroup, 2, 3)
	var fx effects
	leave(&g, 1, t0, &fx)
	if g.State != postgres.CallEnded || g.EndReason != reasonCancelled || len(fx.cancel) != 2 {
		t.Fatalf("cancelled: %+v %+v", g, fx)
	}
}

func TestMediaEvents(t *testing.T) {
	c := newCall(postgres.ChatGroup, 2)
	s1, s2 := uuid.New(), uuid.New()
	mustJoin(t, &c, 1, s1, t0)
	mustJoin(t, &c, 2, s2, t0)
	var fx effects
	mediaJoined(&c, 3, uuid.New(), &fx)
	if len(fx.remove) != 1 || fx.changed {
		t.Fatalf("a stranger in the room is removed: %+v", fx)
	}
	fx = effects{}
	mediaLeft(&c, 2, s2, t0.Add(time.Second), &fx)
	if !fx.changed || fx.visible || find(&c, 2).DisconnectedAt == nil {
		t.Fatalf("left the room: %+v", fx)
	}
	fx = effects{}
	sweep(&c, t0.Add(22*time.Second), 20*time.Second, &fx)
	if find(&c, 2).State != postgres.CallParticipantLeft || c.Epoch != 3 {
		t.Fatalf("lost participant after the grace: %+v", c)
	}
	fx = effects{}
	roomFinished(&c, t0.Add(time.Minute), &fx)
	if c.State != postgres.CallActive || !fx.changed || fx.visible || find(&c, 1).DisconnectedAt == nil {
		t.Fatalf("room finished with someone in it: %+v %+v", c, fx)
	}
	fx = effects{}
	mediaJoined(&c, 1, s1, &fx)
	if find(&c, 1).DisconnectedAt != nil {
		t.Fatal("a rejoin into the recreated room was not recorded")
	}
}

func TestRoomFinishedLeavesEndingToTheServer(t *testing.T) {
	c := newCall(postgres.ChatGroup, 2)
	s1, s2 := uuid.New(), uuid.New()
	mustJoin(t, &c, 1, s1, t0)
	mustJoin(t, &c, 2, s2, t0)
	var fx effects
	mediaLeft(&c, 1, s1, t0.Add(time.Second), &fx)
	fx = effects{}
	roomFinished(&c, t0.Add(10*time.Second), &fx)
	if c.State != postgres.CallActive || *find(&c, 1).DisconnectedAt != t0.Add(time.Second) || *find(&c, 2).DisconnectedAt != t0.Add(10*time.Second) {
		t.Fatalf("room finished: %+v", c)
	}
	fx = effects{}
	sweep(&c, t0.Add(21*time.Second), 20*time.Second, &fx)
	if find(&c, 1).State != postgres.CallParticipantLeft || find(&c, 2).State != postgres.CallParticipantJoined || c.State == postgres.CallEnded {
		t.Fatalf("after the first grace: %+v", c)
	}
	sweep(&c, t0.Add(30*time.Second), 20*time.Second, &fx)
	sweep(&c, t0.Add(50*time.Second), 20*time.Second, &fx)
	if c.State != postgres.CallEnded || c.EndReason != reasonCompleted || joinedCount(&c) != 0 {
		t.Fatalf("room finished after everyone left the media room: %+v", c)
	}
}

func TestCompletedNeedsTwoParticipants(t *testing.T) {
	g := newCall(postgres.ChatGroup, 2, 3)
	mustJoin(t, &g, 2, uuid.New(), t0)
	var fx effects
	endCall(&g, outcome(&g), t0.Add(time.Minute), &fx)
	if g.EndReason != reasonCancelled {
		t.Fatalf("one participant: %s", g.EndReason)
	}
	g = newCall(postgres.ChatGroup, 2, 3)
	mustJoin(t, &g, 2, uuid.New(), t0)
	mustJoin(t, &g, 3, uuid.New(), t0)
	fx = effects{}
	endCall(&g, outcome(&g), t0.Add(time.Minute), &fx)
	if g.EndReason != reasonCompleted {
		t.Fatalf("two participants without the initiator: %s", g.EndReason)
	}
}

func TestDirectCallEndsWhenTheInitiatorNeverJoins(t *testing.T) {
	c := newCall(postgres.ChatDirect, 2)
	mustJoin(t, &c, 2, uuid.New(), t0.Add(2*time.Second))
	var fx effects
	sweep(&c, t0.Add(25*time.Second), 20*time.Second, &fx)
	if c.State != postgres.CallActive {
		t.Fatal("ended within the ring window")
	}
	sweep(&c, t0.Add(30*time.Second), 20*time.Second, &fx)
	if c.State != postgres.CallEnded || c.EndReason != reasonCancelled || joinedCount(&c) != 0 {
		t.Fatalf("callee alone: %+v", c)
	}

	d := newCall(postgres.ChatDirect, 2)
	mustJoin(t, &d, 2, uuid.New(), t0.Add(2*time.Second))
	mustJoin(t, &d, 1, uuid.New(), t0.Add(3*time.Second))
	fx = effects{}
	sweep(&d, t0.Add(time.Minute), 20*time.Second, &fx)
	if d.State != postgres.CallActive || fx.changed {
		t.Fatalf("both joined: %+v", d)
	}
}

func TestReconcileMarksAbsentParticipants(t *testing.T) {
	c := newCall(postgres.ChatGroup, 2)
	s1, s2 := uuid.New(), uuid.New()
	mustJoin(t, &c, 1, s1, t0)
	mustJoin(t, &c, 2, s2, t0)
	var fx effects
	reconcile(&c, map[string]bool{Identity(1, s1): true, "9:" + uuid.NewString(): true}, t0.Add(time.Minute), 20*time.Second, &fx)
	if find(&c, 2).DisconnectedAt == nil || find(&c, 1).DisconnectedAt != nil || len(fx.remove) != 1 {
		t.Fatalf("reconcile: %+v %+v", c, fx)
	}
}

func TestOrphanAndStaleCallsEnd(t *testing.T) {
	c := newCall(postgres.ChatGroup, 2)
	c.ChatID = 0
	var fx effects
	sweep(&c, t0, 20*time.Second, &fx)
	if c.State != postgres.CallEnded {
		t.Fatal("call of a deleted chat kept running")
	}
	s := newCall(postgres.ChatGroup, 2)
	mustJoin(t, &s, 1, uuid.New(), t0)
	mustJoin(t, &s, 2, uuid.New(), t0)
	fx = effects{}
	sweep(&s, t0.Add(25*time.Hour), 20*time.Second, &fx)
	if s.State != postgres.CallEnded || s.EndReason != reasonFailed {
		t.Fatalf("stale call: %+v", s)
	}
}

func TestIdentityParsing(t *testing.T) {
	sid := uuid.New()
	uid, got, ok := parseIdentity(Identity(42, sid))
	if !ok || uid != 42 || got != sid {
		t.Fatal("identity round trip")
	}
	for _, bad := range []string{"", "42", "x:" + sid.String(), "42:nope", "-1:" + sid.String()} {
		if _, _, ok := parseIdentity(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	if id, ok := roomCall(RoomName(9)); !ok || id != 9 {
		t.Fatal("room name round trip")
	}
	if _, ok := roomCall("other-9"); ok {
		t.Fatal("accepted a foreign room")
	}
}
