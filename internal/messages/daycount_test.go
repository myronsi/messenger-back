package messages_test

import (
	"context"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/stats"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/testenv"
)

func TestSentMessagesAreCountedPerDay(t *testing.T) {
	pg := testenv.Postgres(t)
	rd, prefix := testenv.Redis(t)
	gen, _ := ids.NewGenerator(1)
	counter := stats.NewDayCounter(pg.MessageCounts(), nil)
	svc := messages.New(messages.Deps{
		Messages: scylla.NewMessages(testenv.Scylla(t), 10*time.Second), Store: pg,
		Members: redis.NewMembers(rd.Client(), prefix, time.Minute), Unread: redis.NewUnread(rd.Client(), prefix),
		Dedup: redis.NewDedup(rd.Client(), prefix, 0), IDs: gen, Counter: counter,
	})
	ctx := context.Background()
	alice, err := pg.Users().Register(ctx, "alice", "alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := pg.Users().Register(ctx, "bob", "bob", "hash")
	if err != nil {
		t.Fatal(err)
	}
	chat, _, err := pg.Chats().CreateDirect(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sent time.Time
	for _, temp := range []string{"one", "two", "two"} {
		s, err := svc.Send(ctx, messages.SendRequest{ChatID: chat.ID, SenderID: alice.ID, ClientTempID: temp, Type: scylla.TypeText, Content: str("hi")})
		if err != nil {
			t.Fatal(err)
		}
		sent = s.Message.CreatedAt
	}
	if _, err := svc.Send(ctx, messages.SendRequest{ChatID: chat.ID, SenderID: alice.ID, ClientTempID: "bad", Type: scylla.TypeText}); err == nil {
		t.Fatal("an invalid send passed")
	}
	if err := counter.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := pg.MessageCounts().On(ctx, sent)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != scylla.TypeText || got[0].Messages != 2 || got[0].Attachments != 0 {
		t.Fatalf("counts %+v, want 2 text messages (the retry is not counted)", got)
	}
}
