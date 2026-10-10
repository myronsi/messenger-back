package redis

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeIssuer records what the lease tells the generator.
type fakeIssuer struct {
	mu    sync.Mutex
	last  int64
	valid time.Time
}

func (f *fakeIssuer) LastMillis() int64 { f.mu.Lock(); defer f.mu.Unlock(); return f.last }
func (f *fakeIssuer) ValidUntil(t time.Time) {
	f.mu.Lock()
	f.valid = t
	f.mu.Unlock()
}
func (f *fakeIssuer) validUntil() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.valid }

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
	if a.Node() == b.Node() || a.HighWater() != 0 {
		t.Fatalf("nodes %d %d, high water %d", a.Node(), b.Node(), a.HighWater())
	}
	if _, err := AcquireNode(ctx, s.Client(), prefix, "x", -1, time.Minute); err == nil {
		t.Fatal("accepted a negative bound")
	}
	if _, err := AcquireNode(ctx, s.Client(), prefix, "c", 1, time.Minute); !errors.Is(err, ErrNoFreeNode) {
		t.Fatalf("third lease: %v", err)
	}
	if err := a.Renew(ctx, 500); err != nil {
		t.Fatal(err)
	}

	// The same instance id acquiring again (a restarted pod, a paused process whose lease expired) gets a
	// lease of its own: the old acquisition cannot renew it, and the new one resumes after the old IDs.
	keys := a.keys(a.Node())
	if err := s.Client().Del(ctx, keys[0]).Err(); err != nil {
		t.Fatal(err)
	}
	a2, err := AcquireNode(ctx, s.Client(), prefix, "a", 1, time.Minute)
	if err != nil || a2.Node() != a.Node() || a2.HighWater() != 500 {
		t.Fatalf("reacquire: node %d high water %d %v", a2.Node(), a2.HighWater(), err)
	}
	if err := a.Renew(ctx, 600); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale acquisition renewed: %v", err)
	}
	// The high-water mark never moves back.
	if err := a2.Renew(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if hw, _ := s.Client().Get(ctx, keys[1]).Int64(); hw != 500 {
		t.Fatalf("high water %d", hw)
	}
}

// A handoff through a normal stop: the final high-water mark is recorded and the successor starts after it.
func TestNodeLeaseHandoff(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	l, err := AcquireNode(ctx, s.Client(), prefix, "a", 0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &fakeIssuer{}
	kctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- l.Keep(kctx, issuer) }()
	time.Sleep(50 * time.Millisecond)
	if v := issuer.validUntil(); time.Until(v) <= 0 || time.Until(v) > 30*time.Second {
		t.Fatalf("validity %v", time.Until(v))
	}
	issuer.mu.Lock()
	issuer.last = 12345 // IDs issued, some borrowed from the future
	issuer.mu.Unlock()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if time.Until(issuer.validUntil()) > 0 {
		t.Fatal("issuer still valid after release")
	}
	next, err := AcquireNode(ctx, s.Client(), prefix, "b", 0, time.Minute)
	if err != nil || next.HighWater() != 12345 {
		t.Fatalf("successor: high water %d %v", next.HighWater(), err)
	}
}

func TestNodeLeaseKeepReportsLoss(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	l, err := AcquireNode(ctx, s.Client(), prefix, "a", 3, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Client().Del(ctx, l.keys(l.Node())[0]).Err(); err != nil {
		t.Fatal(err)
	}
	issuer := &fakeIssuer{}
	done := make(chan error, 1)
	go func() { done <- l.Keep(ctx, issuer) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("keep: %v", err)
		}
		if time.Until(issuer.validUntil()) > 0 {
			t.Fatal("issuer still valid after the loss")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("loss not reported")
	}
}
