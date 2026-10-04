package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// Redis is what the auth service needs from a Redis client.
type Redis interface {
	goredis.Cmdable
	goredis.Scripter
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ---------------------------------------------------------------- two-factor login challenges

const (
	// ChallengeTTL is how long the user has to enter the second factor after the password.
	ChallengeTTL = 5 * time.Minute
	// MaxChallengeAttempts is how many codes may be tried against one challenge.
	MaxChallengeAttempts = 5
)

// ErrChallengeInvalid is returned for an unknown, expired, used or exhausted challenge.
var ErrChallengeInvalid = errors.New("invalid login challenge")

// Challenges stores the pending second step of a login on the server. Unlike a signed token, a challenge
// can be limited in attempts and is single use.
type Challenges struct {
	rdb Redis
	ns  string
}

// NewChallenges returns a challenge store under the namespace.
func NewChallenges(rdb Redis, ns string) *Challenges { return &Challenges{rdb: rdb, ns: ns} }

func (c *Challenges) key(token string) string { return c.ns + ":2fa:" + digest(token) }

// Create stores a challenge for the user and returns the opaque value the client sends back.
func (c *Challenges) Create(ctx context.Context, userID int64) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	k := c.key(token)
	pipe := c.rdb.TxPipeline()
	pipe.HSet(ctx, k, "uid", userID, "attempts", 0)
	pipe.Expire(ctx, k, ChallengeTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("store login challenge: %w", err)
	}
	return token, nil
}

// KEYS[1] challenge; ARGV[1] max attempts. Returns the user id, or -1 when missing or exhausted (the
// challenge is deleted once the limit is passed).
var attemptScript = goredis.NewScript(`
local uid = redis.call('HGET', KEYS[1], 'uid')
if not uid then return -1 end
local n = redis.call('HINCRBY', KEYS[1], 'attempts', 1)
if n > tonumber(ARGV[1]) then
  redis.call('DEL', KEYS[1])
  return -1
end
return tonumber(uid)`)

// Attempt counts one try against the challenge and returns the user it belongs to. After
// MaxChallengeAttempts tries the challenge is gone.
func (c *Challenges) Attempt(ctx context.Context, token string) (int64, error) {
	uid, err := attemptScript.Run(ctx, c.rdb, []string{c.key(token)}, MaxChallengeAttempts).Int64()
	if err != nil {
		return 0, fmt.Errorf("login challenge: %w", err)
	}
	if uid < 0 {
		return 0, ErrChallengeInvalid
	}
	return uid, nil
}

// Consume ends the challenge after a successful second step. Only one caller wins.
func (c *Challenges) Consume(ctx context.Context, token string) error {
	n, err := c.rdb.Del(ctx, c.key(token)).Result()
	if err != nil {
		return fmt.Errorf("login challenge: %w", err)
	}
	if n == 0 {
		return ErrChallengeInvalid
	}
	return nil
}

// ---------------------------------------------------------------- one-time TOTP codes

// TOTPReplay refuses a TOTP code that was already used, so a code observed over a shoulder or in a log is
// worthless once the owner has logged in with it.
type TOTPReplay struct {
	rdb Redis
	ns  string
}

// NewTOTPReplay returns the replay guard under the namespace.
func NewTOTPReplay(rdb Redis, ns string) *TOTPReplay { return &TOTPReplay{rdb: rdb, ns: ns} }

// Claim marks the step of the user as used; false when it was used before.
func (t *TOTPReplay) Claim(ctx context.Context, userID, step int64) (bool, error) {
	ok, err := t.rdb.SetNX(ctx, fmt.Sprintf("%s:totp:%d:%d", t.ns, userID, step), 1, 3*time.Duration(totpPeriod)*time.Second).Result()
	if err != nil {
		return false, fmt.Errorf("totp replay guard: %w", err)
	}
	return ok, nil
}

// ---------------------------------------------------------------- session cache

// SessionCache keeps the verdict "this session is active" for a short time, so an authenticated request
// does not hit PostgreSQL. Revoking writes a tombstone, which a concurrent cache fill cannot overwrite.
type SessionCache struct {
	rdb Redis
	ns  string
	ttl time.Duration
	// touchEvery limits how often last_active_at is written per session.
	touchEvery time.Duration
}

// NewSessionCache returns a cache whose entries live for ttl (at most).
func NewSessionCache(rdb Redis, ns string, ttl time.Duration) *SessionCache {
	return &SessionCache{rdb: rdb, ns: ns, ttl: ttl, touchEvery: 5 * time.Minute}
}

