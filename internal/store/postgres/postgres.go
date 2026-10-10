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
	"math/rand/v2"
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

// Social returns the repository of privacy settings, blocks and contact names.
func (s *Store) Social() SocialRepository { return socialRepo{s} }

// Approvals returns the approval request repository.
func (s *Store) Approvals() ApprovalRepository { return approvalRepo{s} }

// Attachments returns the attachment metadata repository.
func (s *Store) Attachments() AttachmentRepository { return attachmentRepo{s} }

// SecurityEvents returns the security event log.
func (s *Store) SecurityEvents() SecurityEventRepository { return securityEventRepo{s} }

// Sessions returns the session repository.
func (s *Store) Sessions() SessionRepository { return sessionRepo{s} }

// SecuritySettings returns the repository of security options and the second factor.
func (s *Store) SecuritySettings() SecuritySettingsRepository { return settingsRepo{s} }

// RecoveryTokens returns the repository of single-use account recovery tokens.
func (s *Store) RecoveryTokens() RecoveryTokenRepository { return recoveryRepo{s} }

// call derives the context of one repository call: the caller's context plus the query timeout.
func (s *Store) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

// maxTxAttempts bounds how often a transaction that PostgreSQL aborted for a deadlock is run again.
const maxTxAttempts = 4

// inTx runs fn in one transaction under a single timeout. The transaction is rolled back when fn fails.
// A transaction aborted by a deadlock or serialization failure is run again from the start (within the same
// timeout), so fn must not keep state between attempts: it has to set every result it hands out.
func (s *Store) inTx(ctx context.Context, fn func(ctx context.Context, q *sqlcdb.Queries) error) error {
	ctx, cancel := s.call(ctx)
	defer cancel()
	var err error
	for attempt := 1; ; attempt++ {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			return fn(ctx, s.q.WithTx(tx))
		})
		if err == nil || !isRetryable(err) || attempt == maxTxAttempts {
			break
		}
		// Jitter keeps two transactions that deadlocked from colliding again in lockstep.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Duration(5+rand.IntN(20)) * time.Millisecond): //nolint:gosec // jitter, not a secret
		}
	}
	return mapError(err)
}
