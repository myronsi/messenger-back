package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// DefaultMembersTTL is how long the member list of a chat is cached.
const DefaultMembersTTL = 10 * time.Minute

// Members caches who is in a chat: a set members:{chat_id} of user ids, so the gateway can authorize every
// action and fan out every message without asking PostgreSQL.
//
// Every change of a membership must call Invalidate after the change is committed. A fill that loaded the
// members before such a change cannot overwrite the invalidation: each chat has a version members:{chat_id}:v
// that Invalidate increments, and a fill only writes when the version is still the one it saw before loading.
type Members struct {
	rdb    Client
	prefix string
	ttl    time.Duration
}

// NewMembers returns the cache under the prefix ("" in production); ttl 0 means DefaultMembersTTL.
func NewMembers(rdb Client, prefix string, ttl time.Duration) *Members {
	if ttl <= 0 {
		ttl = DefaultMembersTTL
	}
	return &Members{rdb: rdb, prefix: prefix, ttl: ttl}
}

// LoadMembers reads the members of a chat from the source of truth.
type LoadMembers func(ctx context.Context, chatID int64) ([]int64, error)

// loadedMarker is in every cached set, so an empty chat is a cache hit too (user ids are positive).
const loadedMarker = "0"

// versionTTL outlives every fill. When the version key expires during a fill, the fill is refused, which is
// safe.
const versionTTL = 24 * time.Hour

func (m *Members) key(chatID int64) string {
	return m.prefix + "members:" + strconv.FormatInt(chatID, 10)
}

func (m *Members) versionKey(chatID int64) string { return m.key(chatID) + ":v" }

var (
	// KEYS: set, version; ARGV: version seen before loading ("" for none), ttl ms, members...
	fillScript = goredis.NewScript(`
local v = redis.call('GET', KEYS[2]) or ''
if v ~= ARGV[1] then return 0 end
redis.call('DEL', KEYS[1])
redis.call('SADD', KEYS[1], unpack(ARGV, 3))
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1`)

	// KEYS: set, version; ARGV: version ttl ms.
	invalidateScript = goredis.NewScript(`
redis.call('INCR', KEYS[2])
redis.call('PEXPIRE', KEYS[2], ARGV[1])
redis.call('DEL', KEYS[1])
return 1`)
)

// Get returns the members of the chat, from the cache or through load.
func (m *Members) Get(ctx context.Context, chatID int64, load LoadMembers) ([]int64, error) {
	vals, err := m.rdb.SMembers(ctx, m.key(chatID)).Result()
	if err != nil {
		return nil, fmt.Errorf("members get: %w", err)
	}
	if len(vals) > 0 {
		out := make([]int64, 0, len(vals)-1)
		for _, v := range vals {
			if uid, err := strconv.ParseInt(v, 10, 64); err == nil && uid > 0 {
				out = append(out, uid)
			}
		}
		return out, nil
	}
	return m.fill(ctx, chatID, load)
}

// ErrMembershipChanging is returned when the members of a chat changed during every attempt to load them;
// callers treat it like any other failure and refuse the action.
var ErrMembershipChanging = errors.New("members: membership kept changing while loading")

// fillAttempts bounds the reloads when invalidations keep racing a fill.
const fillAttempts = 3

// fill loads the members and caches them. A refused fill means the membership changed after the version
// was read, possibly after load read the members, so that list may be stale and is loaded again.
func (m *Members) fill(ctx context.Context, chatID int64, load LoadMembers) ([]int64, error) {
	for range fillAttempts {
		seen, err := m.rdb.Get(ctx, m.versionKey(chatID)).Result()
		if err != nil && !errors.Is(err, goredis.Nil) {
			return nil, fmt.Errorf("members get: %w", err)
		}
		members, err := load(ctx, chatID)
		if err != nil {
			return nil, err
		}
		args := make([]any, 0, len(members)+3)
		args = append(args, seen, m.ttl.Milliseconds(), loadedMarker)
		for _, uid := range members {
			args = append(args, uid)
		}
		stored, err := fillScript.Run(ctx, m.rdb, []string{m.key(chatID), m.versionKey(chatID)}, args...).Int()
		if err != nil {
			return nil, fmt.Errorf("members fill: %w", err)
		}
		if stored == 1 {
			return members, nil
		}
	}
	return nil, ErrMembershipChanging
}

// IsMember reports whether the user is in the chat.
func (m *Members) IsMember(ctx context.Context, chatID, userID int64, load LoadMembers) (bool, error) {
	res, err := m.rdb.SMIsMember(ctx, m.key(chatID), loadedMarker, userID).Result()
	if err != nil {
		return false, fmt.Errorf("members check: %w", err)
	}
	if len(res) == 2 && res[0] {
		return res[1], nil
	}
	members, err := m.fill(ctx, chatID, load)
	if err != nil {
		return false, err
	}
	for _, uid := range members {
		if uid == userID {
			return true, nil
		}
	}
	return false, nil
}

// Invalidate drops the cached members of the chats. Call it after every committed membership change
// (member added, removed or left, chat deleted, account deleted).
func (m *Members) Invalidate(ctx context.Context, chatIDs ...int64) error {
	if len(chatIDs) == 0 {
		return nil
	}
	_, err := pipelineScript(ctx, m.rdb, invalidateScript, len(chatIDs), func(pipe goredis.Pipeliner, i int) *goredis.Cmd {
		return invalidateScript.EvalSha(ctx, pipe, []string{m.key(chatIDs[i]), m.versionKey(chatIDs[i])}, versionTTL.Milliseconds())
	})
	if err != nil {
		return fmt.Errorf("members invalidate: %w", err)
	}
	return nil
}
