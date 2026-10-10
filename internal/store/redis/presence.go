package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// DefaultPresenceTTL is how long an instance's claim "this user is connected here" lasts without a
// heartbeat. A crashed instance's users go offline within about this time.
const DefaultPresenceTTL = 60 * time.Second

// sweepBatch bounds how many expired users one sweep looks at.
const sweepBatch = 500

// Change is a presence transition: the user came online (first connection on any instance) or went
// offline (last connection closed, or the instance holding it stopped heartbeating).
type Change struct {
	UserID int64
	Online bool
	// Version increases with every transition of the user (it is a microsecond timestamp of the Redis
	// clock, or the previous version plus one). Subscribers drop changes older than the last one they
	// applied, because changes published by different instances can overtake each other.
	Version int64
}

// PresenceOptions configures Presence.
type PresenceOptions struct {
	// Prefix is put in front of every key ("" in production; tests use a namespace of their own).
	Prefix string
	// Instance identifies this API process; it must be unique among the running instances.
	Instance string
	// TTL is how long a claim lasts without a heartbeat (DefaultPresenceTTL when 0). Run heartbeats every
	// TTL/3 and sweeps every TTL/4.
	TTL time.Duration
	// OnChange receives the transitions that Run detects (users of a crashed instance going offline, users
	// of this instance coming back after Redis lost their claim). It must not block.
	OnChange func([]Change)
	Log      *slog.Logger
}

// Presence tracks which users have a WebSocket connection on some instance.
//
// Redis holds a hash presence:{user_id} with one field per instance that has a connection of the user,
// valued with the time the claim expires, and a field with the version of the last transition. Each
// instance refreshes the claims of its users with heartbeats; the hash itself expires after twice the TTL,
// so a sweep still finds the claims of a crashed instance and announces those users offline. The index
// presence:index (user id scored by the latest expiry) is how the sweep finds them without scanning.
//
// The connection count per user is kept in the instance: only the first connection of a user on an
// instance writes to Redis, and only the last one to close removes the claim.
type Presence struct {
	rdb      Client
	prefix   string
	instance string
	ttl      time.Duration
	onChange func([]Change)
	log      *slog.Logger

	mu    sync.Mutex
	conns map[int64]int
}

