// Package postgres is the PostgreSQL store: accounts, chats, groups and other relational data.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps a connection pool. The pool connects lazily, so the process starts while the
// database is still down and /readyz reports it as unavailable.
type Store struct {
	pool *pgxpool.Pool
}

// New parses the connection URL and creates the pool without connecting.
func New(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// pgx includes the URL in some parse errors; do not pass it on.
		return nil, fmt.Errorf("parse postgres url: invalid format")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Pool returns the underlying pool for the repositories of later features.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping checks that the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Close releases all connections.
func (s *Store) Close() { s.pool.Close() }
