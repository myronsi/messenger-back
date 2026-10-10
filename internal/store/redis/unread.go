package redis

import (
	"context"
	"fmt"
	"strconv"

	goredis "github.com/redis/go-redis/v9"
)

// Unread keeps the number of unread messages per user and chat in a hash unread:{user_id} with one field
// per chat. A new message increments the counter of every member except the sender; reading resets it.
//
// The hash has no expiry and is not the source of truth: it carries a marker field once it was built, and a
// user without the marker (a new instance of Redis, an evicted key) gets counts rebuilt from the message
// store. Increments for a user whose hash is not built are skipped, because the rebuild counts those
// messages anyway.
type Unread struct {
	rdb    Client
	prefix string
}

// NewUnread returns the counters under the prefix ("" in production).
func NewUnread(rdb Client, prefix string) *Unread { return &Unread{rdb: rdb, prefix: prefix} }

// builtField marks a hash that holds the counts of every chat of the user.
const builtField = "_built"

func (u *Unread) key(userID int64) string {
	return u.prefix + "unread:" + strconv.FormatInt(userID, 10)
}

var (
	// KEYS[1] hash; ARGV: chat field, by. Increments only a built hash.
	incrScript = goredis.NewScript(`
if redis.call('HEXISTS', KEYS[1], '_built') == 0 then return 0 end
redis.call('HINCRBY', KEYS[1], ARGV[1], tonumber(ARGV[2]))
return 1`)

	// KEYS[1] hash; ARGV: chat, count, chat, count, ... Replaces the hash unless another rebuild finished
	// first (then its counts are at least as fresh). Returns 1 when written.
	loadScript = goredis.NewScript(`
if redis.call('HEXISTS', KEYS[1], '_built') == 1 then return 0 end
redis.call('DEL', KEYS[1])
redis.call('HSET', KEYS[1], '_built', 1)
for i = 1, #ARGV, 2 do
  if tonumber(ARGV[i + 1]) > 0 then redis.call('HSET', KEYS[1], ARGV[i], ARGV[i + 1]) end
end
return 1`)
)

// Increment adds one unread message in the chat for every user (call it with the members except the
// sender), in one round trip.
func (u *Unread) Increment(ctx context.Context, chatID int64, userIDs ...int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	field := strconv.FormatInt(chatID, 10)
	_, err := pipelineScript(ctx, u.rdb, incrScript, len(userIDs), func(pipe goredis.Pipeliner, i int) *goredis.Cmd {
		return incrScript.EvalSha(ctx, pipe, []string{u.key(userIDs[i])}, field, 1)
	})
	if err != nil {
		return fmt.Errorf("unread increment: %w", err)
	}
	return nil
}

// Set stores the count of one chat, for example after the user read up to a message that is not the
// latest. A hash that is not built stays unbuilt.
func (u *Unread) Set(ctx context.Context, userID, chatID, count int64) error {
	field := strconv.FormatInt(chatID, 10)
	var err error
	if count <= 0 {
		err = u.rdb.HDel(ctx, u.key(userID), field).Err()
	} else {
		// Only a built hash: a lone field would make an unbuilt hash look like it had data.
		err = setIfBuiltScript.Run(ctx, u.rdb, []string{u.key(userID)}, field, count).Err()
	}
	if err != nil {
		return fmt.Errorf("unread set: %w", err)
	}
	return nil
}

var setIfBuiltScript = goredis.NewScript(`
if redis.call('HEXISTS', KEYS[1], '_built') == 0 then return 0 end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
return 1`)

// Reset marks the chat as read for the user.
func (u *Unread) Reset(ctx context.Context, userID, chatID int64) error {
	return u.Set(ctx, userID, chatID, 0)
}

// Forget removes the chat from the counters of the users (the chat was deleted or they left it).
func (u *Unread) Forget(ctx context.Context, chatID int64, userIDs ...int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	field := strconv.FormatInt(chatID, 10)
	pipe := u.rdb.Pipeline()
	for _, uid := range userIDs {
		pipe.HDel(ctx, u.key(uid), field)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("unread forget: %w", err)
	}
	return nil
}

// Drop removes all counters of the user (the account is gone).
func (u *Unread) Drop(ctx context.Context, userID int64) error {
	if err := u.rdb.Del(ctx, u.key(userID)).Err(); err != nil {
		return fmt.Errorf("unread drop: %w", err)
	}
	return nil
}

// Get returns the counts of the user's chats that have unread messages. built is false when the hash has to
// be rebuilt (see Counts).
func (u *Unread) Get(ctx context.Context, userID int64) (counts map[int64]int64, built bool, err error) {
	m, err := u.rdb.HGetAll(ctx, u.key(userID)).Result()
	if err != nil {
		return nil, false, fmt.Errorf("unread get: %w", err)
	}
	if _, ok := m[builtField]; !ok {
		return nil, false, nil
	}
	counts = make(map[int64]int64, len(m)-1)
	for f, v := range m {
		chat, err1 := strconv.ParseInt(f, 10, 64)
		n, err2 := strconv.ParseInt(v, 10, 64)
		if err1 != nil || err2 != nil || n <= 0 {
			continue
		}
		counts[chat] = n
	}
	return counts, true, nil
}

// Load stores counts that were computed from the message store, unless a concurrent rebuild got there
// first.
func (u *Unread) Load(ctx context.Context, userID int64, counts map[int64]int64) error {
	args := make([]any, 0, 2*len(counts))
	for chat, n := range counts {
		args = append(args, strconv.FormatInt(chat, 10), n)
	}
	if err := loadScript.Run(ctx, u.rdb, []string{u.key(userID)}, args...).Err(); err != nil {
		return fmt.Errorf("unread load: %w", err)
	}
	return nil
}

// Counts returns the user's counts, rebuilding them with rebuild when the hash is missing. A message that
// arrives between rebuild and Load can be missed until the next read of that chat resets its counter;
// that is the price of not locking.
func (u *Unread) Counts(ctx context.Context, userID int64, rebuild func(context.Context) (map[int64]int64, error)) (map[int64]int64, error) {
	counts, built, err := u.Get(ctx, userID)
	if err != nil || built {
		return counts, err
	}
	counts, err = rebuild(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.Load(ctx, userID, counts); err != nil {
		return counts, err
	}
	return counts, nil
}
