package redis

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNodeLease(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	a, err := AcquireNode(ctx, s.Client(), prefix, "a", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b, err := AcquireNode(ctx, s.Client(), prefix, "b", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if a.Node() == b.Node() {
		t.Fatalf("both got node %d", a.Node())
	}
	if _, err := AcquireNode(ctx, s.Client(), prefix, "c", 1, time.Minute); !errors.Is(err, ErrNoFreeNode) {
		t.Fatalf("third lease: %v", err)
	}
	if err := a.Renew(ctx); err != nil {
		t.Fatal(err)
	}

	// The same instance id acquiring again (a restarted pod, a paused process whose lease expired) gets a
	// lease of its own: the old acquisition cannot renew it.
	if err := s.Client().Del(ctx, b.key(b.Node())).Err(); err != nil {
		t.Fatal(err)
	}
	b2, err := AcquireNode(ctx, s.Client(), prefix, "b", 1, time.Minute)
	if err != nil || b2.Node() != b.Node() {
		t.Fatalf("reacquire: %v", err)
	}
	if err := b.Renew(ctx); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale acquisition renewed: %v", err)
	}
	b = b2

	// Somebody else holds a's number now (Redis lost the key, another instance took it).
	if err := s.Client().Set(ctx, a.key(a.Node()), "intruder", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := a.Renew(ctx); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("renew of a lost lease: %v", err)
	}

	// Keep releases the lease on a normal stop.
	kctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- b.Keep(kctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Client().Exists(ctx, b.key(b.Node())).Result(); n != 0 {
		t.Fatal("lease not released")
	}
}

func TestNodeLeaseKeepReportsLoss(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	l, err := AcquireNode(ctx, s.Client(), prefix, "a", 3, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Client().Del(ctx, l.key(l.Node())).Err(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-func() chan error { c := make(chan error, 1); go func() { c <- l.Keep(ctx) }(); return c }():
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("keep: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("loss not reported")
	}
}