// NewPresence returns the presence tracker of this instance.
func NewPresence(rdb Client, o PresenceOptions) (*Presence, error) {
	if o.Instance == "" {
		return nil, errors.New("presence: instance id is required")
	}
	if o.TTL <= 0 {
		o.TTL = DefaultPresenceTTL
	}
	if o.TTL < time.Second {
		return nil, errors.New("presence: ttl must be at least 1s")
	}
	if o.OnChange == nil {
		o.OnChange = func([]Change) {}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Presence{
		rdb: rdb, prefix: o.Prefix, instance: o.Instance, ttl: o.TTL,
		onChange: o.OnChange, log: o.Log, conns: make(map[int64]int),
	}, nil
}

func (p *Presence) key(userID int64) string {
	return p.prefix + "presence:" + strconv.FormatInt(userID, 10)
}

func (p *Presence) indexKey() string { return p.prefix + "presence:index" }

// Shared Lua: the clock, dropping expired claims and the version counter. Times come from Redis, so
// instances with skewed clocks agree on what has expired. Numbers are written with %.0f: Lua would turn
// a microsecond timestamp into a rounded 1.7e+15.
const presenceLib = `
local t = redis.call('TIME')
local now = t[1] * 1000 + math.floor(t[2] / 1000)
local now_us = t[1] * 1000000 + t[2]
local function purge(key)
  local f = redis.call('HGETALL', key)
  local live = 0
  for i = 1, #f, 2 do
    if string.sub(f[i], 1, 2) == 'i:' then
      if tonumber(f[i + 1]) <= now then redis.call('HDEL', key, f[i]) else live = live + 1 end
    end
  end
  return live
end
local function bump(key)
  local v = tonumber(redis.call('HGET', key, 'v') or '0')
  if v < now_us then v = now_us else v = v + 1 end
  redis.call('HSET', key, 'v', string.format('%.0f', v))
  return v
end
`

var (
	// KEYS[1] user hash; ARGV: instance, ttl ms. Claims the user for the instance.
	// Returns {came_online, version, expires_at_ms}.
	claimScript = goredis.NewScript(presenceLib + `
local ttl = tonumber(ARGV[2])
local live = purge(KEYS[1])
redis.call('HSET', KEYS[1], 'i:' .. ARGV[1], string.format('%.0f', now + ttl))
local v = 0
if live == 0 then v = bump(KEYS[1]) end
redis.call('PEXPIRE', KEYS[1], ttl * 2)
if live == 0 then return {1, v, now + ttl} end
return {0, 0, now + ttl}`)

	// KEYS[1] user hash; ARGV[1] instance to release, or "" to only drop expired claims. When no claim is
	// left the hash is deleted and the user is reported offline, exactly once. Returns {went_offline, version}.
	settleScript = goredis.NewScript(presenceLib + `
if redis.call('EXISTS', KEYS[1]) == 0 then return {0, 0} end
if ARGV[1] ~= '' then redis.call('HDEL', KEYS[1], 'i:' .. ARGV[1]) end
if purge(KEYS[1]) > 0 then return {0, 0} end
local v = bump(KEYS[1])
redis.call('DEL', KEYS[1])
return {1, v}`)

	// KEYS: user hashes. Returns 1 or 0 per key.
	onlineScript = goredis.NewScript(presenceLib + `
local out = {}
for i, key in ipairs(KEYS) do
  local f = redis.call('HGETALL', key)
  local on = 0
  for j = 1, #f, 2 do
    if string.sub(f[j], 1, 2) == 'i:' and tonumber(f[j + 1]) > now then on = 1 break end
  end
  out[i] = on
end
return out`)

	// KEYS[1] index; ARGV: max. Returns the users whose latest claim has expired.
	expiredScript = goredis.NewScript(presenceLib + `
return redis.call('ZRANGE', KEYS[1], '-inf', string.format('%.0f', now), 'BYSCORE', 'LIMIT', 0, tonumber(ARGV[1]))`)

	// KEYS[1] index; ARGV: user ids. Removes them unless a newer claim was indexed meanwhile.
	unindexScript = goredis.NewScript(presenceLib + `
for _, uid in ipairs(ARGV) do
  local s = redis.call('ZSCORE', KEYS[1], uid)
  if s and tonumber(s) <= now then redis.call('ZREM', KEYS[1], uid) end
end
return 1`)
)

func (p *Presence) ttlMS() int64 { return p.ttl.Milliseconds() }

// claim runs the claim script for the users in one round trip and indexes the claims in a second.
func (p *Presence) claim(ctx context.Context, userIDs []int64) ([]Change, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	cmds, err := pipelineScript(ctx, p.rdb, claimScript, len(userIDs), func(pipe goredis.Pipeliner, i int) *goredis.Cmd {
		return claimScript.EvalSha(ctx, pipe, []string{p.key(userIDs[i])}, p.instance, p.ttlMS())
	})
	if err != nil {
		return nil, fmt.Errorf("presence claim: %w", err)
	}
	var changes []Change
	index := p.rdb.Pipeline()
	for i, cmd := range cmds {
		res, err := cmd.Int64Slice()
		if err != nil || len(res) != 3 {
			return nil, fmt.Errorf("presence claim: unexpected reply")
		}
		if res[0] == 1 {
			changes = append(changes, Change{UserID: userIDs[i], Online: true, Version: res[1]})
		}
		// GT keeps the latest expiry when instances heartbeat out of order.
		index.ZAddGT(ctx, p.indexKey(), goredis.Z{Score: float64(res[2]), Member: userIDs[i]})
	}
	if _, err := index.Exec(ctx); err != nil {
		return changes, fmt.Errorf("presence index: %w", err)
	}
	return changes, nil
}

func (p *Presence) settle(ctx context.Context, userID int64, instance string) (Change, bool, error) {
	res, err := settleScript.Run(ctx, p.rdb, []string{p.key(userID)}, instance).Int64Slice()
	if err != nil || len(res) != 2 {
		return Change{}, false, fmt.Errorf("presence settle: %w", err)
	}
	if res[0] != 1 {
		return Change{}, false, nil
	}
	return Change{UserID: userID, Online: false, Version: res[1]}, true, nil
}

// Connect registers a new connection of the user on this instance. It returns the transition when the
// user was offline everywhere until now. The local count is kept even when Redis fails: the next heartbeat
// writes the claim and reports the transition through OnChange.
func (p *Presence) Connect(ctx context.Context, userID int64) (Change, bool, error) {
	p.mu.Lock()
	p.conns[userID]++
	first := p.conns[userID] == 1
	p.mu.Unlock()
	if !first {
		return Change{}, false, nil
	}
	changes, err := p.claim(ctx, []int64{userID})
	if len(changes) == 1 {
		return changes[0], true, err
	}
	return Change{}, false, err
}

// Disconnect unregisters a closed connection. When it was the user's last connection on this instance the
// claim is released, and the transition is returned when no other instance holds one. If Redis fails, the
// claim expires and a sweep announces the user offline.
func (p *Presence) Disconnect(ctx context.Context, userID int64) (Change, bool, error) {
	p.mu.Lock()
	n, ok := p.conns[userID]
	if !ok {
		p.mu.Unlock()
		return Change{}, false, nil
	}
	if n > 1 {
		p.conns[userID] = n - 1
		p.mu.Unlock()
		return Change{}, false, nil
	}
	delete(p.conns, userID)
	p.mu.Unlock()
	return p.settle(ctx, userID, p.instance)
}

// Local returns the users with a connection on this instance.
func (p *Presence) Local() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int64, 0, len(p.conns))
	for uid := range p.conns {
		out = append(out, uid)
	}
	return out
}

