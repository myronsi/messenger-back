package redis

import (
	"context"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

func TestObserveTimesCommandsAndPipelines(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL is not set")
	}
	var mu sync.Mutex
	var ops []string
	s, err := New(url, Options{Observe: func(op string, d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if d >= 0 {
			ops = append(ops, op)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	key := "t" + uuid.NewString()
	if err := s.Client().Set(ctx, key, "1", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	pipe := s.Client().Pipeline()
	pipe.Get(ctx, key)
	pipe.Del(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Client().XReadGroup(ctx, &goredis.XReadGroupArgs{Group: "g", Consumer: "c", Streams: []string{key + ":missing", ">"}, Block: 10 * time.Millisecond}).Result(); err == nil {
		t.Fatal("expected the blocking read on a missing stream to fail")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"set", "pipeline"} {
		if !slices.Contains(ops, want) {
			t.Fatalf("observed %v, missing %q", ops, want)
		}
	}
	if slices.Contains(ops, "xreadgroup") {
		t.Fatalf("a blocking read was timed: %v", ops)
	}
}
