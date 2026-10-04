package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// Rule is a sliding-window limit: at most Limit events in any Window.
type Rule struct {
	Name   string
	Limit  int
	Window time.Duration
}

// The limits of the Python backend, now enforced in Redis so they hold across API instances.
// Login, two-factor and recovery count failures; refresh and ticket requests count every request.
var (
	RuleLoginIP    = Rule{"login_ip", 20, 15 * time.Minute}
	RuleLoginUser  = Rule{"login_user", 8, 15 * time.Minute}
	RuleTwoFAIP    = Rule{"2fa_ip", 20, 15 * time.Minute}
	RuleTwoFAUser  = Rule{"2fa_user", 10, 15 * time.Minute}
	RuleRecoverIP  = Rule{"recover_ip", 10, time.Hour}
	RuleRecoverUsr = Rule{"recover_user", 5, time.Hour}
	RuleResetIP    = Rule{"reset_ip", 20, time.Hour}
	RuleRefreshIP  = Rule{"refresh_ip", 300, 15 * time.Minute}
	RuleRegisterIP = Rule{"register_ip", 20, time.Hour}
	RulePasswordU  = Rule{"password_user", 10, 15 * time.Minute}
	RuleTicketUser = Rule{"ticket_user", 20, time.Minute}
)

// Limiter is a Redis sliding-window rate limiter (a sorted set per key, trimmed atomically in Lua). Callers
// treat a Redis error as "deny": a limiter that fails open is no protection against password guessing.
type Limiter struct {
	rdb Redis
	ns  string
}

// NewLimiter returns a limiter that keeps its keys under the namespace.
func NewLimiter(rdb Redis, ns string) *Limiter { return &Limiter{rdb: rdb, ns: ns} }

// Both scripts read the clock from Redis, so instances with skewed clocks agree on the window.
var (
	// KEYS[1] key; ARGV: window ms, limit, add (1 to record the event when allowed), member.
	// Returns {allowed, retry_after_ms}.
	checkScript = goredis.NewScript(`
local t = redis.call('TIME')
local now = t[1] * 1000 + math.floor(t[2] / 1000)
local window = tonumber(ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - window)
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[2]) then
  local oldest = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
  return {0, tonumber(oldest[2]) + window - now}
end
if ARGV[3] == '1' then
  redis.call('ZADD', KEYS[1], now, ARGV[4])
  redis.call('PEXPIRE', KEYS[1], window)
end
return {1, 0}`)

	// KEYS[1] key; ARGV: window ms, member.
	hitScript = goredis.NewScript(`
local t = redis.call('TIME')
local now = t[1] * 1000 + math.floor(t[2] / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - tonumber(ARGV[1]))
redis.call('ZADD', KEYS[1], now, ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[1])
return 1`)
)

func (l *Limiter) key(r Rule, subject string) string {
	// Usernames and IPs are personal data; only a digest reaches Redis.
	sum := sha256.Sum256([]byte(subject))
	return fmt.Sprintf("%s:rl:%s:%s", l.ns, r.Name, hex.EncodeToString(sum[:16]))
}

func (l *Limiter) run(ctx context.Context, r Rule, subject string, add bool) (member string, allowed bool, retry time.Duration, err error) {
	flag, member := "0", uuid.NewString()
	if add {
		flag = "1"
	}
	res, err := checkScript.Run(ctx, l.rdb, []string{l.key(r, subject)}, r.Window.Milliseconds(), r.Limit, flag, member).Int64Slice()
	if err != nil || len(res) != 2 {
		return "", false, 0, fmt.Errorf("rate limiter: %w", err)
	}
	return member, res[0] == 1, time.Duration(res[1]) * time.Millisecond, nil
}

// Check reports whether the subject is under the limit, without counting anything.
func (l *Limiter) Check(ctx context.Context, r Rule, subject string) (allowed bool, retryAfter time.Duration, err error) {
	_, allowed, retryAfter, err = l.run(ctx, r, subject, false)
	return allowed, retryAfter, err
}

// Take counts one event if the subject is under the limit (atomically) and reports whether it was.
func (l *Limiter) Take(ctx context.Context, r Rule, subject string) (allowed bool, retryAfter time.Duration, err error) {
	_, allowed, retryAfter, err = l.run(ctx, r, subject, true)
	return allowed, retryAfter, err
}

// Reserve counts one event if the subject is under the limit, in one atomic step, and returns a token for
// Release. Failure-counting callers reserve before they verify a secret, so concurrent guesses cannot all
// pass the check before the first failure is recorded; a correct guess gives the event back.
func (l *Limiter) Reserve(ctx context.Context, r Rule, subject string) (member string, allowed bool, retryAfter time.Duration, err error) {
	return l.run(ctx, r, subject, true)
}

// Release gives a reserved event back.
func (l *Limiter) Release(ctx context.Context, r Rule, subject, member string) error {
	if err := l.rdb.ZRem(ctx, l.key(r, subject), member).Err(); err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}
	return nil
}

// Hit records one event unconditionally, typically a failed attempt.
func (l *Limiter) Hit(ctx context.Context, r Rule, subject string) error {
	if err := hitScript.Run(ctx, l.rdb, []string{l.key(r, subject)}, r.Window.Milliseconds(), uuid.NewString()).Err(); err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}
	return nil
}

// Reset forgets the events of a subject, e.g. after a successful login.
func (l *Limiter) Reset(ctx context.Context, r Rule, subject string) error {
	return l.rdb.Del(ctx, l.key(r, subject)).Err()
}
