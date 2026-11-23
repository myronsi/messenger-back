package postgres

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSyncPurgeDropsOldChangesAndKeysOfEndedSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	repo := s.Sync()
	if err := repo.Record(ctx, SyncChange{ChatID: 1, Kind: SyncMessageEdited, MessageID: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE sync_changes SET created_at = now() - interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Record(ctx, SyncChange{ChatID: 1, UserID: 5, Kind: SyncChatHidden, MessageID: 11}); err != nil {
		t.Fatal(err)
	}

	u := mustUser(t, s, "alice")
	kept, ended := newSession(t, s, u.ID, "kept"), newSession(t, s, u.ID, "ended")
	for name, sess := range map[string]Session{"kept": kept, "ended": ended} {
		if got, err := repo.SaveStorageKey(ctx, sess.ID, "sealed-"+name); err != nil || got != "sealed-"+name {
			t.Fatalf("save key: %q %v", got, err)
		}
	}
	if got, err := repo.SaveStorageKey(ctx, kept.ID, "another"); err != nil || got != "sealed-kept" {
		t.Fatalf("a saved key is never replaced: %q %v", got, err)
	}
	if err := s.Sessions().Revoke(ctx, u.ID, ended.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StorageKey(ctx, ended.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("key of an ended session: %v", err)
	}

	n, err := repo.Purge(ctx, SyncChangeRetention)
	if err != nil || n != 1 {
		t.Fatalf("purged %d: %v", n, err)
	}
	oldest, err := repo.Oldest(ctx)
	if err != nil || oldest != 2 {
		t.Fatalf("oldest %d: %v", oldest, err)
	}
	head, err := repo.Head(ctx, time.Hour)
	if err != nil || head != 1 {
		t.Fatalf("head before the newest change settled %d: %v", head, err)
	}
	var keys int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_storage_keys`).Scan(&keys); err != nil || keys != 1 {
		t.Fatalf("keys left %d: %v", keys, err)
	}
	if got, err := repo.StorageKey(ctx, kept.ID); err != nil || got != "sealed-kept" {
		t.Fatalf("key of a live session: %q %v", got, err)
	}

	if _, err := s.pool.Exec(ctx, `DELETE FROM sync_changes`); err != nil {
		t.Fatal(err)
	}
	if oldest, err := repo.Oldest(ctx); err != nil || oldest != 3 {
		t.Fatalf("oldest of an empty log is the next seq: %d %v", oldest, err)
	}
	if head, err := repo.Head(ctx, 0); err != nil || head != 2 {
		t.Fatalf("head of an empty log is the last seq: %d %v", head, err)
	}
}
