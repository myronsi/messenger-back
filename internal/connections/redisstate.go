package connections

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type RedisState struct {
	rdb    goredis.Cmdable
	prefix string
}

func NewRedisState(rdb goredis.Cmdable, prefix string) RedisState {
	return RedisState{rdb: rdb, prefix: prefix}
}

func (r RedisState) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return r.rdb.Set(ctx, r.prefix+key, value, ttl).Err()
}

func (r RedisState) Get(ctx context.Context, key string) ([]byte, bool, error) {
	v, err := r.rdb.Get(ctx, r.prefix+key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	return v, err == nil, err
}

func (r RedisState) Take(ctx context.Context, key string) ([]byte, bool, error) {
	v, err := r.rdb.GetDel(ctx, r.prefix+key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	return v, err == nil, err
}

func (r RedisState) Delete(ctx context.Context, key string) error {
	return r.rdb.Del(ctx, r.prefix+key).Err()
}
