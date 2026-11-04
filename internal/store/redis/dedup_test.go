package redis

import (
	"context"
	"testing"
)

func TestDedup(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	d := NewDedup(s.Client(), prefix, 0)

	if id, claimed, err := d.Claim(ctx, 1, 9, "c-1", 100); err != nil || !claimed || id != 100 {
		t.Fatalf("first claim: %d %v %v", id, claimed, err)
	}
	// A retry gets the first message's id.
	if id, claimed, err := d.Claim(ctx, 1, 9, "c-1", 200); err != nil || claimed || id != 100 {
		t.Fatalf("second claim: %d %v %v", id, claimed, err)
	}
	// Ids are per user and per chat.
	if _, claimed, _ := d.Claim(ctx, 2, 9, "c-1", 300); !claimed {
		t.Fatal("users share client_temp_ids")
	}
	if _, claimed, _ := d.Claim(ctx, 1, 10, "c-1", 301); !claimed {
		t.Fatal("chats share client_temp_ids")
	}
	// Only the owner of a claim releases it.
	if err := d.Release(ctx, 1, 9, "c-1", 200); err != nil {
		t.Fatal(err)
	}
	if id, _, _ := d.Claim(ctx, 1, 9, "c-1", 400); id != 100 {
		t.Fatal("a foreign release removed the claim")
	}
	if err := d.Release(ctx, 1, 9, "c-1", 100); err != nil {
		t.Fatal(err)
	}
	if id, claimed, _ := d.Claim(ctx, 1, 9, "c-1", 500); !claimed || id != 500 {
		t.Fatal("released claim still there")
	}
}

func TestDedupFailedClaimIsResumedOnce(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	d := NewDedup(s.Client(), prefix, 0)

	if _, claimed, err := d.Claim(ctx, 1, 9, "c-1", 100); err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if ok, err := d.Resume(ctx, 1, 9, "c-1", 100); err != nil || ok {
		t.Fatalf("a running send must not be resumed: %v %v", ok, err)
	}
	if err := d.Fail(ctx, 1, 9, "c-1", 100); err != nil {
		t.Fatal(err)
	}
	if id, claimed, err := d.Claim(ctx, 1, 9, "c-1", 200); err != nil || claimed || id != 100 {
		t.Fatalf("a failed send keeps its id: %d %v %v", id, claimed, err)
	}
	if ok, err := d.Resume(ctx, 1, 9, "c-1", 100); err != nil || !ok {
		t.Fatalf("resume: %v %v", ok, err)
	}
	if ok, _ := d.Resume(ctx, 1, 9, "c-1", 100); ok {
		t.Fatal("two retries resumed the same send")
	}
	if err := d.Fail(ctx, 1, 9, "c-1", 300); err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.Resume(ctx, 1, 9, "c-1", 300); ok {
		t.Fatal("a foreign failure changed the claim")
	}
}
