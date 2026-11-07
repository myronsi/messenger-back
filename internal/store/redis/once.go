package redis

import (
	"context"
	"fmt"
	"time"
)

type Once struct {
	rdb    Client
	prefix string
	name   string
}

func NewOnce(rdb Client, prefix, name string) *Once {
	return &Once{rdb: rdb, prefix: prefix, name: name}
}

func (o *Once) key(id string) string { return o.prefix + "once:" + o.name + ":" + digest(id) }

func (o *Once) Claim(ctx context.Context, id string, ttl time.Duration) (bool, error) {
	ok, err := o.rdb.SetNX(ctx, o.key(id), 1, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("once claim: %w", err)
	}
	return ok, nil
}

func (o *Once) Release(ctx context.Context, id string) error {
	if err := o.rdb.Del(ctx, o.key(id)).Err(); err != nil {
		return fmt.Errorf("once release: %w", err)
	}
	return nil
}
