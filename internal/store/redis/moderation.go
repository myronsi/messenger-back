package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const DefaultModerationTTL = time.Minute

type ModerationCache struct {
	rdb    Client
	prefix string
	ttl    time.Duration
}

func NewModerationCache(rdb Client, prefix string, ttl time.Duration) *ModerationCache {
	if ttl <= 0 {
		ttl = DefaultModerationTTL
	}
	return &ModerationCache{rdb: rdb, prefix: prefix, ttl: ttl}
}

func (c *ModerationCache) key(userID int64) string {
	return c.prefix + "moderation:" + strconv.FormatInt(userID, 10)
}

func (c *ModerationCache) Get(ctx context.Context, userID int64) (string, bool, error) {
	v, err := c.rdb.Get(ctx, c.key(userID)).Result()
	if errors.Is(err, goredis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("moderation cache get: %w", err)
	}
	return v, true, nil
}

func (c *ModerationCache) Set(ctx context.Context, userID int64, status string) error {
	if err := c.rdb.Set(ctx, c.key(userID), status, c.ttl).Err(); err != nil {
		return fmt.Errorf("moderation cache set: %w", err)
	}
	return nil
}

func (c *ModerationCache) Invalidate(ctx context.Context, userID int64) error {
	if err := c.rdb.Del(ctx, c.key(userID)).Err(); err != nil {
		return fmt.Errorf("moderation cache invalidate: %w", err)
	}
	return nil
}

func (c *ModerationCache) GetMany(ctx context.Context, userIDs []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	keys := make([]string, len(userIDs))
	for i, id := range userIDs {
		keys[i] = c.key(id)
	}
	vals, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("moderation cache get: %w", err)
	}
	for i, v := range vals {
		if s, ok := v.(string); ok {
			out[userIDs[i]] = s
		}
	}
	return out, nil
}

func (c *ModerationCache) SetMany(ctx context.Context, statuses map[int64]string) error {
	if len(statuses) == 0 {
		return nil
	}
	pipe := c.rdb.Pipeline()
	for id, st := range statuses {
		pipe.Set(ctx, c.key(id), st, c.ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("moderation cache set: %w", err)
	}
	return nil
}
