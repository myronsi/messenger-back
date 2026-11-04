package messages_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/events"
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
	mu         sync.Mutex
	created    []scylla.Message
	recipients []int64
	deleted    []string
	reads      [][]int64
}

func (r *recorder) MessageCreated(_ context.Context, m scylla.Message, recipients []int64, _ string) {
	r.mu.Lock()
	r.created = append(r.created, m)
	r.recipients = recipients
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

// eventLog records emitted events.
type eventLog struct {
	mu  sync.Mutex
	got []events.Event
}

func (l *eventLog) Emit(_ context.Context, e events.Event) error {
	l.mu.Lock()
	l.got = append(l.got, e)
	l.mu.Unlock()
	return nil
}

func (l *eventLog) types() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.got))
	for i, e := range l.got {
		out[i] = e.Type
	}
	return out
}

type fixture struct {
	events   *eventLog
	t        *testing.T
	pg       *postgres.Store
	svc      *messages.Service
	unread   *redis.Unread
	rec      *recorder
	linkDown *atomic.Bool
}

type flakyLinkStore struct {
	*postgres.Store
	down *atomic.Bool
}

func (s flakyLinkStore) Attachments() postgres.AttachmentRepository {
	return flakyLinks{s.Store.Attachments(), s.down}
}

type flakyLinks struct {
	postgres.AttachmentRepository
	down *atomic.Bool
}

func (l flakyLinks) UnlinkMessage(ctx context.Context, chatID, messageID int64) error {
	if l.down.Load() {
		return errors.New("postgres is away")
	}
	return l.AttachmentRepository.UnlinkMessage(ctx, chatID, messageID)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, func(r scylla.MessageRepository) scylla.MessageRepository { return r })
}

