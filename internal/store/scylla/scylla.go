// Package scylla is the ScyllaDB store: message history.
package scylla

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gocql/gocql"
)

// Store connects to ScyllaDB lazily: the first successful Ping (or Session call) opens the session,
// so the process starts while the cluster is still down and /readyz reports it as unavailable.
type Store struct {
	hosts    []string
	keyspace string

	mu      sync.Mutex
	session *gocql.Session
}

// New remembers the connection settings without connecting.
func New(hosts []string, keyspace string) *Store {
	return &Store{hosts: hosts, keyspace: keyspace}
}

// Session returns the open session, connecting first when needed.
func (s *Store) Session() (*gocql.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil && !s.session.Closed() {
		return s.session, nil
	}
	cluster := gocql.NewCluster(s.hosts...)
	cluster.Keyspace = s.keyspace
	cluster.Consistency = gocql.LocalQuorum
	cluster.ConnectTimeout = 2 * time.Second
	cluster.Timeout = 2 * time.Second
	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("connect to scylla: %w", err)
	}
	s.session = session
	return session, nil
}

// Ping checks that the cluster answers a query.
func (s *Store) Ping(ctx context.Context) error {
	session, err := s.Session()
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
	if s.session == nil {
		return nil
	}
	s.session.Close()
	s.session = nil
	return nil
}
