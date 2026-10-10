// Package scylla is the ScyllaDB store: messages, reactions and per-user hidden messages (docs/messages.md).
package scylla

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gocql/gocql"
)

const connectTimeout = 2 * time.Second

// Options tunes the session.
type Options struct {
	// Consistency of reads and writes; LOCAL_QUORUM when unset. Lightweight transactions use LOCAL_SERIAL.
	Consistency gocql.Consistency
}

// Store connects to ScyllaDB lazily: the first successful Ping (or Session call) opens the session,
// so the process starts while the cluster is still down and /readyz reports it as unavailable.
type Store struct {
	hosts       []string
	keyspace    string
	consistency gocql.Consistency

	mu         sync.Mutex
	session    *gocql.Session
	connecting *attempt
	closed     bool
}

// attempt is one in-flight connection try shared by concurrent callers.
type attempt struct {
	done chan struct{}
	err  error
}

// New remembers the connection settings without connecting.
func New(hosts []string, keyspace string, o Options) *Store {
	if o.Consistency == gocql.Any {
		o.Consistency = gocql.LocalQuorum
	}
	return &Store{hosts: hosts, keyspace: keyspace, consistency: o.Consistency}
}

// Session returns the open session, connecting first when needed. gocql cannot cancel a connection
// attempt, so the attempt keeps running in the background (bounded by the connect timeout) when ctx
// ends first; its result is kept for the next call.
func (s *Store) Session(ctx context.Context) (*gocql.Session, error) {
	s.mu.Lock()
	if s.session != nil && !s.session.Closed() {
		session := s.session
		s.mu.Unlock()
		return session, nil
	}
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("connect to scylla: store closed")
	}
	if s.connecting == nil {
		s.connecting = &attempt{done: make(chan struct{})}
		go s.connect(s.connecting)
	}
	a := s.connecting
	s.mu.Unlock()

	select {
	case <-a.done:
	case <-ctx.Done():
		return nil, fmt.Errorf("connect to scylla: %w", ctx.Err())
	}
	if a.err != nil {
		return nil, a.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return nil, fmt.Errorf("connect to scylla: store closed")
	}
	return s.session, nil
}

func (s *Store) connect(a *attempt) {
	cluster := gocql.NewCluster(s.hosts...)
	cluster.Keyspace = s.keyspace
	cluster.Consistency = s.consistency
	cluster.SerialConsistency = gocql.LocalSerial
	cluster.ConnectTimeout = connectTimeout
	cluster.Timeout = connectTimeout
	session, err := cluster.CreateSession()

	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err != nil:
		a.err = fmt.Errorf("connect to scylla: %w", err)
	case s.closed:
		session.Close()
	default:
		s.session = session
	}
	s.connecting = nil
	close(a.done)
}

// Ping checks that the cluster answers a query.
func (s *Store) Ping(ctx context.Context) error {
	session, err := s.Session(ctx)
	if err != nil {
		return err
	}
	var now time.Time
	return session.Query("SELECT toTimestamp(now()) FROM system.local").WithContext(ctx).Scan(&now)
}

// Close closes the session if one is open.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.session == nil {
		return nil
	}
	s.session.Close()
	s.session = nil
	return nil
}
