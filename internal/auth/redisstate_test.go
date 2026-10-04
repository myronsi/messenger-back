package auth

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// testRedis connects to TEST_REDIS_URL, or skips. Every test uses its own key namespace.
func testRedis(t *testing.T) (Redis, string) {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL is not set")
	}
	opts, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := goredis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis: %v", err)
	}
	return c, "t" + uuid.NewString()
}

func TestLimiterSlidingWindow(t *testing.T) {
	rdb, ns := testRedis(t)
	ctx := context.Background()
	l := NewLimiter(rdb, ns)
	rule := Rule{Name: "r", Limit: 3, Window: 400 * time.Millisecond}

	for i := range 3 {
		ok, _, err := l.Check(ctx, rule, "alice")
		if err != nil || !ok {
			t.Fatalf("check %d: %v %v", i, ok, err)
		}
		if err := l.Hit(ctx, rule, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	ok, retry, err := l.Check(ctx, rule, "alice")
	if err != nil || ok || retry <= 0 || retry > 400*time.Millisecond {
		t.Fatalf("over the limit: ok=%v retry=%v err=%v", ok, retry, err)
	}
	// Other subjects and other rules are independent.
	if ok, _, _ := l.Check(ctx, rule, "bob"); !ok {
		t.Fatal("subjects share a counter")
	}
	if ok, _, _ := l.Check(ctx, Rule{Name: "other", Limit: 3, Window: time.Second}, "alice"); !ok {
		t.Fatal("rules share a counter")
	}
	time.Sleep(450 * time.Millisecond)
	if ok, _, _ := l.Check(ctx, rule, "alice"); !ok {
		t.Fatal("the window did not slide")
	}

	// Reset clears the subject.
	for range 3 {
		_ = l.Hit(ctx, rule, "carol")
	}
	if ok, _, _ := l.Check(ctx, rule, "carol"); ok {
		t.Fatal("not limited")
	}
	if err := l.Reset(ctx, rule, "carol"); err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := l.Check(ctx, rule, "carol"); !ok {
		t.Fatal("reset did not clear")
	}
}

func TestLimiterTakeIsAtomic(t *testing.T) {
	rdb, ns := testRedis(t)
	ctx := context.Background()
	l := NewLimiter(rdb, ns)
	rule := Rule{Name: "take", Limit: 5, Window: time.Minute}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := l.Take(ctx, rule, "ip")
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 5 {
		t.Fatalf("%d of 40 concurrent requests passed a limit of 5", allowed)
	}
}

func TestLimiterReserveAndRelease(t *testing.T) {
	rdb, ns := testRedis(t)
	ctx := context.Background()
	l := NewLimiter(rdb, ns)
	rule := Rule{Name: "reserve", Limit: 3, Window: time.Minute}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var members []string
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, ok, _, err := l.Reserve(ctx, rule, "u")
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				members = append(members, m)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(members) != 3 {
		t.Fatalf("%d of 30 concurrent reservations passed a limit of 3", len(members))
	}
	if _, ok, _, _ := l.Reserve(ctx, rule, "u"); ok {
		t.Fatal("the limit is reached")
	}
	if err := l.Release(ctx, rule, "u", members[0]); err != nil {
		t.Fatal(err)
	}
	if _, ok, _, _ := l.Reserve(ctx, rule, "u"); !ok {
		t.Fatal("a released reservation frees one slot")
	}
}

func TestLimiterKeysAreDigests(t *testing.T) {
	rdb, ns := testRedis(t)
	ctx := context.Background()
	l := NewLimiter(rdb, ns)
	if err := l.Hit(ctx, RuleLoginUser, "secret-username"); err != nil {
		t.Fatal(err)
	}
	keys, err := rdb.Keys(ctx, ns+":rl:*").Result()
	if err != nil || len(keys) != 1 {
		t.Fatalf("%v %v", keys, err)
	}
	if want := "secret-username"; len(keys[0]) == 0 || strings.Contains(keys[0], want) {
		t.Fatalf("the subject leaks into the key %q", keys[0])
	}
}

func TestLimiterFailsClosedWhenRedisIsDown(t *testing.T) {
	c := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	defer c.Close()
	l := NewLimiter(c, "x")
	ok, _, err := l.Check(context.Background(), RuleLoginIP, "1.2.3.4")
	if ok || err == nil {
		t.Fatalf("ok=%v err=%v: an unreachable limiter must deny", ok, err)
	}
}

func TestChallenges(t *testing.T) {
	rdb, ns := testRedis(t)
	ctx := context.Background()
	c := NewChallenges(rdb, ns)

	tok, err := c.Create(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if uid, err := c.Attempt(ctx, tok); err != nil || uid != 7 {
		t.Fatalf("attempt: %d %v", uid, err)
	}
	if _, err := c.Attempt(ctx, "unknown"); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("unknown challenge: %v", err)
	}
	if err := c.Consume(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if err := c.Consume(ctx, tok); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("a challenge was consumed twice: %v", err)
	}
	if _, err := c.Attempt(ctx, tok); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("a consumed challenge is still usable: %v", err)
	}

	// Five tries are allowed, the sixth finds nothing, and the challenge stays dead.
	tok, _ = c.Create(ctx, 8)
	for i := range MaxChallengeAttempts {
		if _, err := c.Attempt(ctx, tok); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if _, err := c.Attempt(ctx, tok); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("the sixth attempt: %v", err)
	}
	if _, err := c.Attempt(ctx, tok); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("after exhaustion: %v", err)
	}

	// The challenge expires on its own and the key is a digest, not the token.
	tok, _ = c.Create(ctx, 9)
	keys, _ := rdb.Keys(ctx, ns+":2fa:*").Result()
	for _, k := range keys {
		if strings.Contains(k, tok) {
			t.Fatal("the token is in the key")
		}
		if ttl := rdb.TTL(ctx, k).Val(); ttl <= 0 || ttl > ChallengeTTL {
			t.Fatalf("ttl %v", ttl)
		}
	}
}

