package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
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
	// KEYS: user hash, index; ARGV: instance, ttl ms, user id. Claims the user for the instance and indexes
	// the claim in the same step, so a claim the sweep cannot find never exists. GT keeps the latest expiry
	// when instances heartbeat out of order. Returns {came_online, version}.
	claimScript = goredis.NewScript(presenceLib + `
local ttl = tonumber(ARGV[2])
local live = purge(KEYS[1])
local expires = string.format('%.0f', now + ttl)
redis.call('HSET', KEYS[1], 'i:' .. ARGV[1], expires)
redis.call('PEXPIRE', KEYS[1], ttl * 2)
redis.call('ZADD', KEYS[2], 'GT', expires, ARGV[3])
if live == 0 then return {1, bump(KEYS[1])} end
return {0, 0}`)

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

// claim claims the users for this instance in one round trip and returns those who came online.
func (p *Presence) claim(ctx context.Context, userIDs []int64) ([]Change, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	cmds, err := pipelineScript(ctx, p.rdb, claimScript, len(userIDs), func(pipe goredis.Pipeliner, i int) *goredis.Cmd {
		return claimScript.EvalSha(ctx, pipe, []string{p.key(userIDs[i]), p.indexKey()}, p.instance, p.ttlMS(), userIDs[i])
	})
	if err != nil {
		return nil, fmt.Errorf("presence claim: %w", err)
	}
	return transitions(cmds, userIDs, true)
}

// settle releases this instance's claims of the users (or, with instance "", only drops expired claims) in
// one round trip and returns those who went offline.
func (p *Presence) settle(ctx context.Context, userIDs []int64, instance string) ([]Change, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	cmds, err := pipelineScript(ctx, p.rdb, settleScript, len(userIDs), func(pipe goredis.Pipeliner, i int) *goredis.Cmd {
		return settleScript.EvalSha(ctx, pipe, []string{p.key(userIDs[i])}, instance)
	})
	if err != nil {
		return nil, fmt.Errorf("presence settle: %w", err)
	}
	return transitions(cmds, userIDs, false)
}

// transitions reads the {changed, version} replies of the claim and settle scripts.
func transitions(cmds []*goredis.Cmd, userIDs []int64, online bool) ([]Change, error) {
	var changes []Change
	for i, cmd := range cmds {
		res, err := cmd.Int64Slice()
		if err != nil || len(res) != 2 {
			return nil, errors.New("presence: unexpected reply")
		}
		if res[0] == 1 {
			changes = append(changes, Change{UserID: userIDs[i], Online: online, Version: res[1]})
		}
	}
	return changes, nil
}

func one(changes []Change, err error) (Change, bool, error) {
	if len(changes) == 1 {
		return changes[0], true, err
	}
	return Change{}, false, err
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
	return one(p.claim(ctx, []int64{userID}))
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
	return one(p.settle(ctx, []int64{userID}, p.instance))
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
//
// The list of local users is a snapshot: a user whose last connection closes meanwhile may have released
// the claim before the heartbeat writes it again. Such claims are released once more afterwards, and their
// transitions are not reported (the Disconnect reported the user offline already).
func (p *Presence) Heartbeat(ctx context.Context) ([]Change, error) {
	const batch = 1000
	users := p.Local()
	var all []Change
	for len(users) > 0 {
		n := min(batch, len(users))
		chunk := users[:n]
		users = users[n:]
		changes, err := p.claim(ctx, chunk)
		gone := p.notLocal(chunk)
		if len(gone) > 0 {
			changes = slices.DeleteFunc(changes, func(c Change) bool { return gone[c.UserID] })
			if _, serr := p.settle(ctx, slices.Collect(maps.Keys(gone)), p.instance); serr != nil && err == nil {
				err = serr
			}
		}
		all = append(all, changes...)
		if err != nil {
			return all, err
		}
	}
	return all, nil
}

func (p *Presence) notLocal(userIDs []int64) map[int64]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	var gone map[int64]bool
	for _, uid := range userIDs {
		if _, ok := p.conns[uid]; !ok {
			if gone == nil {
				gone = make(map[int64]bool)
			}
			gone[uid] = true
		}
	}
	return gone
}

// sweepBudget bounds one Sweep, so a sweep after a large crash cannot run into the next one.
func (p *Presence) sweepBudget() time.Duration { return p.ttl / 8 }

// Sweep announces the users whose claims have all expired, typically because the instance holding them
// crashed. It works in batches until none is left or its time budget is used up. Any number of instances
// may sweep at the same time; each transition is reported by one of them.
func (p *Presence) Sweep(ctx context.Context) ([]Change, error) {
	deadline := time.Now().Add(p.sweepBudget())
	var all []Change
	for {
		changes, n, err := p.sweepBatch(ctx)
		all = append(all, changes...)
		if err != nil || n < sweepBatch || time.Now().After(deadline) {
			return all, err
		}
	}
}

func (p *Presence) sweepBatch(ctx context.Context) ([]Change, int, error) {
	ids, err := expiredScript.Run(ctx, p.rdb, []string{p.indexKey()}, sweepBatch).StringSlice()
	if err != nil {
		return nil, 0, fmt.Errorf("presence sweep: %w", err)
	}
	users := make([]int64, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, s := range ids {
		if uid, err := strconv.ParseInt(s, 10, 64); err == nil {
			users = append(users, uid)
		}
		args = append(args, s)
	}
	changes, err := p.settle(ctx, users, "")
	if err != nil {
		return nil, len(ids), err
	}
	if len(args) > 0 {
		if err := unindexScript.Run(ctx, p.rdb, []string{p.indexKey()}, args...).Err(); err != nil {
			return changes, len(ids), fmt.Errorf("presence sweep: %w", err)
		}
	}
	return changes, len(ids), nil
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
