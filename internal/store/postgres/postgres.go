// Package postgres is the PostgreSQL store: accounts, chats, groups and other relational data.
//
// The schema is in migrations/postgres, the SQL of the typed queries in queries/, and sqlcdb/ is
// generated from both by sqlc (go generate). Feature packages use the repository interfaces in
// repository.go, never the pool or the generated code directly.
package postgres

//go:generate go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate -f ../../../sqlc.yaml

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

const defaultQueryTimeout = 5 * time.Second

// Options tunes the pool and the per-call deadline. Zero values select the defaults.
type Options struct {
	// MaxConns is the pool size (pgxpool default when 0).
	MaxConns int32
	// QueryTimeout bounds every repository call; a transaction counts as one call.
	QueryTimeout time.Duration
}

// Store wraps the process-wide connection pool. The pool connects lazily, so the process starts while
// the database is still down and /readyz reports it as unavailable.
type Store struct {
	pool    *pgxpool.Pool
	q       *sqlcdb.Queries
	timeout time.Duration
}

// New parses the connection URL and creates the pool without connecting.
func New(ctx context.Context, url string, opts Options) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// pgx includes the URL in some parse errors; do not pass it on.
		return nil, fmt.Errorf("parse postgres url: invalid format")
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	return newStore(pool, opts.QueryTimeout), nil
}

func newStore(pool *pgxpool.Pool, timeout time.Duration) *Store {
	if timeout <= 0 {
		timeout = defaultQueryTimeout
	}
	return &Store{pool: pool, q: sqlcdb.New(pool), timeout: timeout}
}

// Pool returns the underlying pool, for health checks and tooling. Repositories are the way to run queries.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping checks that the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Close releases all connections.
func (s *Store) Close() { s.pool.Close() }

// Users returns the account repository.
func (s *Store) Users() UserRepository { return userRepo{s} }

// Chats returns the repository for chats, groups and participants.
func (s *Store) Chats() ChatRepository { return chatRepo{s} }

// Attachments returns the attachment metadata repository.
func (s *Store) Attachments() AttachmentRepository { return attachmentRepo{s} }

// SecurityEvents returns the security event log.
func (s *Store) SecurityEvents() SecurityEventRepository { return securityEventRepo{s} }

// call derives the context of one repository call: the caller's context plus the query timeout.
func (s *Store) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

// inTx runs fn in one transaction under a single timeout. The transaction is rolled back when fn fails.
func (s *Store) inTx(ctx context.Context, fn func(ctx context.Context, q *sqlcdb.Queries) error) error {
	ctx, cancel := s.call(ctx)
	defer cancel()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(ctx, s.q.WithTx(tx))
	})
	return mapError(err)
}