func TestTOTPReplay(t *testing.T) {
	rdb, ns := testRedis(t)
	r := NewTOTPReplay(rdb, ns)
	ctx := context.Background()
	if ok, err := r.Claim(ctx, 1, 100); err != nil || !ok {
		t.Fatalf("first use: %v %v", ok, err)
	}
	if ok, _ := r.Claim(ctx, 1, 100); ok {
		t.Fatal("a code was accepted twice")
	}
	if ok, _ := r.Claim(ctx, 2, 100); !ok {
		t.Fatal("another user's code was refused")
	}
	if ok, _ := r.Claim(ctx, 1, 101); !ok {
		t.Fatal("the next step was refused")
	}
}

func TestSessionCache(t *testing.T) {
	rdb, ns := testRedis(t)
	ctx := context.Background()
	c := NewSessionCache(rdb, ns, time.Minute)
	id := uuid.New()

	if _, ok, err := c.Get(ctx, id); ok || err != nil {
		t.Fatalf("miss: %v %v", ok, err)
	}
	exp := time.Now().Add(time.Hour)
	if err := c.Put(ctx, id, 5, exp); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get(ctx, id)
	if err != nil || !ok || got.UserID != 5 || got.Revoked || got.Expires.Unix() != exp.Unix() {
		t.Fatalf("hit: %+v %v %v", got, ok, err)
	}
	if err := c.Revoke(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := c.Get(ctx, id); !ok || !got.Revoked {
		t.Fatalf("after revoke: %+v %v", got, ok)
	}
	// A cache fill that raced with the revocation must not bring the session back.
	if err := c.Put(ctx, id, 5, exp); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := c.Get(ctx, id); !got.Revoked {
		t.Fatal("a late cache fill overwrote the revocation")
	}
	// Nothing is cached for a session that is about to expire anyway.
	other := uuid.New()
	_ = c.Put(ctx, other, 5, time.Now().Add(-time.Second))
	if _, ok, _ := c.Get(ctx, other); ok {
		t.Fatal("an expired session was cached")
	}

	if !c.ShouldTouch(ctx, id) || c.ShouldTouch(ctx, id) {
		t.Fatal("touches are not throttled to one per interval")
	}
}

func TestTickets(t *testing.T) {
	rdb, ns := testRedis(t)
	ctx := context.Background()
	tk := NewTickets(rdb, ns)
	sid := uuid.New()
	ticket, err := tk.Issue(ctx, Ticket{UserID: 3, SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.Redeem(ctx, ticket)
	if err != nil || got.UserID != 3 || got.SessionID != sid {
		t.Fatalf("redeem: %+v %v", got, err)
	}
	if _, err := tk.Redeem(ctx, ticket); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("a ticket worked twice: %v", err)
	}
	if _, err := tk.Redeem(ctx, "nope"); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("unknown ticket: %v", err)
	}
	ticket, _ = tk.Issue(ctx, Ticket{UserID: 3, SessionID: sid})
	keys, _ := rdb.Keys(ctx, ns+":wsticket:*").Result()
	if len(keys) != 1 || strings.Contains(keys[0], ticket) {
		t.Fatalf("keys %v", keys)
	}
	if ttl := rdb.TTL(ctx, keys[0]).Val(); ttl <= 0 || ttl > TicketTTL {
		t.Fatalf("ttl %v", ttl)
	}
}
