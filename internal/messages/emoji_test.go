package messages_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/emoji"
	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/testenv"
)

type allowOne struct{ id int64 }

func (a allowOne) Clean(_ context.Context, _ int64, text string) (string, error) {
	return emoji.Rewrite(text, func(t emoji.Token) string {
		if t.ID == a.id {
			return emoji.Format(t.Shortcode, t.ID, t.Animated)
		}
		return emoji.Fallback(t.Shortcode)
	}), nil
}

func (a allowOne) Allowed(_ context.Context, _ int64, id int64) (bool, error) { return id == a.id, nil }

func emojiFixture(t *testing.T, filter messages.EmojiFilter) *fixture {
	t.Helper()
	pg := testenv.Postgres(t)
	rd, prefix := testenv.Redis(t)
	gen, _ := ids.NewGenerator(1)
	rec := &recorder{}
	svc := messages.New(messages.Deps{
		Messages: scylla.NewMessages(testenv.Scylla(t), 10*time.Second), Store: flakyLinkStore{pg, new(atomic.Bool)},
		Members: redis.NewMembers(rd.Client(), prefix, time.Minute), Unread: redis.NewUnread(rd.Client(), prefix),
		Dedup: redis.NewDedup(rd.Client(), prefix, 0), IDs: gen, Notifier: rec, Events: &eventLog{}, Emoji: filter,
	})
	return &fixture{t: t, pg: pg, svc: svc, rec: rec}
}

func TestCustomEmojiInMessagesAndReactions(t *testing.T) {
	f := emojiFixture(t, allowOne{id: 7})
	ctx := context.Background()
	a, b := f.user("emoa"), f.user("emob")
	chat, _, err := f.pg.Chats().CreateDirect(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	m := f.send(chat.ID, a, "e1", "hi <:party:7> <a:nope:8>")
	if m.Content == nil || *m.Content != "hi <:party:7> :nope:" {
		t.Fatalf("send: %q", *m.Content)
	}
	edited, err := f.svc.Edit(ctx, a, chat.ID, m.ID, "<:nope:8> again <:party:7>")
	if err != nil || edited.Content == nil || *edited.Content != ":nope: again <:party:7>" {
		t.Fatalf("edit: %v %v", edited.Content, err)
	}
	if err := f.svc.React(ctx, b, chat.ID, m.ID, "c:7", true); err != nil {
		t.Fatalf("allowed custom reaction: %v", err)
	}
	for _, bad := range []string{"c:8", "c:abc", "c:0"} {
		if err := f.svc.React(ctx, b, chat.ID, m.ID, bad, true); !errors.Is(err, messages.ErrInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if err := f.svc.React(ctx, b, chat.ID, m.ID, "c:8", false); err != nil {
		t.Fatalf("removing needs no permission: %v", err)
	}
	if err := f.svc.React(ctx, b, chat.ID, m.ID, "👍", true); err != nil {
		t.Fatalf("unicode reactions are unchanged: %v", err)
	}
}

func TestCustomEmojiWithoutAFilterFallBackToText(t *testing.T) {
	f := emojiFixture(t, nil)
	ctx := context.Background()
	a, b := f.user("emoc"), f.user("emod")
	chat, _, err := f.pg.Chats().CreateDirect(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	m := f.send(chat.ID, a, "e1", "<:party:7>!")
	if *m.Content != ":party:!" {
		t.Fatalf("plain: %q", *m.Content)
	}
	if err := f.svc.React(ctx, b, chat.ID, m.ID, "c:7", true); !errors.Is(err, messages.ErrInvalid) {
		t.Fatalf("custom reactions need a filter: %v", err)
	}
}
