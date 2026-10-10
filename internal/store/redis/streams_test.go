package redis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// sink records handled entries by their "n" field, like an idempotent indexer keyed by message id.
type sink struct {
	mu      sync.Mutex
	seen    map[string]int
	handled chan string
}

func newSink() *sink { return &sink{seen: map[string]int{}, handled: make(chan string, 1000)} }

func (s *sink) record(e Entry) {
	s.mu.Lock()
	s.seen[e.Fields["n"]]++
	s.mu.Unlock()
	s.handled <- e.Fields["n"]
}

func (s *sink) distinct() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

func (s *sink) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for s.distinct() < n {
		if time.Now().After(deadline) {
			t.Fatalf("handled %d of %d", s.distinct(), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func run(t *testing.T, prefix, name string, h Handler, o ConsumerOptions) (stop func()) {
	t.Helper()
	s, err := New(os.Getenv("TEST_REDIS_URL"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	o.Prefix, o.Stream, o.Group, o.Name, o.Handler = prefix, StreamMessages, "test-group", name, h
	if o.Block == 0 {
		o.Block = 200 * time.Millisecond
	}
	if o.RetryAfter == 0 {
		o.RetryAfter = 200 * time.Millisecond
	}
	c, err := NewConsumer(s.Client(), o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done
			_ = s.Close()
		})
	}
	t.Cleanup(stop)
	return stop
}

func add(t *testing.T, st *Streams, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		if _, err := st.Add(context.Background(), StreamMessages, map[string]any{"type": "message.created", "n": fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
}

func pending(t *testing.T, s *Store, prefix string) int64 {
	t.Helper()
	p, err := s.Client().XPending(context.Background(), prefix+StreamMessages, "test-group").Result()
	if err != nil {
		t.Fatal(err)
	}
	return p.Count
}

func TestConsumerOptionsAreChecked(t *testing.T) {
	if _, err := NewConsumer(nil, ConsumerOptions{Stream: "s", Group: "g"}); err == nil {
		t.Fatal("accepted a consumer without name and handler")
	}
}

// A worker that is stopped for a while processes every missed entry when it starts again.
func TestConsumerCatchesUpAfterARestart(t *testing.T) {
	s, prefix := testStore(t)
	st := NewStreams(s.Client(), prefix, 0)
	out := newSink()
	h := func(_ context.Context, e Entry) error { out.record(e); return nil }

	add(t, st, 0, 10) // before the group exists: a new group starts at the beginning
	stop := run(t, prefix, "worker-1", h, ConsumerOptions{})
	out.waitFor(t, 10)
	stop()

	add(t, st, 10, 25) // while no worker runs
	run(t, prefix, "worker-1", h, ConsumerOptions{})
	out.waitFor(t, 25)
	time.Sleep(300 * time.Millisecond)
	if n := pending(t, s, prefix); n != 0 {
		t.Fatalf("%d entries left unacknowledged", n)
	}
	out.mu.Lock()
	defer out.mu.Unlock()
	for k, n := range out.seen {
		if n != 1 {
			t.Fatalf("entry %s handled %d times", k, n)
		}
	}
}

// Entries a crashed consumer took but never acknowledged are reclaimed by another one.
func TestCrashedConsumersEntriesAreReclaimed(t *testing.T) {
	s, prefix := testStore(t)
	st := NewStreams(s.Client(), prefix, 0)
	took := make(chan struct{}, 1)
	var crash func()
	crashed := func(ctx context.Context, e Entry) error {
		select {
		case took <- struct{}{}:
		default:
		}
		<-ctx.Done() // the process dies while handling: no ack
		return ctx.Err()
	}
	crash = run(t, prefix, "doomed", crashed, ConsumerOptions{Batch: 5})
	add(t, st, 0, 5)
	<-took
	crash()

	out := newSink()
	run(t, prefix, "survivor", func(_ context.Context, e Entry) error { out.record(e); return nil }, ConsumerOptions{})
	out.waitFor(t, 5)
}

func TestFailuresAreRetriedThenDeadLettered(t *testing.T) {
	s, prefix := testStore(t)
	st := NewStreams(s.Client(), prefix, 0)
	var mu sync.Mutex
	attempts := map[string]int{}
	var results []string
	h := func(_ context.Context, e Entry) error {
		mu.Lock()
		defer mu.Unlock()
		attempts[e.Fields["n"]]++
		switch e.Fields["n"] {
		case "flaky":
			if attempts["flaky"] < 3 {
				return errors.New("index unavailable")
			}
			return nil
		case "poison":
			return errors.New("cannot parse")
		case "later":
			if attempts["later"] == 1 {
				return RetryLater{After: 300 * time.Millisecond}
			}
			return nil
		}
		return nil
	}
	run(t, prefix, "w", h, ConsumerOptions{
		RetryAfter: 100 * time.Millisecond, MaxFailures: 3,
		OnResult: func(ok, dead bool) {
			mu.Lock()
			results = append(results, fmt.Sprint(ok, dead))
			mu.Unlock()
		},
	})
	for _, n := range []string{"flaky", "poison", "later"} {
		if _, err := st.Add(context.Background(), StreamMessages, map[string]any{"n": n}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		dead, _ := s.Client().XRange(context.Background(), prefix+StreamMessages+":dead", "-", "+").Result()
		mu.Lock()
		done := attempts["flaky"] >= 3 && attempts["later"] >= 2 && len(dead) == 1
		mu.Unlock()
		if done {
			if dead[0].Values["n"] != "poison" || dead[0].Values["dead_error"] != "cannot parse" || dead[0].Values["dead_group"] != "test-group" {
				t.Fatalf("dead letter: %v", dead[0].Values)
			}
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			t.Fatalf("attempts %v, dead letters %d", attempts, len(dead))
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if n := pending(t, s, prefix); n != 0 {
		t.Fatalf("%d entries left pending", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts["poison"] != 3 || attempts["later"] != 2 {
		t.Fatalf("attempts: %v", attempts)
	}
}

func TestPanickingHandlerIsDeadLettered(t *testing.T) {
	s, prefix := testStore(t)
	st := NewStreams(s.Client(), prefix, 0)
	out := newSink()
	run(t, prefix, "w", func(_ context.Context, e Entry) error {
		if e.Fields["n"] == "0" {
			panic("cannot index entry")
		}
		out.record(e)
		return nil
	}, ConsumerOptions{RetryAfter: 100 * time.Millisecond, MaxFailures: 2})
	add(t, st, 0, 2)
	out.waitFor(t, 1) // the consumer survived the panic
	deadline := time.Now().Add(10 * time.Second)
	for {
		dead, _ := s.Client().XRange(context.Background(), prefix+StreamMessages+":dead", "-", "+").Result()
		if len(dead) == 1 {
			if e, _ := dead[0].Values["dead_error"].(string); !strings.Contains(e, "panicked") {
				t.Fatalf("dead letter: %v", dead[0].Values)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the panicking entry was not dead-lettered")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEntriesInProgressAreNotReclaimed(t *testing.T) {
	s, prefix := testStore(t)
	st := NewStreams(s.Client(), prefix, 0)
	out := newSink()
	slow := func(_ context.Context, e Entry) error {
		time.Sleep(400 * time.Millisecond) // longer than RetryAfter: idle time alone would hand it to the other
		out.record(e)
		return nil
	}
	run(t, prefix, "a", slow, ConsumerOptions{Batch: 4})
	add(t, st, 0, 4)
	time.Sleep(100 * time.Millisecond) // a has taken the batch
	run(t, prefix, "b", slow, ConsumerOptions{Batch: 4})
	out.waitFor(t, 4)
	time.Sleep(600 * time.Millisecond)
	out.mu.Lock()
	defer out.mu.Unlock()
	for n, times := range out.seen {
		if times != 1 {
			t.Fatalf("entry %s was handled %d times", n, times)
		}
	}
}

func TestCrashLoopsAreCountedAndRecordsSwept(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	st := NewStreams(s.Client(), prefix, 0)
	key, retry := prefix+StreamMessages, prefix+StreamMessages+":retry:test-group"
	if err := s.Client().XGroupCreateMkStream(ctx, key, "test-group", "0").Err(); err != nil {
		t.Fatal(err)
	}
	add(t, st, 0, 2)
	// A consumer that died twice with the entries: "1" already reached the limit before its handler returned.
	got, err := s.Client().XReadGroup(ctx, &goredis.XReadGroupArgs{Group: "test-group", Consumer: "ghost", Streams: []string{key, ">"}, Count: 2}).Result()
	if err != nil {
		t.Fatal(err)
	}
	ids := got[0].Messages
	s.Client().HSet(ctx, retry, ids[1].ID, "0,3", "9-9", "0,1")
	time.Sleep(250 * time.Millisecond)

	var mu sync.Mutex
	var seen []string
	run(t, prefix, "w", func(ctx context.Context, e Entry) error {
		rec, _ := s.Client().HGet(ctx, retry, e.ID).Result()
		mu.Lock()
		seen = append(seen, e.Fields["n"]+"="+rec)
		mu.Unlock()
		return nil
	}, ConsumerOptions{MaxFailures: 3})
	deadline := time.Now().Add(10 * time.Second)
	for {
		dead, _ := s.Client().XRange(ctx, key+":dead", "-", "+").Result()
		mu.Lock()
		done := len(seen) == 1 && len(dead) == 1
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("handled %v, dead letters %d", seen, len(dead))
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	// The failure is recorded before a reclaimed entry's handler runs.
	if !strings.HasPrefix(seen[0], "0=") || !strings.HasSuffix(seen[0], ",1") {
		t.Fatalf("record during the handler: %v", seen)
	}
	mu.Unlock()
	if n, _ := s.Client().HLen(ctx, retry).Result(); n != 0 {
		t.Fatalf("%d retry records left (the orphan was not swept)", n)
	}
}
