package redis

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnreadRebuildAndCount(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	u := NewUnread(s.Client(), prefix)
	const alice, bob = 1, 2

	// Nothing is counted for a user whose hash is not built: the rebuild includes those messages.
	if err := u.Increment(ctx, 10, alice); err != nil {
		t.Fatal(err)
	}
	if _, built, err := u.Get(ctx, alice); err != nil || built {
		t.Fatalf("built without a rebuild: %v %v", built, err)
	}

	rebuilds := 0
	rebuild := func(context.Context) (map[int64]int64, error) {
		rebuilds++
		return map[int64]int64{10: 3, 11: 0}, nil
	}
	counts, err := u.Counts(ctx, alice, rebuild)
	if err != nil || !maps.Equal(counts, map[int64]int64{10: 3, 11: 0}) {
		t.Fatalf("counts: %v %v", counts, err)
	}
	if err := u.Increment(ctx, 10, alice, bob); err != nil {
		t.Fatal(err)
	}
	if err := u.Increment(ctx, 11, alice); err != nil {
		t.Fatal(err)
	}
	counts, err = u.Counts(ctx, alice, rebuild)
	if err != nil || rebuilds != 1 || !maps.Equal(counts, map[int64]int64{10: 4, 11: 1}) {
		t.Fatalf("after increments: %v (rebuilds %d) %v", counts, rebuilds, err)
	}

	if err := u.Reset(ctx, alice, 10); err != nil {
		t.Fatal(err)
	}
	if err := u.Set(ctx, alice, 11, 5); err != nil {
		t.Fatal(err)
	}
	counts, _, _ = u.Get(ctx, alice)
	if !maps.Equal(counts, map[int64]int64{11: 5}) {
		t.Fatalf("after reset and set: %v", counts)
	}
	if err := u.Forget(ctx, 11, alice, bob); err != nil {
		t.Fatal(err)
	}
	counts, built, _ := u.Get(ctx, alice)
	if !built || len(counts) != 0 {
		t.Fatalf("after forget: %v built=%v", counts, built)
	}
	// Set on an unbuilt hash does not make it look built.
	if err := u.Set(ctx, bob, 10, 2); err != nil {
		t.Fatal(err)
	}
	if _, built, _ := u.Get(ctx, bob); built {
		t.Fatal("set built the hash")
	}

	failing := errors.New("scylla down")
	if _, err := u.Counts(ctx, bob, func(context.Context) (map[int64]int64, error) { return nil, failing }); !errors.Is(err, failing) {
		t.Fatalf("rebuild error: %v", err)
	}
}

func TestUnreadConcurrentRebuildsKeepTheFirst(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	u := NewUnread(s.Client(), prefix)
	if err := u.Load(ctx, 1, map[int64]int64{5: 1}); err != nil {
		t.Fatal(err)
	}
	if err := u.Increment(ctx, 5, 1); err != nil {
		t.Fatal(err)
	}
	// A slower rebuild that read older data must not overwrite the counts.
	if err := u.Load(ctx, 1, map[int64]int64{5: 1}); err != nil {
		t.Fatal(err)
	}
	counts, _, _ := u.Get(ctx, 1)
	if counts[5] != 2 {
		t.Fatalf("count %d, want 2", counts[5])
	}
}

