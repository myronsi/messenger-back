package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Rate is a GCRA limit: on average Rate events per Period, with bursts of up to Burst events at once.
type Rate struct {
	// Name labels the action in the key: rl:{name}:{subject}.
	Name   string
	Rate   int
	Period time.Duration
	Burst  int
}

// interval is the time one event uses up.
func (r Rate) interval() time.Duration { return r.Period / time.Duration(r.Rate) }

// Result is the verdict of the limiter.
type Result struct {
	Allowed bool
	// Remaining is how many more events would be allowed right now.
	Remaining int
	// RetryAfter is how long to wait until the event would be allowed (0 when allowed).
	RetryAfter time.Duration
}

// RateLimiter is a GCRA (generic cell rate algorithm) limiter for frequent actions such as sending messages
// or typing indicators: one key per subject holding a single timestamp, so it costs the same at any rate.
// The authentication limits, which count failures in a sliding window, are in the auth package.
type RateLimiter struct {
	rdb    Client
	prefix string
}

// NewRateLimiter returns a limiter that keeps its keys under the prefix ("" in production).
func NewRateLimiter(rdb Client, prefix string) *RateLimiter {
	return &RateLimiter{rdb: rdb, prefix: prefix}
}

// KEYS[1] key; ARGV: interval us, burst, cost. Returns {allowed, remaining, retry_after_us}. The clock is
// Redis's, in microseconds; the theoretical arrival time is stored with %.0f to keep it exact.
var gcraScript = goredis.NewScript(`
local t = redis.call('TIME')
local now = t[1] * 1000000 + t[2]
local interval = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])
local tolerance = interval * burst
local tat = tonumber(redis.call('GET', KEYS[1]) or '0')
if tat < now then tat = now end
local new_tat = tat + interval * cost
local allow_at = new_tat - tolerance
if allow_at > now then
  local remaining = math.floor((now - (tat - tolerance)) / interval)
  return {0, math.max(remaining, 0), allow_at - now}
end
redis.call('SET', KEYS[1], string.format('%.0f', new_tat), 'PX', math.ceil((new_tat - now) / 1000))
return {1, math.floor((now - allow_at) / interval), 0}`)

func (l *RateLimiter) key(r Rate, subject string) string {
	// Subjects can be IPs or usernames, which are personal data; only a digest reaches Redis.
	return l.prefix + "rl:" + r.Name + ":" + digest(subject)
}

// Allow counts one event for the subject when it is within the rate.
func (l *RateLimiter) Allow(ctx context.Context, r Rate, subject string) (Result, error) {
	return l.AllowN(ctx, r, subject, 1)
}

// AllowN counts n events at once when all of them are within the rate; otherwise nothing is counted.
func (l *RateLimiter) AllowN(ctx context.Context, r Rate, subject string, n int) (Result, error) {
	// The script works in microseconds, so an event must use up at least one.
	if r.Rate <= 0 || r.Period <= 0 || r.Burst <= 0 || n <= 0 || r.interval() < time.Microsecond {
		return Result{}, errors.New("rate limiter: invalid rate")
	}
	res, err := gcraScript.Run(ctx, l.rdb, []string{l.key(r, subject)}, r.interval().Microseconds(), r.Burst, n).Int64Slice()
	if err != nil || len(res) != 3 {
		return Result{}, fmt.Errorf("rate limiter: %w", err)
	}
	return Result{
		Allowed:    res[0] == 1,
		Remaining:  int(res[1]),
		RetryAfter: time.Duration(res[2]) * time.Microsecond,
	}, nil
}