const revokedMarker = "x"

func (c *SessionCache) key(id uuid.UUID) string { return c.ns + ":sess:" + id.String() }

// CachedSession is a cache hit.
type CachedSession struct {
	UserID  int64
	Revoked bool
	Expires time.Time
}

// Get returns the cached verdict. ok is false on a miss; errors mean the caller should ask the database.
func (c *SessionCache) Get(ctx context.Context, id uuid.UUID) (CachedSession, bool, error) {
	v, err := c.rdb.Get(ctx, c.key(id)).Result()
	if errors.Is(err, goredis.Nil) {
		return CachedSession{}, false, nil
	}
	if err != nil {
		return CachedSession{}, false, err
	}
	if v == revokedMarker {
		return CachedSession{Revoked: true}, true, nil
	}
	uid, exp, found := strings.Cut(v, ":")
	u, err1 := strconv.ParseInt(uid, 10, 64)
	e, err2 := strconv.ParseInt(exp, 10, 64)
	if !found || err1 != nil || err2 != nil {
		return CachedSession{}, false, nil
	}
	return CachedSession{UserID: u, Expires: time.Unix(e, 0)}, true, nil
}

// Put caches an active session, unless a tombstone or a newer entry is there.
func (c *SessionCache) Put(ctx context.Context, id uuid.UUID, userID int64, expires time.Time) error {
	ttl := min(c.ttl, time.Until(expires))
	if ttl <= 0 {
		return nil
	}
	return c.rdb.SetNX(ctx, c.key(id), fmt.Sprintf("%d:%d", userID, expires.Unix()), ttl).Err()
}

// Revoke makes the session count as revoked for everybody for the cache lifetime.
func (c *SessionCache) Revoke(ctx context.Context, ids ...uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	pipe := c.rdb.Pipeline()
	for _, id := range ids {
		pipe.Set(ctx, c.key(id), revokedMarker, c.ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// ShouldTouch reports true at most once per touchEvery for a session.
func (c *SessionCache) ShouldTouch(ctx context.Context, id uuid.UUID) bool {
	ok, err := c.rdb.SetNX(ctx, c.ns+":touch:"+id.String(), 1, c.touchEvery).Result()
	return err == nil && ok
}

// ---------------------------------------------------------------- WebSocket tickets

// TicketTTL is how long a WebSocket ticket can be redeemed.
const TicketTTL = 30 * time.Second

// Ticket identifies the session a WebSocket connection is opened for.
type Ticket struct {
	UserID    int64
	SessionID uuid.UUID
}

// Tickets are one-time, 30-second credentials for the WebSocket handshake, so no access token travels in a
// URL (and with it into logs and proxies).
type Tickets struct {
	rdb Redis
	ns  string
}

// NewTickets returns a ticket store under the namespace.
func NewTickets(rdb Redis, ns string) *Tickets { return &Tickets{rdb: rdb, ns: ns} }

func (t *Tickets) key(ticket string) string { return t.ns + ":wsticket:" + digest(ticket) }

// Issue creates a ticket for the session.
func (t *Tickets) Issue(ctx context.Context, tk Ticket) (string, error) {
	ticket, err := randomToken(32)
	if err != nil {
		return "", err
	}
	if err := t.rdb.Set(ctx, t.key(ticket), fmt.Sprintf("%d:%s", tk.UserID, tk.SessionID), TicketTTL).Err(); err != nil {
		return "", fmt.Errorf("store ticket: %w", err)
	}
	return ticket, nil
}

// ErrTicketInvalid is returned for an unknown, expired or already used ticket.
var ErrTicketInvalid = errors.New("invalid ticket")

// Redeem consumes the ticket. A second call with the same ticket fails.
func (t *Tickets) Redeem(ctx context.Context, ticket string) (Ticket, error) {
	v, err := t.rdb.GetDel(ctx, t.key(ticket)).Result()
	if errors.Is(err, goredis.Nil) {
		return Ticket{}, ErrTicketInvalid
	}
	if err != nil {
		return Ticket{}, fmt.Errorf("redeem ticket: %w", err)
	}
	uid, sid, found := strings.Cut(v, ":")
	u, err1 := strconv.ParseInt(uid, 10, 64)
	s, err2 := uuid.Parse(sid)
	if !found || err1 != nil || err2 != nil {
		return Ticket{}, ErrTicketInvalid
	}
	return Ticket{UserID: u, SessionID: s}, nil
}