// Online reports for every user whether some instance holds a live claim.
func (p *Presence) Online(ctx context.Context, userIDs ...int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	keys := make([]string, len(userIDs))
	for i, uid := range userIDs {
		keys[i] = p.key(uid)
	}
	res, err := onlineScript.Run(ctx, p.rdb, keys).Int64Slice()
	if err != nil || len(res) != len(userIDs) {
		return nil, fmt.Errorf("presence online: %w", err)
	}
	for i, uid := range userIDs {
		out[uid] = res[i] == 1
	}
	return out, nil
}

// Heartbeat renews the claims of every local user. Users whose claim Redis had lost (a restart, a long
// pause) come online again, and those transitions are returned.
func (p *Presence) Heartbeat(ctx context.Context) ([]Change, error) {
	const batch = 1000
	users := p.Local()
	var all []Change
	for len(users) > 0 {
		n := min(batch, len(users))
		changes, err := p.claim(ctx, users[:n])
		all = append(all, changes...)
		if err != nil {
			return all, err
		}
		users = users[n:]
	}
	return all, nil
}

// Sweep announces the users whose claims have all expired, typically because the instance holding them
// crashed. Any number of instances may sweep at the same time; each transition is reported by one of them.
func (p *Presence) Sweep(ctx context.Context) ([]Change, error) {
	ids, err := expiredScript.Run(ctx, p.rdb, []string{p.indexKey()}, sweepBatch).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("presence sweep: %w", err)
	}
	var changes []Change
	args := make([]any, 0, len(ids))
	for _, s := range ids {
		uid, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			continue
		}
		c, ok, err := p.settle(ctx, uid, "")
		if err != nil {
			return changes, err
		}
		if ok {
			changes = append(changes, c)
		}
		args = append(args, s)
	}
	if len(args) > 0 {
		if err := unindexScript.Run(ctx, p.rdb, []string{p.indexKey()}, args...).Err(); err != nil {
			return changes, fmt.Errorf("presence sweep: %w", err)
		}
	}
	return changes, nil
}

// Run heartbeats every TTL/3 and sweeps every TTL/4 until ctx is done, reporting transitions to OnChange.
func (p *Presence) Run(ctx context.Context) {
	beat := time.NewTicker(p.ttl / 3)
	defer beat.Stop()
	sweep := time.NewTicker(p.ttl / 4)
	defer sweep.Stop()
	for {
		var (
			changes []Change
			err     error
			what    string
		)
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			what = "heartbeat"
			changes, err = p.Heartbeat(ctx)
		case <-sweep.C:
			what = "sweep"
			changes, err = p.Sweep(ctx)
		}
		if len(changes) > 0 {
			p.onChange(changes)
		}
		if err != nil && ctx.Err() == nil {
			p.log.WarnContext(ctx, "presence "+what+" failed", "error", err)
		}
	}
}
