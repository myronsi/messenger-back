// Package testenv gives integration tests of several packages real stores: a fresh PostgreSQL database with
// the schema, a Redis key namespace and a ScyllaDB keyspace with the migrations applied. Each skips the
// test when its TEST_* variable is not set. Only tests import this package.
package testenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

// root is the repository root, found from this file's location.
func root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func randomName(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// Postgres returns a store on a new database with every migration applied; the database is dropped when the
// test ends. It needs TEST_DATABASE_URL (a server where databases may be created).
func Postgres(t testing.TB) *postgres.Store {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	name := randomName("test_")
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = conn.Exec(c, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = conn.Close(c)
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name

	db, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(root(), "migrations", "postgres", "*.up.sql"))
	slices.Sort(files)
	for _, f := range files {
		sql, err := os.ReadFile(f) //nolint:gosec // the repository's own migrations
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, string(sql), pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
	}
	_ = db.Close(ctx)

	s, err := postgres.New(context.Background(), u.String(), postgres.Options{MaxConns: 8, QueryTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// Redis returns a client and a key prefix of the test's own. It needs TEST_REDIS_URL.
func Redis(t testing.TB) (*redis.Store, string) {
	t.Helper()
	u := os.Getenv("TEST_REDIS_URL")
	if u == "" {
		t.Skip("TEST_REDIS_URL is not set")
	}
	s, err := redis.New(u, redis.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("redis: %v", err)
	}
	return s, randomName("t") + ":"
}

var (
	scyllaOnce sync.Once
	keyspace   string
	scyllaErr  error
)

// Scylla returns a store on a keyspace with the migrations applied. The keyspace is shared by the tests of a
// package run (schema changes are slow), so tests must use ids of their own. It needs TEST_SCYLLA_HOSTS.
// Drop it with DropKeyspace from TestMain.
func Scylla(t testing.TB) *scylla.Store {
	t.Helper()
	hosts := scyllaHosts()
	if hosts == nil {
		t.Skip("TEST_SCYLLA_HOSTS is not set")
	}
	scyllaOnce.Do(func() { keyspace, scyllaErr = createKeyspace(hosts) })
	if scyllaErr != nil {
		t.Fatalf("scylla: %v", scyllaErr)
	}
	s := scylla.New(hosts, keyspace, scylla.Options{RequestTimeout: 10 * time.Second})
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func scyllaHosts() []string {
	v := os.Getenv("TEST_SCYLLA_HOSTS")
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

func createKeyspace(hosts []string) (string, error) {
	cluster := gocql.NewCluster(hosts...)
	cluster.Timeout = 30 * time.Second
	cluster.ConnectTimeout = 10 * time.Second
	sess, err := cluster.CreateSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	ks := randomName("test_")
	if err := sess.Query(`CREATE KEYSPACE ` + ks + ` WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': 1}`).Exec(); err != nil {
		return "", err
	}
	cluster.Keyspace = ks
	ksSess, err := cluster.CreateSession()
	if err != nil {
		return "", err
	}
	defer ksSess.Close()
	files, _ := filepath.Glob(filepath.Join(root(), "migrations", "scylla", "*.up.cql"))
	slices.Sort(files)
	if len(files) == 0 {
		return "", fmt.Errorf("no scylla migrations found")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // the repository's own migrations
		if err != nil {
			return "", err
		}
		for _, stmt := range strings.Split(string(raw), ";") {
			if strings.TrimSpace(stripComments(stmt)) == "" {
				continue
			}
			if err := ksSess.Query(stmt).Exec(); err != nil {
				return "", fmt.Errorf("%s: %w", filepath.Base(f), err)
			}
		}
	}
	return ks, nil
}

func stripComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// DropKeyspace drops the keyspace Scylla created, if any. Call it from TestMain after m.Run.
func DropKeyspace() {
	hosts := scyllaHosts()
	if keyspace == "" || hosts == nil {
		return
	}
	cluster := gocql.NewCluster(hosts...)
	cluster.Timeout = 30 * time.Second
	if sess, err := cluster.CreateSession(); err == nil {
		_ = sess.Query(`DROP KEYSPACE IF EXISTS ` + keyspace).Exec()
		sess.Close()
	}
}
