package messages_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/testenv"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testenv.DropKeyspace()
	os.Exit(code)
}

// recorder is a Notifier that remembers what it was told.
type recorder struct {
	messages.NopNotifier
	mu      sync.Mutex
	created []scylla.Message
	deleted []string
	reads   [][]int64
}

func (r *recorder) MessageCreated(_ context.Context, m scylla.Message, _ []int64, _ string) {
	r.mu.Lock()
	r.created = append(r.created, m)
	r.mu.Unlock()
}

func (r *recorder) MessageDeleted(_ context.Context, _, _ int64, scope string, recipients []int64) {
	r.mu.Lock()
	r.deleted = append(r.deleted, scope)
	r.mu.Unlock()
}

func (r *recorder) Read(_ context.Context, _, _, _ int64, _ time.Time, recipients []int64) {
	r.mu.Lock()
	r.reads = append(r.reads, recipients)
	r.mu.Unlock()
}

type fixture struct {
	t      *testing.T
	pg     *postgres.Store
	svc    *messages.Service
	unread *redis.Unread
	rec    *recorder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pg := testenv.Postgres(t)
	rd, prefix := testenv.Redis(t)
	repo := scylla.NewMessages(testenv.Scylla(t), 10*time.Second)
	gen, _ := ids.NewGenerator(1)
	rec := &recorder{}
	unread := redis.NewUnread(rd.Client(), prefix)
	svc := messages.New(messages.Deps{
		Messages: repo, Store: pg, Members: redis.NewMembers(rd.Client(), prefix, time.Minute), Unread: unread,
		Dedup: redis.NewDedup(rd.Client(), prefix, 0), IDs: gen, Notifier: rec,
	})
	return &fixture{t: t, pg: pg, svc: svc, unread: unread, rec: rec}
}

func (f *fixture) user(name string) int64 {
	u, err := f.pg.Users().Register(context.Background(), name, name, "hash")
	if err != nil {
		f.t.Fatal(err)
	}
	return u.ID
}

func str(s string) *string { return &s }

func (f *fixture) send(chat, sender int64, temp, content string) scylla.Message {
	f.t.Helper()
	s, err := f.svc.Send(context.Background(), messages.SendRequest{ChatID: chat, SenderID: sender, ClientTempID: temp, Type: scylla.TypeText, Content: str(content)})
	if err != nil {
		f.t.Fatalf("send: %v", err)
	}
	return s.Message
}

func TestSendValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	att := uuid.New()
	cases := map[string]messages.SendRequest{
		"blank text":         {Type: scylla.TypeText, Content: str("   ")},
		"too long":           {Type: scylla.TypeText, Content: str(strings.Repeat("x", 4097))},
		"nul byte":           {Type: scylla.TypeText, Content: str("a\x00b")},
		"text and file":      {Type: scylla.TypeText, Content: str("x"), AttachmentID: &att},
		"file without id":    {Type: scylla.TypeFile},
		"system from client": {Type: scylla.TypeSystem, Content: str("x")},
		"unknown attachment": {Type: scylla.TypeFile, AttachmentID: &att},
		"reply to nothing":   {Type: scylla.TypeText, Content: str("x"), ReplyTo: new(int64)},
	}
	for name, r := range cases {
		r.ChatID, r.SenderID, r.ClientTempID = chat.ID, alice, "t-"+name
		if _, err := f.svc.Send(ctx, r); !errors.Is(err, messages.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.svc.Send(ctx, messages.SendRequest{ChatID: chat.ID, SenderID: alice, Type: scylla.TypeText, Content: str("x")}); !errors.Is(err, messages.ErrInvalid) {
		t.Error("accepted a send without client_temp_id")
	}
}

func TestAttachmentsMustBelongToTheSenderAndChat(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	other, _, _ := f.pg.Chats().CreateDirect(ctx, alice, f.user("carol"))
	upload := func(chatID, uploader int64, mime string) uuid.UUID {
		a, err := f.pg.Attachments().Create(ctx, postgres.NewAttachment{UploaderID: &uploader, ChatID: chatID, StorageKey: "attachments/" + uuid.NewString(), MimeType: mime, Size: 10})
		if err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	try := func(typ string, id uuid.UUID) error {
		_, err := f.svc.Send(ctx, messages.SendRequest{ChatID: chat.ID, SenderID: alice, ClientTempID: uuid.NewString(), Type: typ, AttachmentID: &id})
		return err
	}
	if err := try(scylla.TypeFile, upload(chat.ID, bob, "image/png")); !errors.Is(err, messages.ErrInvalid) {
		t.Errorf("someone else's upload: %v", err)
	}
	if err := try(scylla.TypeFile, upload(other.ID, alice, "image/png")); !errors.Is(err, messages.ErrInvalid) {
		t.Errorf("upload of another chat: %v", err)
	}
	if err := try(scylla.TypeVoice, upload(chat.ID, alice, "image/png")); !errors.Is(err, messages.ErrInvalid) {
		t.Errorf("voice message with an image: %v", err)
	}
	if err := try(scylla.TypeVoice, upload(chat.ID, alice, "audio/ogg")); err != nil {
		t.Errorf("voice message: %v", err)
	}
}

func TestSendIsIdempotentAndCountsUnread(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	if err := f.unread.Load(ctx, bob, map[int64]int64{}); err != nil {
		t.Fatal(err)
	}
	first := f.send(chat.ID, alice, "same", "hello")
	again, err := f.svc.Send(ctx, messages.SendRequest{ChatID: chat.ID, SenderID: alice, ClientTempID: "same", Type: scylla.TypeText, Content: str("hello")})
	if err != nil || !again.Duplicate || again.Message.ID != first.ID || !again.Message.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("retry: %+v %v", again, err)
	}
	f.send(chat.ID, alice, "other", "second")
	counts, _, _ := f.unread.Get(ctx, bob)
	if counts[chat.ID] != 2 {
		t.Fatalf("bob has %d unread, want 2", counts[chat.ID])
	}
	if len(f.rec.created) != 2 {
		t.Fatalf("%d deliveries, want 2", len(f.rec.created))
	}
	// Reading the first message leaves one unread.
	if err := f.svc.Read(ctx, bob, chat.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if counts, _, _ := f.unread.Get(ctx, bob); counts[chat.ID] != 1 {
		t.Fatalf("after reading the first: %d", counts[chat.ID])
	}
	if got := f.rec.reads[0]; len(got) != 2 {
		t.Fatalf("read receipts go to both members: %v", got)
	}
	// With read receipts off, only the reader's devices hear about it.
	settings, _ := f.pg.Social().Privacy(ctx, []int64{bob})
	s := settings[bob]
	s.ReadReceiptsEnabled = false
	if _, err := f.pg.Social().UpdatePrivacy(ctx, s); err != nil {
		t.Fatal(err)
	}
	last := f.send(chat.ID, alice, "third", "third")
	if err := f.svc.Read(ctx, bob, chat.ID, last.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.rec.reads[1]; len(got) != 1 || got[0] != bob {
		t.Fatalf("receipts off: %v", got)
	}
}

func TestEditAndDeletePermissions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, mod, member := f.user("owner"), f.user("mod"), f.user("member")
	group, err := f.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: owner, Name: "G", MemberIDs: []int64{mod, member}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.pg.Chats().SetRole(ctx, group.ID, mod, postgres.RoleModerator); err != nil {
		t.Fatal(err)
	}
	m := f.send(group.ID, member, "a", "mine")
	if _, err := f.svc.Edit(ctx, owner, group.ID, m.ID, "not yours"); !errors.Is(err, messages.ErrForbidden) {
		t.Fatalf("edit someone else's: %v", err)
	}
	if _, err := f.svc.Edit(ctx, member, group.ID, m.ID, "edited"); err != nil {
		t.Fatal(err)
	}
	// Anyone may delete for themselves; for everyone only the sender or a moderator.
	if err := f.svc.Delete(ctx, owner, group.ID, m.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	m2 := f.send(group.ID, owner, "b", "by owner")
	if err := f.svc.Delete(ctx, member, group.ID, m2.ID, messages.ScopeEveryone); !errors.Is(err, messages.ErrForbidden) {
		t.Fatalf("member deleting the owner's message: %v", err)
	}
	if err := f.svc.Delete(ctx, mod, group.ID, m2.ID, messages.ScopeEveryone); err != nil {
		t.Fatalf("moderator: %v", err)
	}
	if _, err := f.svc.Edit(ctx, owner, group.ID, m2.ID, "back"); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("edit after delete: %v", err)
	}
	// Outsiders learn nothing.
	stranger := f.user("stranger")
	if err := f.svc.Delete(ctx, stranger, group.ID, m.ID, messages.ScopeMe); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("stranger: %v", err)
	}
	if err := f.svc.React(ctx, stranger, group.ID, m.ID, "👍", true); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("stranger reacting: %v", err)
	}
	if err := f.svc.React(ctx, member, group.ID, m.ID, "two words", true); !errors.Is(err, messages.ErrInvalid) {
		t.Fatalf("emoji with a space: %v", err)
	}
	if err := f.svc.Delete(ctx, owner, group.ID, m.ID, "all"); !errors.Is(err, messages.ErrInvalid) {
		t.Fatalf("bad scope: %v", err)
	}
	// In a direct chat nobody moderates.
	a, b := f.user("anna"), f.user("bert")
	direct, _, _ := f.pg.Chats().CreateDirect(ctx, a, b)
	dm := f.send(direct.ID, a, "c", "hi")
	if err := f.svc.Delete(ctx, b, direct.ID, dm.ID, messages.ScopeEveryone); !errors.Is(err, messages.ErrForbidden) {
		t.Fatalf("deleting the other's direct message: %v", err)
	}
}

func TestResendOnlyOwnMessages(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	m := f.send(chat.ID, alice, "x", "hello")
	if err := f.svc.Resend(ctx, bob, chat.ID, m.ID); !errors.Is(err, messages.ErrForbidden) {
		t.Fatalf("resending someone else's: %v", err)
	}
	if err := f.svc.Resend(ctx, alice, chat.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.rec.created) != 2 {
		t.Fatalf("resend did not deliver: %d", len(f.rec.created))
	}
}
