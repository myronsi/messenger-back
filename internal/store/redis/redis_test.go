package redis

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// testStore connects to TEST_REDIS_URL, or skips. Every test gets a key prefix of its own.
func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL is not set")
	}
	s, err := New(url, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("redis: %v", err)
	}
	return s, "t" + uuid.NewString() + ":"
}

func TestNewRejectsBadURL(t *testing.T) {
	if _, err := New("http://example.com", Options{}); err == nil {
		t.Fatal("accepted a non-redis URL")
	}
}

func TestHookSetsDeadline(t *testing.T) {
	h := hook{timeout: 50 * time.Millisecond}
	var got time.Duration
	var has bool
	next := func(ctx context.Context, _ goredis.Cmder) error {
		dl, ok := ctx.Deadline()
		has, got = ok, time.Until(dl)
		return nil
	}
	ctx := context.Background()

	if err := h.ProcessHook(next)(ctx, goredis.NewCmd(ctx, "get", "k")); err != nil {
		t.Fatal(err)
	}
	if !has || got > 50*time.Millisecond {
		t.Fatalf("plain command: deadline=%v in %v", has, got)
	}

	// A shorter deadline of the caller is kept.
	short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	_ = h.ProcessHook(next)(short, goredis.NewCmd(ctx, "get", "k"))
	if !has || got > 10*time.Millisecond {
		t.Fatalf("caller deadline lost: %v", got)
	}

	// Blocking commands wait as long as the caller says.
	if err := h.ProcessHook(next)(ctx, goredis.NewCmd(ctx, "xreadgroup", "GROUP", "g", "c", "BLOCK", 0)); err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("blocking command got a deadline")
	}

	var pipeHas bool
	_ = h.ProcessPipelineHook(func(ctx context.Context, _ []goredis.Cmder) error {
		_, pipeHas = ctx.Deadline()
		return nil
	})(ctx, nil)
	if !pipeHas {
		t.Fatal("pipeline without deadline")
	}
}

func TestHookCountsErrors(t *testing.T) {
	n := 0
	h := hook{timeout: time.Second, onError: func() { n++ }}
	ctx := context.Background()
	call := func(err error) {
		_ = h.ProcessHook(func(context.Context, goredis.Cmder) error { return err })(ctx, goredis.NewCmd(ctx, "get", "k"))
	}
	call(nil)
	call(goredis.Nil)
	call(context.DeadlineExceeded)
	if n != 1 {
		t.Fatalf("counted %d errors, want 1 (a missing key is not an error)", n)
	}
}

func TestStoreAgainstRedis(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	if err := s.Client().Set(ctx, prefix+"k", "v", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	// A blocking read longer than the default timeout is not cut short by the hook.
	start := time.Now()
	_, err := s.Client().BLPop(ctx, DefaultTimeout+500*time.Millisecond, prefix+"empty").Result()
	if !errors.Is(err, goredis.Nil) {
		t.Fatalf("blpop: %v", err)
	}
	if time.Since(start) < DefaultTimeout {
		t.Fatal("blocking command was cut short")
	}
}
