package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestMigrationsRoundTrip(t *testing.T) {
	url := newDatabase(t)
	if err := migrate(t, url, ".up.sql"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := migrate(t, url, ".down.sql"); err != nil {
		t.Fatalf("down: %v", err)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var left int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname = 'public'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("down migrations left %d tables behind", left)
	}
	if err := migrate(t, url, ".up.sql"); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// Every foreign key states what happens on delete, so deleting an account never trips over a reference.
func TestForeignKeysHaveDeleteRules(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.Pool().Query(context.Background(), `
		SELECT conrelid::regclass::text, conname
		FROM pg_constraint
		WHERE contype = 'f' AND connamespace = 'public'::regnamespace AND confdeltype = 'a'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, name string
		if err := rows.Scan(&table, &name); err != nil {
			t.Fatal(err)
		}
		t.Errorf("foreign key %s on %s has no explicit ON DELETE rule", name, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// A foreign key column without an index makes deletes and joins scan the whole table. A partial index
// only counts when it excludes nothing but NULL keys: the cascade of an account deletion must find every row.
func TestForeignKeyColumnsAreIndexed(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.Pool().Query(context.Background(), `
		SELECT c.conrelid::regclass::text, c.conname
		FROM pg_constraint c
		WHERE c.contype = 'f' AND c.connamespace = 'public'::regnamespace
		AND NOT EXISTS (
			SELECT 1 FROM pg_index i
			WHERE i.indrelid = c.conrelid
			AND i.indisvalid
			AND (i.indkey::int2[])[0:cardinality(c.conkey) - 1] = c.conkey
			AND (
				i.indpred IS NULL
				OR (cardinality(c.conkey) = 1 AND pg_get_expr(i.indpred, i.indrelid) = format(
					'(%s IS NOT NULL)',
					(SELECT attname FROM pg_attribute WHERE attrelid = c.conrelid AND attnum = c.conkey[1])))
			)
		)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, name string
		if err := rows.Scan(&table, &name); err != nil {
			t.Fatal(err)
		}
		t.Errorf("foreign key %s on %s has no unfiltered index starting with its columns", name, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestAllTimestampsAreTimestamptz(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.Pool().Query(context.Background(), `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND data_type = 'timestamp without time zone'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, col string
		if err := rows.Scan(&table, &col); err != nil {
			t.Fatal(err)
		}
		t.Errorf("%s.%s is a TIMESTAMP without time zone", table, col)
	}
}
