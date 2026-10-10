package redis

import (
	"context"
	"testing"
)

func TestDedup(t *testing.T) {
	s, prefix := testStore(t)
	ctx := context.Background()
	d := NewDedup(s.Client(), prefix, 0)

	if id, claimed, err := d.Claim(ctx, 1, "c-1", 100); err != nil || !claimed || id != 100 {
		t.Fatalf("first claim: %d %v %v", id, claimed, err)
	}
	// A retry gets the first message's id.
	if id, claimed, err := d.Claim(ctx, 1, "c-1", 200); err != nil || claimed || id != 100 {
		t.Fatalf("second claim: %d %v %v", id, claimed, err)
	}
	// Ids are per user.
	if _, claimed, _ := d.Claim(ctx, 2, "c-1", 300); !claimed {
		t.Fatal("users share client_temp_ids")
	}
	// Only the owner of a claim releases it.
	if err := d.Release(ctx, 1, "c-1", 200); err != nil {
		t.Fatal(err)
	}
	if id, _, _ := d.Claim(ctx, 1, "c-1", 400); id != 100 {
		t.Fatal("a foreign release removed the claim")
	}
	if err := d.Release(ctx, 1, "c-1", 100); err != nil {
		t.Fatal(err)
	}
	if id, claimed, _ := d.Claim(ctx, 1, "c-1", 500); !claimed || id != 500 {
		t.Fatal("released claim still there")
	}
}
