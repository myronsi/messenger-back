// Package redis is the Redis store: cache, presence, rate limiting and the event stream.
package redis

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

// Store wraps a Redis client. The client connects lazily.
type Store struct {
	client *goredis.Client
}

// New parses the connection URL and creates the client without connecting.
func New(url string) (*Store, error) {
	opts, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: invalid format")
	}
	return &Store{client: goredis.NewClient(opts)}, nil
}

// Client returns the underlying client for the features that use Redis.
func (s *Store) Client() *goredis.Client { return s.client }

// Ping checks that Redis is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }

// Close closes the client.
func (s *Store) Close() error { return s.client.Close() }
