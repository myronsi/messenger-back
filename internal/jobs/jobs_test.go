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

func (m members) FinishDeletion(_ context.Context, chat int64) error {
	m[[2]int64{chat, -1}] = true
	return nil
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
	d, f, m := &deleter{}, &forgetter{}, members{}
	h := ChatCleanup(d, f, m, time.Minute, nil)
	ctx := context.Background()

	// Fresh deletion: the first pass, then come back after the grace period.
	err := h(ctx, entry(events.Event{Type: events.ChatDeleted, ChatID: 9, At: time.Now()}))
	var later redis.RetryLater
	if !errors.As(err, &later) || later.After <= 0 || later.After > time.Minute || m[[2]int64{9, -1}] {
		t.Fatalf("first pass: %v", err)
	}
	// After the grace period: the second pass finishes the entry.
	if err := h(ctx, entry(events.Event{Type: events.ChatDeleted, ChatID: 9, At: time.Now().Add(-2 * time.Minute)})); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 2 || d.calls[0] != 9 || !m[[2]int64{9, -1}] {
		t.Fatalf("deletions: %v, finished %v", d.calls, m[[2]int64{9, -1}])
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

type deletions struct {
	stale   []postgres.ChatDeletion
	emitted []int64
}

func (d *deletions) StaleDeletions(context.Context, time.Duration, int) ([]postgres.ChatDeletion, error) {
	return d.stale, nil
}

func (d *deletions) MarkDeletionEmitted(_ context.Context, chatID int64) error {
	d.emitted = append(d.emitted, chatID)
	return nil
}

type emitter struct {
	got []events.Event
	err error
}

func (e *emitter) Emit(_ context.Context, ev events.Event) error {
	if e.err != nil {
		return e.err
	}
	e.got = append(e.got, ev)
	return nil
}

func TestLostChatDeletionsAreSentAgain(t *testing.T) {
	at := time.Now().Add(-time.Hour)
	store := &deletions{stale: []postgres.ChatDeletion{{ChatID: 7, DeletedAt: at}, {ChatID: 8, DeletedAt: at}}}
	em := &emitter{}
	n, err := ResendChatDeletions(context.Background(), store, em, 15*time.Minute, 100)
	if err != nil || n != 2 || len(em.got) != 2 || em.got[0].Type != events.ChatDeleted || em.got[0].ChatID != 7 || !em.got[0].At.Equal(at) {
		t.Fatalf("resent %d %v: %+v", n, err, em.got)
	}
	if len(store.emitted) != 2 {
		t.Fatalf("marked %v", store.emitted)
	}
	store.emitted, em.err = nil, errors.New("redis down")
	if _, err := ResendChatDeletions(context.Background(), store, em, 15*time.Minute, 100); err == nil || len(store.emitted) != 0 {
		t.Fatalf("a failed emit must not be marked: %v %v", err, store.emitted)
	}
}
