package redis

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

func newPresence(t *testing.T, s *Store, prefix, instance string, ttl time.Duration) *Presence {
	t.Helper()
	p, err := NewPresence(s.Client(), PresenceOptions{Prefix: prefix, Instance: instance, TTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPresenceOptions(t *testing.T) {
	if _, err := NewPresence(nil, PresenceOptions{}); err == nil {
		t.Fatal("accepted an empty instance id")
	}
	if _, err := NewPresence(nil, PresenceOptions{Instance: "a", TTL: time.Millisecond}); err == nil {
		t.Fatal("accepted a tiny ttl")
	}
}

func online(t *testing.T, p *Presence, uid int64) bool {
	t.Helper()
	m, err := p.Online(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	return m[uid]
}

func TestPresenceAcrossInstances(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	a := newPresence(t, s, prefix, "a", time.Minute)
	b := newPresence(t, s, prefix, "b", time.Minute)
	const uid = 7

	if online(t, b, uid) {
		t.Fatal("online before connecting")
	}
	c, changed, err := a.Connect(ctx, uid)
	if err != nil || !changed || !c.Online || c.UserID != uid || c.Version <= 0 {
		t.Fatalf("first connection: %+v %v %v", c, changed, err)
	}
	first := c.Version
	// A second tab on A and a connection on B are no transitions.
	if _, changed, _ := a.Connect(ctx, uid); changed {
		t.Fatal("second local connection reported a transition")
	}
	if _, changed, _ := b.Connect(ctx, uid); changed {
		t.Fatal("connection on another instance reported a transition")
	}
	if !online(t, b, uid) || !online(t, a, uid) {
		t.Fatal("not online")
	}

	// A closes both tabs: B still holds a connection.
	for range 2 {
		if _, changed, err := a.Disconnect(ctx, uid); changed || err != nil {
			t.Fatalf("disconnect on A: %v %v", changed, err)
		}
	}
	if !online(t, a, uid) {
		t.Fatal("offline while B holds a connection")
	}
	c, changed, err = b.Disconnect(ctx, uid)
	if err != nil || !changed || c.Online || c.Version <= first {
		t.Fatalf("last connection: %+v %v %v", c, changed, err)
	}
	if online(t, a, uid) {
		t.Fatal("still online")
	}
	// Disconnecting an unknown user is a no-op.
	if _, changed, err := b.Disconnect(ctx, uid); changed || err != nil {
		t.Fatal("extra disconnect")
	}

	c2, changed, _ := a.Connect(ctx, uid)
	if !changed || c2.Version <= c.Version {
		t.Fatalf("versions must increase: %d then %d", c.Version, c2.Version)
	}
}

func TestPresenceCrashedInstanceIsSwept(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	crashed := newPresence(t, s, prefix, "crashed", time.Second)
	survivor := newPresence(t, s, prefix, "survivor", time.Second)
	other := newPresence(t, s, prefix, "other", time.Second)

	if _, _, err := crashed.Connect(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := crashed.Connect(ctx, 2); err != nil {
		t.Fatal(err)
	}
	// User 2 also has a connection on the survivor, which keeps heartbeating.
	if _, _, err := survivor.Connect(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if changes, err := survivor.Sweep(ctx); err != nil || len(changes) != 0 {
		t.Fatalf("swept live claims: %v %v", changes, err)
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := survivor.Heartbeat(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Two instances sweep at once; user 1 goes offline exactly once, user 2 stays.
	var mu sync.Mutex
	var all []Change
	var wg sync.WaitGroup
	for _, p := range []*Presence{survivor, other} {
		wg.Go(func() {
			changes, err := p.Sweep(ctx)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			all = append(all, changes...)
			mu.Unlock()
		})
	}
	wg.Wait()
	if len(all) != 1 || all[0].UserID != 1 || all[0].Online {
		t.Fatalf("sweep: %+v", all)
	}
	if online(t, other, 1) || !online(t, other, 2) {
		t.Fatal("wrong presence after the sweep")
	}
	if changes, _ := other.Sweep(ctx); len(changes) != 0 {
		t.Fatalf("announced twice: %+v", changes)
	}
}

func TestPresenceHeartbeatRestoresLostClaims(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	p := newPresence(t, s, prefix, "a", time.Minute)
	for _, uid := range []int64{1, 2, 3} {
		if _, _, err := p.Connect(ctx, uid); err != nil {
			t.Fatal(err)
		}
	}
	if changes, err := p.Heartbeat(ctx); err != nil || len(changes) != 0 {
		t.Fatalf("heartbeat of live claims: %v %v", changes, err)
	}
	// Redis lost user 2 (a restart without persistence, a flush).
	if err := s.Client().Del(ctx, p.key(2)).Err(); err != nil {
		t.Fatal(err)
	}
	changes, err := p.Heartbeat(ctx)
	if err != nil || len(changes) != 1 || changes[0].UserID != 2 || !changes[0].Online {
		t.Fatalf("heartbeat: %+v %v", changes, err)
	}
	local := p.Local()
	slices.Sort(local)
	if !slices.Equal(local, []int64{1, 2, 3}) {
		t.Fatalf("local users: %v", local)
	}
}

func TestPresenceRunReportsChanges(t *testing.T) {
	s, prefix := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	crashed := newPresence(t, s, prefix, "crashed", time.Second)
	if _, _, err := crashed.Connect(ctx, 5); err != nil {
		t.Fatal(err)
	}

	got := make(chan Change, 4)
	p, err := NewPresence(s.Client(), PresenceOptions{
		Prefix: prefix, Instance: "runner", TTL: time.Second,
		OnChange: func(cs []Change) {
			for _, c := range cs {
				got <- c
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	select {
	case c := <-got:
		if c.UserID != 5 || c.Online {
			t.Fatalf("change: %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the crashed instance's user was not announced offline")
	}
	cancel()
	<-done
}

// After a crash of an instance with many users, one sweep announces all of them, not one batch.
func TestPresenceSweepDrainsEveryBatch(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	crashed := newPresence(t, s, prefix, "crashed", time.Second)
	const users = 2*sweepBatch + 100
	crashed.mu.Lock()
	for uid := int64(1); uid <= users; uid++ {
		crashed.conns[uid] = 1
	}
	crashed.mu.Unlock()
	if _, err := crashed.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)

	sweeper := newPresence(t, s, prefix, "sweeper", 10*time.Second)
	changes, err := sweeper.Sweep(ctx)
	if err != nil || len(changes) != users {
		t.Fatalf("swept %d of %d: %v", len(changes), users, err)
	}
}

// A user whose last connection closes while a heartbeat is in flight must not stay claimed.
func TestPresenceHeartbeatDoesNotResurrectDisconnectedUsers(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	p := newPresence(t, s, prefix, "a", time.Minute)
	if _, _, err := p.Connect(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Disconnect(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// What a heartbeat that took its snapshot before the disconnect does: claim, then notice and release.
	if _, err := p.claim(ctx, []int64{1}); err != nil {
		t.Fatal(err)
	}
	gone := p.notLocal([]int64{1})
	if !gone[1] {
		t.Fatal("user still local")
	}
	if _, err := p.settle(ctx, []int64{1}, p.instance); err != nil {
		t.Fatal(err)
	}
	if online(t, p, 1) {
		t.Fatal("disconnected user is online")
	}
}
