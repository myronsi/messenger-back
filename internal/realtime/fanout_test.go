package realtime

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/testenv"
	"github.com/myronsi/messenger-back/internal/users"
)

type capturedBus struct {
	mu   sync.Mutex
	sent map[int64]string
}

func (b *capturedBus) PublishEach(_ context.Context, payloads map[int64][]byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for uid, p := range payloads {
		b.sent[uid] = string(p)
	}
	return nil
}

func (b *capturedBus) Broadcast(context.Context, []byte) error { return nil }

type fixedReply struct{ m scylla.Message }

func (r fixedReply) Get(context.Context, int64, int64) (scylla.Message, error) { return r.m, nil }

func (r fixedReply) HiddenAmong(context.Context, int64, int64, []int64) (map[int64]bool, error) {
	return map[int64]bool{}, nil
}

func TestReplySenderFollowsSharedChatsOfEachRecipient(t *testing.T) {
	pg := testenv.Postgres(t)
	ctx := context.Background()
	user := func(name string) int64 {
		u, err := pg.Users().Register(ctx, name, name, "hash")
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	alice, bob, carol := user("alice"), user("bob"), user("carol")
	bio := "carol-private-bio"
	if _, err := pg.Users().UpdateProfile(ctx, carol, postgres.Profile{DisplayName: "carol", Bio: &bio}); err != nil {
		t.Fatal(err)
	}
	settings, err := pg.Social().Privacy(ctx, []int64{carol})
	if err != nil {
		t.Fatal(err)
	}
	p := settings[carol]
	p.ProfileVisibility = postgres.VisibleSharedChats
	if _, err := pg.Social().UpdatePrivacy(ctx, p); err != nil {
		t.Fatal(err)
	}
	group, err := pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: alice, Name: "g", MemberIDs: []int64{bob}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pg.Chats().CreateDirect(ctx, carol, alice); err != nil {
		t.Fatal(err)
	}

	bus := &capturedBus{sent: map[int64]string{}}
	gen, _ := ids.NewGenerator(1)
	original := scylla.Message{ChatID: group.ID, ID: 10, SenderID: &carol, Type: scylla.TypeText, Content: new(string)}
	f := NewFanout(FanoutDeps{Bus: bus, Directory: users.NewDirectory(pg, nil, "/api/v2"), Store: pg, IDs: gen, BasePath: "/api/v2", Replies: fixedReply{original}})
	reply := original.ID
	text := "answer"
	f.MessageCreated(ctx, scylla.Message{ChatID: group.ID, ID: 11, SenderID: &alice, Type: scylla.TypeText, Content: &text, ReplyTo: &reply}, []int64{alice, bob}, "")

	if !strings.Contains(bus.sent[alice], bio) {
		t.Fatalf("alice shares a chat with carol and sees her bio: %s", bus.sent[alice])
	}
	if strings.Contains(bus.sent[bob], bio) {
		t.Fatalf("bob shares no chat with carol and must not see her bio: %s", bus.sent[bob])
	}
}
