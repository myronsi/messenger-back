package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const migrationsDir = "../../../migrations/postgres"

// adminURL is a connection to a server where the tests may create and drop databases.
// Without TEST_DATABASE_URL the database tests are skipped.
func adminURL(t *testing.T) string {
	t.Helper()
	v := os.Getenv("TEST_DATABASE_URL")
	if v == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	return v
}

func migrationFiles(t *testing.T, suffix string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*"+suffix))
	if err != nil || len(files) == 0 {
		t.Fatalf("no %s migrations found: %v", suffix, err)
	}
	sort.Strings(files)
	return files
}

func applyFiles(ctx context.Context, conn *pgx.Conn, files []string) error {
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, string(sql), pgx.QueryExecModeSimpleProtocol); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
	}
	return nil
}

// newDatabase creates an empty database and returns its URL; it is dropped when the test ends.
func newDatabase(t *testing.T) string {
	t.Helper()
	admin := adminURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to the admin database: %v", err)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "test_" + hex.EncodeToString(suffix)
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
	return u.String()
}

// migrate applies all migrations in one direction to the database.
func migrate(t *testing.T, dbURL, suffix string) error {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	files := migrationFiles(t, suffix)
	if suffix == ".down.sql" {
		slices.Reverse(files)
	}
	return applyFiles(ctx, conn, files)
}

// newTestStore returns a Store on a fresh database with the full schema.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbURL := newDatabase(t)
	if err := migrate(t, dbURL, ".up.sql"); err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), dbURL, Options{MaxConns: 4, QueryTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func mustUser(t *testing.T, s *Store, name string) User {
	t.Helper()
	u, err := s.Users().Create(context.Background(), name, strings.ToUpper(name[:1])+name[1:], "hash-"+name)
	if err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	return u
}
