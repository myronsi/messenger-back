package redis

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

type delivery struct {
	user    int64
	payload string
}

// instance is one API process as far as Redis is concerned: its own client, bus and presence.
type instance struct {
	store    *Store
	bus      *Bus
	presence *Presence
	got      chan delivery
	resub    chan int64
}

func newInstance(t *testing.T, prefix, name string) *instance {
	t.Helper()
	s, err := New(os.Getenv("TEST_REDIS_URL"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	in := &instance{store: s, got: make(chan delivery, 16), resub: make(chan int64, 16)}
	in.bus, err = NewBus(s.Client(), BusOptions{
		Prefix:        prefix,
		Handler:       func(uid int64, p []byte) { in.got <- delivery{uid, string(p)} },
		OnResubscribe: func(uid int64) { in.resub <- uid },
	})
	if err != nil {
		t.Fatal(err)
	}
	in.presence, err = NewPresence(s.Client(), PresenceOptions{Prefix: prefix, Instance: name, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); in.bus.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = s.Close()
	})
	return in
}

func (in *instance) expect(t *testing.T, user int64, payload string) {
	t.Helper()
	select {
	case d := <-in.got:
		if d.user != user || d.payload != payload {
			t.Fatalf("got %+v, want user %d %q", d, user, payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("nothing delivered for user %d", user)
	}
}

func (in *instance) expectNothing(t *testing.T) {
	t.Helper()
	select {
	case d := <-in.got:
		t.Fatalf("unexpected delivery %+v", d)
	case <-time.After(200 * time.Millisecond):
	}
}

// connect is what the gateway does when a user's socket opens: subscribe and claim presence.
func (in *instance) connect(t *testing.T, uid int64) (Change, bool) {
	t.Helper()
	ctx := context.Background()
	if err := in.bus.Subscribe(ctx, uid); err != nil {
		t.Fatal(err)
	}
	c, changed, err := in.presence.Connect(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	return c, changed
}

// waitSubscribed publishes probes until the subscription is active (SUBSCRIBE is asynchronous).
func waitSubscribed(t *testing.T, from, to *instance, uid int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := from.bus.Publish(context.Background(), []byte("probe"), uid)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			to.expect(t, uid, "probe")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("subscription never became active")
}

func TestBusRequiresHandler(t *testing.T) {
	if _, err := NewBus(nil, BusOptions{}); err == nil {
		t.Fatal("accepted a bus without handler")
	}
}

func TestBusSubscriptionsAreCounted(t *testing.T) {
	_, prefix := testStore(t)
	a := newInstance(t, prefix, "a")
	b := newInstance(t, prefix, "b")
	ctx := context.Background()

	for range 2 { // two tabs
		if err := a.bus.Subscribe(ctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	waitSubscribed(t, b, a, 1)

	if err := a.bus.Unsubscribe(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if !a.bus.Subscribed(1) {
		t.Fatal("unsubscribed while a tab is open")
	}
	if _, err := b.bus.Publish(ctx, []byte("still here"), 1); err != nil {
		t.Fatal(err)
	}
	a.expect(t, 1, "still here")

	if err := a.bus.Unsubscribe(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if a.bus.Subscribed(1) {
		t.Fatal("still subscribed")
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := b.bus.Publish(ctx, []byte("gone"), 1); err != nil {
		t.Fatal(err)
	}
	a.expectNothing(t)
	// Unknown users are ignored.
	if err := a.bus.Unsubscribe(ctx, 99); err != nil {
		t.Fatal(err)
	}
}

// TestTwoInstances is the acceptance test of #47: two API instances deliver messages and presence
// between users connected to different instances.
func TestTwoInstances(t *testing.T) {
	_, prefix := testStore(t)
	a := newInstance(t, prefix, "a")
	b := newInstance(t, prefix, "b")
	ctx := context.Background()
	const alice, bob = 1, 2

	// Alice connects to A. Her presence change is announced to her contacts wherever they are.
	change, changed := a.connect(t, alice)
	if !changed || !change.Online {
		t.Fatal("alice did not come online")
	}
	// Bob connects to B and sees Alice online, although she is on the other instance.
	b.connect(t, bob)
	waitSubscribed(t, a, b, bob)
	waitSubscribed(t, b, a, alice)
	if m, err := b.presence.Online(ctx, alice, bob); err != nil || !m[alice] || !m[bob] {
		t.Fatalf("presence seen from B: %v %v", m, err)
	}

	// Bob sends a message through B to the chat with Alice: it is published to every member.
	msg, _ := json.Marshal(map[string]any{"type": "message", "chat_id": "42", "data": map[string]string{"content": "hi"}})
	n, err := b.bus.Publish(ctx, msg, alice, bob)
	if err != nil || n != 2 {
		t.Fatalf("publish: %d %v", n, err)
	}
	a.expect(t, alice, string(msg))
	b.expect(t, bob, string(msg))

	// Alice disconnects from A; the presence change reaches Bob on B through his channel.
	if err := a.bus.Unsubscribe(ctx, alice); err != nil {
		t.Fatal(err)
	}
	change, changed, err = a.presence.Disconnect(ctx, alice)
	if err != nil || !changed || change.Online {
		t.Fatalf("alice did not go offline: %+v %v %v", change, changed, err)
	}
	ev, _ := json.Marshal(map[string]any{"type": "presence", "data": map[string]any{"user_id": "1", "online": false}})
	if _, err := a.bus.Publish(ctx, ev, bob); err != nil {
		t.Fatal(err)
	}
	b.expect(t, bob, string(ev))
	if m, _ := b.presence.Online(ctx, alice); m[alice] {
		t.Fatal("alice still online")
	}
}

// After the connection to Redis is lost the bus subscribes again and reports the users whose events may
// have been lost, so the gateway can make their clients catch up.
func TestBusReportsResubscriptions(t *testing.T) {
	_, prefix := testStore(t)
	a := newInstance(t, prefix, "a")
	b := newInstance(t, prefix, "b")
	ctx := context.Background()
	if err := a.bus.Subscribe(ctx, 1); err != nil {
		t.Fatal(err)
	}
	waitSubscribed(t, b, a, 1)
	select {
	case uid := <-a.resub:
		t.Fatalf("first subscription reported as renewed: %d", uid)
	default:
	}

	// Drop every pub/sub connection on the server.
	if err := b.store.Client().Do(ctx, "CLIENT", "KILL", "TYPE", "pubsub").Err(); err != nil {
		t.Fatal(err)
	}
	select {
	case uid := <-a.resub:
		if uid != 1 {
			t.Fatalf("resubscribed %d", uid)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no resubscription reported")
	}
	waitSubscribed(t, b, a, 1)
}

func TestBusPublishEachAndBroadcast(t *testing.T) {
	_, prefix := testStore(t)
	a := newInstance(t, prefix, "a")
	ctx := context.Background()
	got := make(chan string, 4)
	s, err := New(os.Getenv("TEST_REDIS_URL"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	bc, err := NewBus(s.Client(), BusOptions{Prefix: prefix, Handler: func(int64, []byte) {}, OnBroadcast: func(p []byte) { got <- string(p) }})
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go bc.Run(rctx)

	for _, uid := range []int64{1, 2} {
		if err := a.bus.Subscribe(ctx, uid); err != nil {
			t.Fatal(err)
		}
		waitSubscribed(t, a, a, uid)
	}
	if err := a.bus.PublishEach(ctx, map[int64][]byte{1: []byte("for one"), 2: []byte("for two")}); err != nil {
		t.Fatal(err)
	}
	seen := map[int64]string{}
	for range 2 {
		select {
		case d := <-a.got:
			seen[d.user] = d.payload
		case <-time.After(3 * time.Second):
			t.Fatal("not delivered")
		}
	}
	if seen[1] != "for one" || seen[2] != "for two" {
		t.Fatalf("deliveries: %v", seen)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := a.bus.Broadcast(ctx, []byte("to everyone")); err != nil {
			t.Fatal(err)
		}
		select {
		case p := <-got:
			if p != "to everyone" {
				t.Fatalf("broadcast: %q", p)
			}
			return
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("broadcast not received")
		}
	}
}
