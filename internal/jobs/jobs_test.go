package jobs

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

type deleter struct {
	mu    sync.Mutex
	calls []int64
	err   error
}

func (d *deleter) DeleteChat(_ context.Context, id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, id)
	return d.err
}

type forgetter struct{ got [][2]int64 }

func (f *forgetter) Forget(_ context.Context, chat int64, users ...int64) error {
	for _, u := range users {
		f.got = append(f.got, [2]int64{chat, u})
	}
	return nil
}

// members holds the current memberships as "chat/user".
type members map[[2]int64]bool

func (m members) Participant(_ context.Context, chat, user int64) (postgres.Participant, error) {
	if m[[2]int64{chat, user}] {
		return postgres.Participant{ChatID: chat, UserID: user}, nil
	}
	return postgres.Participant{}, postgres.ErrNotFound
}

func entry(e events.Event) redis.Entry {
	f := map[string]string{}
	for k, v := range e.Fields() {
		switch x := v.(type) {
		case string:
			f[k] = x
		case int64:
			f[k] = strconv.FormatInt(x, 10)
		}
	}
	return redis.Entry{ID: "1-0", Fields: f}
}

func TestChatDeletedRunsTwice(t *testing.T) {
	d, f := &deleter{}, &forgetter{}
	h := ChatCleanup(d, f, members{}, time.Minute, nil)
	ctx := context.Background()

	// Fresh deletion: the first pass, then come back after the grace period.
	err := h(ctx, entry(events.Event{Type: events.ChatDeleted, ChatID: 9, At: time.Now()}))
	var later redis.RetryLater
	if !errors.As(err, &later) || later.After <= 0 || later.After > time.Minute {
		t.Fatalf("first pass: %v", err)
	}
	// After the grace period: the second pass finishes the entry.
	if err := h(ctx, entry(events.Event{Type: events.ChatDeleted, ChatID: 9, At: time.Now().Add(-2 * time.Minute)})); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 2 || d.calls[0] != 9 {
		t.Fatalf("deletions: %v", d.calls)
	}
	// A store failure is a failure (retried with backoff).
	d.err = errors.New("scylla down")
	if err := h(ctx, entry(events.Event{Type: events.ChatDeleted, ChatID: 9, At: time.Now()})); !errors.Is(err, d.err) {
		t.Fatalf("store failure: %v", err)
	}
}

func TestMemberRemovedForgetsUnread(t *testing.T) {
	d, f := &deleter{}, &forgetter{}
	h := ChatCleanup(d, f, members{}, time.Minute, nil)
	if err := h(context.Background(), entry(events.Event{Type: events.ChatMemberRemoved, ChatID: 4, UserID: 2})); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || f.got[0] != [2]int64{4, 2} || len(d.calls) != 0 {
		t.Fatalf("forgot %v, deleted %v", f.got, d.calls)
	}
	// A user who is back in the chat keeps the counter: the entry was handled late.
	h = ChatCleanup(d, f, members{{4, 3}: true}, time.Minute, nil)
	if err := h(context.Background(), entry(events.Event{Type: events.ChatMemberRemoved, ChatID: 4, UserID: 3})); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 {
		t.Fatalf("forgot the counter of a member: %v", f.got)
	}
	// Malformed entries are skipped, other types ignored.
	if err := h(context.Background(), redis.Entry{Fields: map[string]string{"type": "??"}}); err != nil {
		t.Fatal(err)
	}
	if err := h(context.Background(), entry(events.Event{Type: events.ChatMemberAdded, ChatID: 4, UserID: 2})); err != nil {
		t.Fatal(err)
	}
}