func newFixtureWith(t *testing.T, wrap func(scylla.MessageRepository) scylla.MessageRepository) *fixture {
	t.Helper()
	pg := testenv.Postgres(t)
	rd, prefix := testenv.Redis(t)
	repo := wrap(scylla.NewMessages(testenv.Scylla(t), 10*time.Second))
	gen, _ := ids.NewGenerator(1)
	rec := &recorder{}
	log := &eventLog{}
	unread := redis.NewUnread(rd.Client(), prefix)
	linkDown := new(atomic.Bool)
	svc := messages.New(messages.Deps{
		Messages: repo, Store: flakyLinkStore{pg, linkDown}, Members: redis.NewMembers(rd.Client(), prefix, time.Minute), Unread: unread,
		Dedup: redis.NewDedup(rd.Client(), prefix, 0), IDs: gen, Notifier: rec, Events: log,
		QueueNotification: pg.Notifications().Enqueue,
	})
	return &fixture{t: t, pg: pg, svc: svc, unread: unread, rec: rec, events: log, linkDown: linkDown}
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
		a, err := f.pg.Attachments().Create(ctx, postgres.NewAttachment{UploaderID: &uploader, ChatID: &chatID, StorageKey: "attachments/" + uuid.NewString(), MimeType: mime, Size: 10})
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

func TestEditChangesOnlyTheCaptionOfAFile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	upload := func(mime string) uuid.UUID {
		a, err := f.pg.Attachments().Create(ctx, postgres.NewAttachment{UploaderID: &alice, ChatID: &chat.ID, StorageKey: "attachments/" + uuid.NewString(), MimeType: mime, Size: 10})
		if err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	photo := upload("image/png")
	sent, err := f.svc.Send(ctx, messages.SendRequest{ChatID: chat.ID, SenderID: alice, ClientTempID: uuid.NewString(), Type: scylla.TypeFile, Content: str("old caption"), AttachmentID: &photo})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := f.svc.Edit(ctx, alice, chat.ID, sent.Message.ID, "new caption")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Content == nil || *edited.Content != "new caption" || edited.EditedAt == nil {
		t.Fatalf("caption not edited: %+v", edited)
	}
	if edited.Type != scylla.TypeFile || edited.AttachmentID == nil || edited.AttachmentID.String() != photo.String() {
		t.Fatalf("attachment changed: %+v", edited)
	}
	if _, err := f.svc.Edit(ctx, bob, chat.ID, sent.Message.ID, "not yours"); !errors.Is(err, messages.ErrForbidden) {
		t.Fatalf("editing someone else's file: %v", err)
	}
	cleared, err := f.svc.Edit(ctx, alice, chat.ID, sent.Message.ID, "   ")
	if err != nil || cleared.Content == nil || *cleared.Content != "" || cleared.AttachmentID == nil {
		t.Fatalf("clearing the caption: %+v %v", cleared, err)
	}
	text, err := f.svc.Send(ctx, messages.SendRequest{ChatID: chat.ID, SenderID: alice, ClientTempID: uuid.NewString(), Type: scylla.TypeText, Content: str("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Edit(ctx, alice, chat.ID, text.Message.ID, ""); !errors.Is(err, messages.ErrInvalid) {
		t.Fatalf("emptying a text message: %v", err)
	}
	voice := upload("audio/ogg")
	recorded, err := f.svc.Send(ctx, messages.SendRequest{ChatID: chat.ID, SenderID: alice, ClientTempID: uuid.NewString(), Type: scylla.TypeVoice, AttachmentID: &voice})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Edit(ctx, alice, chat.ID, recorded.Message.ID, "words"); !errors.Is(err, messages.ErrForbidden) {
		t.Fatalf("editing a voice message: %v", err)
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
	if n, err := f.svc.Read(ctx, bob, chat.ID, first.ID); err != nil || n != 1 {
		t.Fatalf("read the first: %d %v", n, err)
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
	if _, err := f.svc.Read(ctx, bob, chat.ID, last.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.rec.reads[1]; len(got) != 1 || got[0] != bob {
		t.Fatalf("receipts off: %v", got)
	}
}

type failingInsert struct {
	scylla.MessageRepository
	failures atomic.Int32
	ids      []int64
}

func (r *failingInsert) Insert(ctx context.Context, m scylla.Message) error {
	r.ids = append(r.ids, m.ID)
	if r.failures.Add(-1) >= 0 {
		return errors.New("scylla is down")
	}
	return r.MessageRepository.Insert(ctx, m)
}

func TestRetryAfterFailedInsertKeepsTheMessageID(t *testing.T) {
	flaky := &failingInsert{}
	f := newFixtureWith(t, func(r scylla.MessageRepository) scylla.MessageRepository {
		flaky.MessageRepository = r
		return flaky
	})
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	if err := f.unread.Load(ctx, bob, map[int64]int64{}); err != nil {
		t.Fatal(err)
	}
	req := messages.SendRequest{ChatID: chat.ID, SenderID: alice, ClientTempID: "flaky", Type: scylla.TypeText, Content: str("hello")}
	flaky.failures.Store(2)
	for range 2 {
		if _, err := f.svc.Send(ctx, req); !errors.Is(err, messages.ErrUnavailable) {
			t.Fatalf("failed insert: %v", err)
		}
	}
	sent, err := f.svc.Send(ctx, req)
	if err != nil || sent.Duplicate {
		t.Fatalf("retry: %+v %v", sent, err)
	}
	if len(flaky.ids) != 3 || flaky.ids[1] != flaky.ids[0] || flaky.ids[2] != flaky.ids[0] || sent.Message.ID != flaky.ids[0] {
		t.Fatalf("every retry must store the first id: %v, sent %d", flaky.ids, sent.Message.ID)
	}
	again, err := f.svc.Send(ctx, req)
	if err != nil || !again.Duplicate || again.Message.ID != sent.Message.ID {
		t.Fatalf("after the stored retry: %+v %v", again, err)
	}
	if counts, _, _ := f.unread.Get(ctx, bob); counts[chat.ID] != 1 {
		t.Fatalf("bob has %d unread, want 1", counts[chat.ID])
	}
	if len(f.rec.created) != 1 {
		t.Fatalf("%d deliveries, want 1", len(f.rec.created))
	}
	page, err := f.svc.History(ctx, bob, scylla.PageQuery{ChatID: chat.ID, Viewer: bob, Limit: 10})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("history: %d messages, %v", len(page.Messages), err)
	}
}

type insertHook struct {
	scylla.MessageRepository
	after func(scylla.Message)
}

func (r *insertHook) Insert(ctx context.Context, m scylla.Message) error {
	if err := r.MessageRepository.Insert(ctx, m); err != nil {
		return err
	}
	if r.after != nil {
		r.after(m)
	}
	return nil
}

func TestReadDuringSendCountsTheMessageOnce(t *testing.T) {
	hook := &insertHook{}
	f := newFixtureWith(t, func(r scylla.MessageRepository) scylla.MessageRepository {
		hook.MessageRepository = r
		return hook
	})
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	if err := f.unread.Load(ctx, bob, map[int64]int64{}); err != nil {
		t.Fatal(err)
	}
	first := f.send(chat.ID, alice, "first", "one")
	hook.after = func(scylla.Message) {
		hook.after = nil
		if n, err := f.svc.Read(ctx, bob, chat.ID, first.ID); err != nil || n != 1 {
			t.Errorf("read while the second message is sent: %d %v", n, err)
		}
	}
	f.send(chat.ID, alice, "second", "two")
	if counts, _, _ := f.unread.Get(ctx, bob); counts[chat.ID] != 1 {
		t.Fatalf("bob has %d unread, want 1", counts[chat.ID])
	}
}

func TestDeletedFileMessagesFreeTheirFile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	other, _, _ := f.pg.Chats().CreateDirect(ctx, alice, carol)
	a, err := f.pg.Attachments().Create(ctx, postgres.NewAttachment{UploaderID: &alice, StorageKey: "attachments/" + uuid.NewString(), MimeType: "image/png", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := f.svc.Send(ctx, messages.SendRequest{ChatID: chat.ID, SenderID: alice, ClientTempID: uuid.NewString(), Type: scylla.TypeFile, AttachmentID: &a.ID})
	if err != nil {
		t.Fatal(err)
	}
	copies, err := f.svc.Forward(ctx, alice, sent.Message.ID, []int64{other.ID})
	if err != nil || len(copies) != 1 {
		t.Fatalf("forward: %v %v", copies, err)
	}
	unused := func() bool {
		t.Helper()
		list, err := f.pg.Attachments().Unreferenced(ctx, time.Now().Add(time.Hour), 100)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(list, func(u postgres.UnreferencedAttachment) bool { return u.ID == a.ID })
	}
	f.linkDown.Store(true)
	if err := f.svc.Delete(ctx, alice, chat.ID, sent.Message.ID, messages.ScopeEveryone); !errors.Is(err, messages.ErrUnavailable) {
		t.Fatalf("a failed unlink must fail the delete: %v", err)
	}
	f.linkDown.Store(false)
	if links, _ := f.pg.Attachments().LinksForViewer(ctx, a.ID, bob); len(links) != 1 {
		t.Fatalf("links before the retry: %v", links)
	}
	if err := f.svc.Delete(ctx, alice, chat.ID, sent.Message.ID, messages.ScopeEveryone); err != nil {
		t.Fatalf("retry of the delete: %v", err)
	}
	if unused() {
		t.Fatal("the forwarded copy still uses the file")
	}
	if links, err := f.pg.Attachments().LinksForViewer(ctx, a.ID, bob); err != nil || len(links) != 0 {
		t.Fatalf("bob still reaches the file of the deleted message: %v %v", links, err)
	}
	f.linkDown.Store(true)
	if err := f.svc.DeleteMany(ctx, alice, other.ID, []int64{copies[0].ID}, messages.ScopeEveryone); !errors.Is(err, messages.ErrUnavailable) {
		t.Fatalf("a failed unlink must fail the bulk delete: %v", err)
	}
	f.linkDown.Store(false)
	if err := f.svc.DeleteMany(ctx, alice, other.ID, []int64{copies[0].ID}, messages.ScopeEveryone); err != nil {
		t.Fatalf("retry of the bulk delete: %v", err)
	}
	if !unused() {
		t.Fatal("the file of deleted messages is never collected")
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

func TestBlocksCoverEveryAction(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	m := f.send(chat.ID, alice, "b-1", "before the block")
	if err := f.pg.Social().Block(ctx, bob, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Edit(ctx, alice, chat.ID, m.ID, "sneaky"); !errors.Is(err, messages.ErrBlocked) {
		t.Fatalf("edit: %v", err)
	}
	if err := f.svc.React(ctx, alice, chat.ID, m.ID, "👋", true); !errors.Is(err, messages.ErrBlocked) {
		t.Fatalf("react: %v", err)
	}
	if err := f.svc.Resend(ctx, alice, chat.ID, m.ID); !errors.Is(err, messages.ErrBlocked) {
		t.Fatalf("resend: %v", err)
	}
	before := len(f.rec.created)
	if err := f.svc.Typing(ctx, alice, chat.ID, true); err != nil {
		t.Fatalf("typing is dropped silently: %v", err)
	}
	if len(f.rec.created) != before {
		t.Fatal("something was delivered")
	}
	f.rec.reads = nil
	if _, err := f.svc.Read(ctx, bob, chat.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Read(ctx, alice, chat.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.rec.reads) != 2 || !slices.Equal(f.rec.reads[0], []int64{bob}) || !slices.Equal(f.rec.reads[1], []int64{alice}) {
		t.Fatalf("read receipts across a block: %v", f.rec.reads)
	}
}

func TestStaleReadChangesNothing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	if err := f.unread.Load(ctx, bob, map[int64]int64{}); err != nil {
		t.Fatal(err)
	}
	first := f.send(chat.ID, alice, "r-1", "one")
	last := f.send(chat.ID, alice, "r-2", "two")
	if _, err := f.svc.Read(ctx, bob, chat.ID, last.ID); err != nil {
		t.Fatal(err)
	}
	// A lagging tab reports an older position: the counter stays at 0 and nobody is told.
	if n, err := f.svc.Read(ctx, bob, chat.ID, first.ID); err != nil || n != 0 {
		t.Fatal(err)
	}
	if counts, _, _ := f.unread.Get(ctx, bob); counts[chat.ID] != 0 {
		t.Fatalf("unread went back up: %d", counts[chat.ID])
	}
	if len(f.rec.reads) != 1 {
		t.Fatalf("%d read events, want 1", len(f.rec.reads))
	}
}

func TestResendRespectsDeletedForMe(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, bob, carol := f.user("owner"), f.user("bob"), f.user("carol")
	g, _ := f.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: owner, Name: "G", MemberIDs: []int64{bob, carol}})
	m := f.send(g.ID, owner, "x-1", "hello")
	if err := f.svc.Delete(ctx, carol, g.ID, m.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	f.rec.mu.Lock()
	f.rec.created = nil
	f.rec.recipients = nil
	f.rec.mu.Unlock()
	if err := f.svc.Resend(ctx, owner, g.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.rec.recipients; len(got) != 1 || got[0] != owner {
		t.Fatalf("resend went to %v, want only the sender's devices", got)
	}
}

func TestClientTempIDsArePerChat(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	c1, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	c2, _, _ := f.pg.Chats().CreateDirect(ctx, alice, carol)
	a := f.send(c1.ID, alice, "c-1", "to bob")
	b := f.send(c2.ID, alice, "c-1", "to carol")
	if a.ID == b.ID {
		t.Fatal("the same client_temp_id in another chat was treated as a retry")
	}
}

func TestChangesAreRecordedAsEvents(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	m := f.send(chat.ID, alice, "e-1", "hi")
	f.send(chat.ID, alice, "e-1", "hi") // a retry is no new event
	if _, err := f.svc.Edit(ctx, alice, chat.ID, m.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.React(ctx, bob, chat.ID, m.ID, "👍", true); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(ctx, bob, chat.ID, m.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(ctx, alice, chat.ID, m.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	want := []string{events.MessageCreated, events.MessageEdited, events.ReactionChanged, events.MessageHidden, events.MessageDeleted}
	if got := f.events.types(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	first := f.events.got[0]
	if first.ChatID != chat.ID || first.MessageID != m.ID || first.ActorID != alice || !first.At.Equal(m.CreatedAt) {
		t.Fatalf("message.created: %+v", first)
	}
	if hid := f.events.got[3]; hid.UserID != bob {
		t.Fatalf("message.hidden: %+v", hid)
	}
}
