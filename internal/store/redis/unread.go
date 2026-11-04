package redis

import (
	"context"
	"fmt"
	"strconv"
	"time"

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

func (u *Unread) idsKey(userID, chatID int64) string {
	return u.key(userID) + ":" + strconv.FormatInt(chatID, 10)
}

func paddedID(id int64) string { return fmt.Sprintf("%020d", id) }

const (
	unreadIDsTTL = 7 * 24 * time.Hour
	unreadIDsCap = 1000
)

var (
	incrScript = goredis.NewScript(`
if redis.call('HEXISTS', KEYS[1], '_built') == 0 then return 0 end
local read = redis.call('HGET', KEYS[1], ARGV[1] .. ':read')
if read and ARGV[2] <= read then return 0 end
if redis.call('ZADD', KEYS[2], 'NX', 0, ARGV[2]) == 0 then return 0 end
redis.call('PEXPIRE', KEYS[2], ARGV[3])
redis.call('ZREMRANGEBYRANK', KEYS[2], 0, -(tonumber(ARGV[4]) + 1))
redis.call('HINCRBY', KEYS[1], ARGV[1], 1)
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
func (u *Unread) Increment(ctx context.Context, chatID, messageID int64, userIDs ...int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	field, id := strconv.FormatInt(chatID, 10), paddedID(messageID)
	_, err := pipelineScript(ctx, u.rdb, incrScript, len(userIDs), func(pipe goredis.Pipeliner, i int) *goredis.Cmd {
		return incrScript.EvalSha(ctx, pipe, []string{u.key(userIDs[i]), u.idsKey(userIDs[i], chatID)}, field, id, unreadIDsTTL.Milliseconds(), unreadIDsCap)
	})
	if err != nil {
		return fmt.Errorf("unread increment: %w", err)
	}
	return nil
}

// Set stores the count of one chat, for example after the user read up to a message that is not the
// latest. A hash that is not built stays unbuilt.
func (u *Unread) Set(ctx context.Context, userID, chatID, readUpTo int64, unread []int64) error {
	args := make([]any, 0, 4+len(unread))
	args = append(args, strconv.FormatInt(chatID, 10), paddedID(readUpTo), unreadIDsTTL.Milliseconds(), unreadIDsCap)
	for _, id := range unread {
		if id > readUpTo {
			args = append(args, paddedID(id))
		}
	}
	if err := setScript.Run(ctx, u.rdb, []string{u.key(userID), u.idsKey(userID, chatID)}, args...).Err(); err != nil {
		return fmt.Errorf("unread set: %w", err)
	}
	return nil
}

var setScript = goredis.NewScript(`
if redis.call('HEXISTS', KEYS[1], '_built') == 0 then
  redis.call('HDEL', KEYS[1], ARGV[1])
  return 0
end
local mark = ARGV[1] .. ':read'
local read = redis.call('HGET', KEYS[1], mark)
if not read or ARGV[2] > read then
  read = ARGV[2]
  redis.call('HSET', KEYS[1], mark, read)
end
redis.call('ZREMRANGEBYLEX', KEYS[2], '-', '[' .. read)
for i = 5, #ARGV do
  if ARGV[i] > read then redis.call('ZADD', KEYS[2], 'NX', 0, ARGV[i]) end
end
redis.call('ZREMRANGEBYRANK', KEYS[2], 0, -(tonumber(ARGV[4]) + 1))
local n = redis.call('ZCARD', KEYS[2])
if n > 0 then
  redis.call('PEXPIRE', KEYS[2], ARGV[3])
  redis.call('HSET', KEYS[1], ARGV[1], n)
else
  redis.call('HDEL', KEYS[1], ARGV[1])
end
return 1`)

// Forget removes the chat from the counters of the users (the chat was deleted or they left it).
func (u *Unread) Forget(ctx context.Context, chatID int64, userIDs ...int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	field := strconv.FormatInt(chatID, 10)
	pipe := u.rdb.Pipeline()
	for _, uid := range userIDs {
		pipe.HDel(ctx, u.key(uid), field, field+":read")
		pipe.Del(ctx, u.idsKey(uid, chatID))
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
