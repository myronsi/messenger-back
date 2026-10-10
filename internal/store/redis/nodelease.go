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

// highWaterTTL keeps the last issued time of a node long after its last holder stopped.
const highWaterTTL = 30 * 24 * time.Hour

// ErrNoFreeNode is returned when every node number is leased.
var ErrNoFreeNode = errors.New("node lease: every node number is in use")

// IDIssuer is the ID generator a lease protects (ids.Generator).
type IDIssuer interface {
	// LastMillis is the time of the newest issued ID, stored as the node's high-water mark.
	LastMillis() int64
	// ValidUntil stops the issuer after t unless it is extended.
	ValidUntil(t time.Time)
}

// NodeLease gives a running instance a node number for its Snowflake IDs (0..max) that no other running
// instance holds, so replicas need no per-instance configuration.
//
//   - ids:node:{n} holds a random token of this acquisition (prefixed with the instance id for operators)
//     and is renewed every TTL/3. The token, not the instance id, proves ownership.
//   - ids:node:{n}:hw is the node's high-water mark: the time of the newest ID any holder issued, written
//     on every renewal and on release. A new holder resumes after it, so it never repeats an ID of the
//     previous one, even right after a handoff, with IDs borrowed from future milliseconds or a clock
//     that is behind.
//   - The generator is only valid for TTL/2 after each successful renewal. A holder that was paused or cut
//     off stops issuing IDs before its lease can expire and be taken over.
//   - ids:epoch exists as long as Redis keeps its data. When a lease finds it missing, Redis started empty
//     (first start, or the data was lost) and may have forgotten leases that are still in use: the lease then
//     asks for a quarantine longer than any previous holder can still issue IDs (see Quarantine).
type NodeLease struct {
	rdb       Client
	prefix    string
	token     string
	ttl       time.Duration
	node      int
	highWater int64
	acquired  time.Time
	cold      bool
}

var (
	// KEYS: lease, high water, epoch; ARGV: token, ttl ms. Returns {high-water mark, cold start}, or {-1, 0}
	// when the node is taken. cold start is 1 when the epoch marker had to be created.
	acquireScript = goredis.NewScript(`
if not redis.call('SET', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[2]) then return {-1, 0} end
local cold = 0
if redis.call('SET', KEYS[3], '1', 'NX') then cold = 1 end
return {tonumber(redis.call('GET', KEYS[2]) or '0'), cold}`)

	// KEYS: lease, high water; ARGV: token, ttl ms, last millis, high-water ttl ms. Renews only a lease this
	// acquisition still holds and raises the high-water mark.
	renewScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
local hw = tonumber(redis.call('GET', KEYS[2]) or '0')
if tonumber(ARGV[3]) > hw then hw = tonumber(ARGV[3]) end
redis.call('SET', KEYS[2], string.format('%.0f', hw), 'PX', ARGV[4])
return 1`)

	// KEYS: lease, high water; ARGV: token, last millis, high-water ttl ms.
	releaseScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
local hw = tonumber(redis.call('GET', KEYS[2]) or '0')
if tonumber(ARGV[2]) > hw then hw = tonumber(ARGV[2]) end
redis.call('SET', KEYS[2], string.format('%.0f', hw), 'PX', ARGV[3])
return redis.call('DEL', KEYS[1])`)
)

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
		start := time.Now()
		res, err := acquireScript.Run(ctx, rdb, append(l.keys(node), prefix+"ids:epoch"), l.token, ttl.Milliseconds()).Int64Slice()
		if err != nil || len(res) != 2 {
			return nil, fmt.Errorf("node lease: %w", err)
		}
		if res[0] >= 0 {
			l.node, l.highWater, l.acquired, l.cold = node, res[0], start, res[1] == 1
			return l, nil
		}
	}
	return nil, ErrNoFreeNode
}

func (l *NodeLease) keys(node int) []string {
	k := l.prefix + "ids:node:" + strconv.Itoa(node)
	return []string{k, k + ":hw"}
}

// Node is the leased node number.
func (l *NodeLease) Node() int { return l.node }

// HighWater is the time (milliseconds since ids.Epoch) of the newest ID issued for the node before this
// lease; pass it to the generator's Resume.
func (l *NodeLease) HighWater() int64 { return l.highWater }

// validity is how long after a renewal the generator may issue IDs: well before the lease can expire.
func (l *NodeLease) validity() time.Duration { return l.ttl / 2 }

// quarantineMargin covers IDs a previous holder borrowed from the future and small clock differences.
const quarantineMargin = 10 * time.Second

// Quarantine is how long to wait after acquiring before the first ID, measured from the acquisition: 0
// normally, and longer than any previous holder can still issue IDs when Redis started empty (a holder
// whose lease Redis forgot stays valid for up to TTL/2 after its last renewal, which happened before
// Redis lost the data and so before this acquisition). Keep must already run while waiting.
func (l *NodeLease) Quarantine() time.Duration {
	if !l.cold {
		return 0
	}
	return l.validity() + quarantineMargin
}

// ErrLeaseLost means another instance may now use the node number: the process must stop creating IDs.
var ErrLeaseLost = errors.New("node lease: lost")

// Renew extends the lease once and records lastMillis as high-water mark.
func (l *NodeLease) Renew(ctx context.Context, lastMillis int64) error {
	ok, err := renewScript.Run(ctx, l.rdb, l.keys(l.node), l.token, l.ttl.Milliseconds(), lastMillis, highWaterTTL.Milliseconds()).Int()
	if err != nil {
		return fmt.Errorf("node lease: %w", err)
	}
	if ok != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Keep protects the issuer until ctx is done: it limits the issuer's validity, renews the lease every TTL/3
// and extends the validity after each renewal. It returns ErrLeaseLost when the lease is gone or renewals
// failed for most of the TTL; the caller must then stop the process (a restart leases a fresh number). On a
// normal stop it records the final high-water mark, releases the lease and returns nil.
func (l *NodeLease) Keep(ctx context.Context, issuer IDIssuer) error {
	issuer.ValidUntil(l.acquired.Add(l.validity()))
	t := time.NewTicker(l.ttl / 3)
	defer t.Stop()
	lastOK := l.acquired
	stop := func() { issuer.ValidUntil(time.Now().Add(-time.Nanosecond)) }
	for {
		select {
		case <-ctx.Done():
			stop()
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			_ = releaseScript.Run(rctx, l.rdb, l.keys(l.node), l.token, issuer.LastMillis(), highWaterTTL.Milliseconds()).Err()
			return nil
		case <-t.C:
		}
		// Measured before the call: the lease runs from when Redis could have renewed it at the earliest.
		start := time.Now()
		err := l.Renew(ctx, issuer.LastMillis())
		switch {
		case err == nil:
			lastOK = start
			issuer.ValidUntil(start.Add(l.validity()))
		case errors.Is(err, ErrLeaseLost):
			stop()
			return err
		case time.Since(lastOK) > l.ttl*2/3:
			// Renewals failed for most of the TTL: another instance may lease the number before we can.
			stop()
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
