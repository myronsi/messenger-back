package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// DefaultDedupTTL is how long a client_temp_id is remembered: longer than any client keeps retrying.
const DefaultDedupTTL = 24 * time.Hour

// Dedup makes sends idempotent: it remembers which message a user's client_temp_id produced, so a send that is
// repeated after a lost acknowledgement returns the first message instead of creating a copy.
type Dedup struct {
	rdb    Client
	prefix string
	ttl    time.Duration
}

// NewDedup returns the store under the prefix ("" in production); ttl 0 means DefaultDedupTTL.
func NewDedup(rdb Client, prefix string, ttl time.Duration) *Dedup {
	if ttl <= 0 {
		ttl = DefaultDedupTTL
	}
	return &Dedup{rdb: rdb, prefix: prefix, ttl: ttl}
}

func (d *Dedup) key(userID int64, clientTempID string) string {
	// The client chooses the id: hash it, so it cannot shape key names.
	return d.prefix + "sent:" + strconv.FormatInt(userID, 10) + ":" + digest(clientTempID)
}

// Claim records id for the user's client_temp_id unless one is recorded already, in which case that one is
// returned with claimed false.
func (d *Dedup) Claim(ctx context.Context, userID int64, clientTempID string, id int64) (existing int64, claimed bool, err error) {
	old, err := d.rdb.SetArgs(ctx, d.key(userID, clientTempID), id, goredis.SetArgs{Mode: "NX", Get: true, TTL: d.ttl}).Result()
	if errors.Is(err, goredis.Nil) {
		return id, true, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("dedup claim: %w", err)
	}
	prev, err := strconv.ParseInt(old, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("dedup claim: corrupt entry")
	}
	return prev, false, nil
}

// KEYS[1] claim; ARGV[1] id. Deletes the claim only if it still records id.
var releaseClaimScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0`)

// Release forgets a claim whose send failed, so the client's retry can succeed. Only the claim of id is
// removed.
func (d *Dedup) Release(ctx context.Context, userID int64, clientTempID string, id int64) error {
	if err := releaseClaimScript.Run(ctx, d.rdb, []string{d.key(userID, clientTempID)}, strconv.FormatInt(id, 10)).Err(); err != nil {
		return fmt.Errorf("dedup release: %w", err)
	}
	return nil
}
