package linkpreview

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type RedisCache struct {
	rdb goredis.Cmdable
}

func NewRedisCache(rdb goredis.Cmdable) RedisCache { return RedisCache{rdb: rdb} }

func (c RedisCache) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := c.rdb.Get(ctx, key).Result()
	if errors.Is(err, goredis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (c RedisCache) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, value, ttl).Err()
}
