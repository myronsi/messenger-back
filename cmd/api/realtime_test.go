package main

import (
	"context"
	"errors"
	"testing"

	"github.com/myronsi/messenger-back/internal/ids"
)

func TestLazyIDsReadyOnlyWithANode(t *testing.T) {
	var l lazyIDs
	if err := l.Ready(context.Background()); !errors.Is(err, errNoNode) {
		t.Fatalf("Ready without a node = %v, want errNoNode", err)
	}
	if _, err := l.Next(); !errors.Is(err, errNoNode) {
		t.Fatalf("Next without a node = %v, want errNoNode", err)
	}
	g, err := ids.NewGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	l.set(g)
	if err := l.Ready(context.Background()); err != nil {
		t.Fatalf("Ready with a node = %v", err)
	}
	if id, err := l.Next(); err != nil || id <= 0 {
		t.Fatalf("Next with a node = %d, %v", id, err)
	}
}
