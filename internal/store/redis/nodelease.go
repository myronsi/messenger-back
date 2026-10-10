package redis

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// NodeLeaseTTL is how long a node number stays leased without renewal.
const NodeLeaseTTL = 60 * time.Second

// ErrNoFreeNode is returned when every node number is leased.
var ErrNoFreeNode = errors.New("node lease: every node number is in use")

// NodeLease gives a running instance a node number for its Snowflake IDs (0..max) that no other running
// instance holds, so replicas need no per-instance configuration. The lease is a key ids:node:{n} holding a
// random token of this acquisition (prefixed with the instance id for operators), renewed every TTL/3. The
// token, not the instance id, proves ownership: a process that paused past the expiry cannot renew a lease
// that someone else acquired meanwhile, even with the same instance id.
type NodeLease struct {
	rdb    Client
	prefix string
	token  string
	ttl    time.Duration
	node   int
}

// AcquireNode leases a free node number between 0 and maxNode.
func AcquireNode(ctx context.Context, rdb Client, prefix, instance string, maxNode int, ttl time.Duration) (*NodeLease, error) {
	if ttl <= 0 {
		ttl = NodeLeaseTTL
	}
	if maxNode < 0 {
		return nil, errors.New("node lease: the largest node number must not be negative")
	}
	nonce, err := randomToken()
	if err != nil {
		return nil, err
	}
	l := &NodeLease{rdb: rdb, prefix: prefix, token: instance + ":" + nonce, ttl: ttl}
	// Start at a random number, so instances starting together rarely compete for the same keys.
	n := maxNode + 1
	first := rand.IntN(n) //nolint:gosec // spreading, not security
	for i := range n {
		node := (first + i) % n
		ok, err := rdb.SetNX(ctx, l.key(node), l.token, ttl).Result()
		if err != nil {
			return nil, fmt.Errorf("node lease: %w", err)
		}
		if ok {
			l.node = node
			return l, nil
		}
	}
	return nil, ErrNoFreeNode
}

func (l *NodeLease) key(node int) string { return l.prefix + "ids:node:" + strconv.Itoa(node) }

// Node is the leased node number.
func (l *NodeLease) Node() int { return l.node }

var (
	// KEYS[1] lease; ARGV: token, ttl ms. Renews only a lease this acquisition still holds.
	renewScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1`)

	// KEYS[1] lease; ARGV[1] token.
	releaseScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0`)
)

// ErrLeaseLost means another instance may now use the node number: the process must stop creating IDs.
var ErrLeaseLost = errors.New("node lease: lost")

// Renew extends the lease once.
func (l *NodeLease) Renew(ctx context.Context) error {
	ok, err := renewScript.Run(ctx, l.rdb, []string{l.key(l.node)}, l.token, l.ttl.Milliseconds()).Int()
	if err != nil {
		return fmt.Errorf("node lease: %w", err)
	}
	if ok != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Keep renews the lease every TTL/3 until ctx is done. It returns ErrLeaseLost when the lease is gone, or
// when renewing kept failing for so long that it may have expired; the caller must then stop the process
// (a restart leases a fresh number). On a normal stop it releases the lease and returns nil.
func (l *NodeLease) Keep(ctx context.Context) error {
	t := time.NewTicker(l.ttl / 3)
	defer t.Stop()
	lastOK := time.Now()
	for {
		select {
		case <-ctx.Done():
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			_ = releaseScript.Run(rctx, l.rdb, []string{l.key(l.node)}, l.token).Err()
			return nil
		case <-t.C:
		}
		err := l.Renew(ctx)
		switch {
		case err == nil:
			lastOK = time.Now()
		case errors.Is(err, ErrLeaseLost):
			return err
		case time.Since(lastOK) > l.ttl*2/3:
			// Renewals failed for most of the TTL: another instance may lease the number before we can.
			return fmt.Errorf("%w: %w", ErrLeaseLost, err)
		}
	}
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		return "", fmt.Errorf("node lease: %w", err)
	}
	return hex.EncodeToString(b), nil
}