func TestMembersCache(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	m := NewMembers(s.Client(), prefix, time.Minute)

	var loads atomic.Int32
	members := []int64{1, 2}
	load := func(context.Context, int64) ([]int64, error) {
		loads.Add(1)
		return slices.Clone(members), nil
	}
	got, err := m.Get(ctx, 9, load)
	slices.Sort(got)
	if err != nil || !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("get: %v %v", got, err)
	}
	for _, c := range []struct {
		uid  int64
		want bool
	}{{1, true}, {2, true}, {3, false}} {
		ok, err := m.IsMember(ctx, 9, c.uid, load)
		if err != nil || ok != c.want {
			t.Fatalf("is %d a member: %v %v", c.uid, ok, err)
		}
	}
	if n := loads.Load(); n != 1 {
		t.Fatalf("loaded %d times, want 1", n)
	}

	// Bob is removed: the next read loads again.
	members = []int64{1}
	if err := m.Invalidate(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.IsMember(ctx, 9, 2, load); ok {
		t.Fatal("removed member still cached")
	}
	if n := loads.Load(); n != 2 {
		t.Fatalf("loaded %d times, want 2", n)
	}

	// A chat without members is cached too.
	empty := func(context.Context, int64) ([]int64, error) { loads.Add(1); return nil, nil }
	for range 2 {
		if got, err := m.Get(ctx, 10, empty); err != nil || len(got) != 0 {
			t.Fatalf("empty chat: %v %v", got, err)
		}
	}
	if n := loads.Load(); n != 3 {
		t.Fatalf("empty chat loaded %d times, want once", loads.Load()-2)
	}

	failing := errors.New("postgres down")
	if _, err := m.IsMember(ctx, 11, 1, func(context.Context, int64) ([]int64, error) { return nil, failing }); !errors.Is(err, failing) {
		t.Fatalf("load error: %v", err)
	}
}

// A fill that read the members before a change must not overwrite the invalidation of that change.
func TestMembersStaleFillIsRefused(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	m := NewMembers(s.Client(), prefix, time.Minute)

	stale := func(ctx context.Context, chatID int64) ([]int64, error) {
		// The member list is read, then the user is removed and the cache invalidated before the fill.
		if err := m.Invalidate(ctx, chatID); err != nil {
			return nil, err
		}
		return []int64{1, 2}, nil
	}
	if _, err := m.Get(ctx, 9, stale); err != nil {
		t.Fatal(err)
	}
	fresh := func(context.Context, int64) ([]int64, error) { return []int64{1}, nil }
	if ok, _ := m.IsMember(ctx, 9, 2, fresh); ok {
		t.Fatal("stale fill was cached")
	}
}

func TestRateLimiterBurstAndRate(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	l := NewRateLimiter(s.Client(), prefix)
	r := Rate{Name: "send", Rate: 10, Period: time.Second, Burst: 3}

	for i := range 3 {
		res, err := l.Allow(ctx, r, "alice")
		if err != nil || !res.Allowed || res.Remaining != 2-i {
			t.Fatalf("event %d: %+v %v", i, res, err)
		}
	}
	res, err := l.Allow(ctx, r, "alice")
	if err != nil || res.Allowed || res.RetryAfter <= 0 || res.RetryAfter > 100*time.Millisecond {
		t.Fatalf("over the burst: %+v %v", res, err)
	}
	if res, _ := l.Allow(ctx, r, "bob"); !res.Allowed {
		t.Fatal("subjects share a limit")
	}
	time.Sleep(res.RetryAfter + 10*time.Millisecond)
	if res, _ := l.Allow(ctx, r, "alice"); !res.Allowed {
		t.Fatal("not allowed after waiting")
	}
	// Too many at once is refused without using anything up.
	if res, _ := l.AllowN(ctx, r, "carol", 4); res.Allowed {
		t.Fatal("allowed more than the burst")
	}
	if res, _ := l.AllowN(ctx, r, "carol", 3); !res.Allowed {
		t.Fatal("refused a full burst")
	}
	if _, err := l.Allow(ctx, Rate{Name: "x"}, "a"); err == nil {
		t.Fatal("accepted an invalid rate")
	}
}

func TestRateLimiterIsAtomic(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	l := NewRateLimiter(s.Client(), prefix)
	r := Rate{Name: "typing", Rate: 1, Period: time.Minute, Burst: 10}
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			res, err := l.Allow(ctx, r, "alice")
			if err != nil {
				t.Error(err)
			}
			if res.Allowed {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if n := allowed.Load(); n != 10 {
		t.Fatalf("%d allowed, want the burst of 10", n)
	}
}
