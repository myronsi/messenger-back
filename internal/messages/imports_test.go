package messages_test

import (
	"context"
	"errors"
	"sync/atomic"
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

type importFixture struct {
	*fixture
	raw *scylla.Messages
}

func newImportFixture(t *testing.T) *importFixture {
	t.Helper()
	pg := testenv.Postgres(t)
	rd, prefix := testenv.Redis(t)
	raw := scylla.NewMessages(testenv.Scylla(t), 10*time.Second)
	gen, _ := ids.NewGenerator(1)
	rec := &recorder{}
	log := &eventLog{}
	unread := redis.NewUnread(rd.Client(), prefix)
	linkDown := new(atomic.Bool)
	svc := messages.New(messages.Deps{
		Messages: messages.NewImportResolver(raw, pg.Imports()), Store: flakyLinkStore{pg, linkDown},
		Members: redis.NewMembers(rd.Client(), prefix, time.Minute), Unread: unread,
		Dedup: redis.NewDedup(rd.Client(), prefix, 0), IDs: gen, Notifier: rec, Events: log, Imports: raw,
		QueueNotification: pg.Notifications().Enqueue, NotificationReady: pg.Notifications().Ready,
	})
	return &importFixture{fixture: &fixture{t: t, pg: pg, svc: svc, unread: unread, rec: rec, events: log, linkDown: linkDown}, raw: raw}
}

var (
	importStart = time.Date(2019, 3, 12, 14, 5, 0, 0, time.UTC)
	importEnd   = time.Date(2021, 7, 1, 9, 0, 0, 0, time.UTC)
)

func (f *importFixture) start(chat, user int64, first, last time.Time, bob int64) messages.ChatImport {
	f.t.Helper()
	ci, err := f.svc.StartImport(context.Background(), messages.StartImport{
		ChatID: chat, UserID: user, Source: messages.ImportSourceWhatsApp, MessageCount: 3, FirstAt: first, LastAt: last,
		Senders: []messages.ImportSenderInput{
			{Key: "a", Name: "Anna", MessageCount: 1, Me: true},
			{Key: "b", Name: "Bert K.", MessageCount: 1, AssignTo: &bob},
			{Key: "c", Name: "Carla", MessageCount: 0},
		},
	})
	if err != nil {
		f.t.Fatalf("start import: %v", err)
	}
	return ci
}

func importBatch(first time.Time) []messages.ImportedMessage {
	reply := 0
	return []messages.ImportedMessage{
		{Index: 0, SenderKey: "a", OriginalAt: first, Type: scylla.TypeText, Content: str("hi from 2019")},
		{Index: 1, SenderKey: "b", OriginalAt: first.Add(time.Minute), Type: scylla.TypeText, Content: str("hello Anna"), ReplyToIndex: &reply},
		{Index: 2, OriginalAt: first.Add(2 * time.Minute), Type: messages.ImportTypeNote, Content: str("Carla joined")},
	}
}

func (f *importFixture) history(chat, user int64) []scylla.Message {
	f.t.Helper()
	p, err := f.svc.History(context.Background(), user, scylla.PageQuery{ChatID: chat, Limit: 100})
	if err != nil {
		f.t.Fatalf("history: %v", err)
	}
	return p.Messages
}

func TestImportPlacesHistoryAboveTheChat(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	alice, bob, eve := f.user("alice"), f.user("bob"), f.user("eve")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	native := f.send(chat.ID, alice, "n1", "first message here")

	ci := f.start(chat.ID, alice, importStart, importEnd, bob)
	if ci.State != postgres.ImportRunning || ci.LastMessageID >= native.ID {
		t.Fatalf("import %+v not placed below %d", ci.ChatImport, native.ID)
	}
	next, err := f.svc.AddImportBatch(ctx, alice, ci.ID, 0, importBatch(importStart))
	if err != nil || next != 3 {
		t.Fatalf("batch: %d %v", next, err)
	}
	if next, err := f.svc.AddImportBatch(ctx, alice, ci.ID, 0, importBatch(importStart)); err != nil || next != 3 {
		t.Fatalf("retried batch: %d %v", next, err)
	}
	if _, err := f.svc.AddImportBatch(ctx, alice, ci.ID, 4, importBatch(importStart)[:1]); !errors.Is(err, messages.ErrConflict) {
		t.Fatalf("overlapping batch: %v", err)
	}
	if _, err := f.svc.FinishImport(ctx, alice, ci.ID); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if again, err := f.svc.FinishImport(ctx, alice, ci.ID); err != nil || again.State != postgres.ImportDone {
		t.Fatalf("finish again: %+v %v", again.ChatImport, err)
	}

	got := f.history(chat.ID, alice)
	if len(got) != 6 || got[5].ID != native.ID {
		t.Fatalf("history has %d messages, last %d", len(got), got[len(got)-1].ID)
	}
	kinds := []string{messages.SystemHistoryImported, "", "", messages.SystemImportedNote, messages.SystemHistoryImportEnded}
	for i, want := range kinds {
		e, _ := messages.ParseSystemEvent(got[i])
		if e.Kind != want || got[i].Import == nil || got[i].Import.ID != ci.ID {
			t.Fatalf("message %d: kind %q, import %+v", i, e.Kind, got[i].Import)
		}
	}
	mine, theirs := got[1], got[2]
	if mine.SenderID == nil || *mine.SenderID != alice || mine.Import.SenderState != messages.ImportSenderStateMe || !mine.Import.OriginalAt.Equal(importStart) {
		t.Fatalf("own imported message %+v %+v", mine, mine.Import)
	}
	if theirs.SenderID != nil || theirs.Import.SenderName != "Bert K." || theirs.Import.SenderState != messages.ImportSenderStateUnconfirmed {
		t.Fatalf("unconfirmed message %+v %+v", theirs, theirs.Import)
	}
	if theirs.ReplyTo == nil || *theirs.ReplyTo != mine.ID {
		t.Fatalf("reply points to %v, want %d", theirs.ReplyTo, mine.ID)
	}

	if _, err := f.svc.Edit(ctx, alice, chat.ID, mine.ID, "changed"); !errors.Is(err, messages.ErrForbidden) {
		t.Fatalf("edit of an imported message: %v", err)
	}
	if _, err := f.svc.DecideImportSender(ctx, eve, ci.ID, "b", messages.ImportDecisionConfirm); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("outsider confirmed: %v", err)
	}
	if _, err := f.svc.DecideImportSender(ctx, alice, ci.ID, "b", messages.ImportDecisionConfirm); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("importer confirmed for bob: %v", err)
	}
	if _, err := f.svc.DecideImportSender(ctx, bob, ci.ID, "b", messages.ImportDecisionConfirm); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	theirs = f.history(chat.ID, bob)[2]
	if theirs.SenderID == nil || *theirs.SenderID != bob || theirs.Import.SenderState != messages.ImportSenderStateConfirmed {
		t.Fatalf("confirmed message %+v %+v", theirs, theirs.Import)
	}
	if _, err := f.svc.DecideImportSender(ctx, bob, ci.ID, "b", messages.ImportDecisionWithdraw); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if _, err := f.svc.AssignImportSender(ctx, alice, ci.ID, "b", &bob); !errors.Is(err, messages.ErrConflict) {
		t.Fatalf("assigned bob again after he withdrew: %v", err)
	}
	if got := f.history(chat.ID, alice)[2]; got.SenderID != nil {
		t.Fatalf("withdrawn name still linked to %d", *got.SenderID)
	}

	if _, err := f.svc.StartImport(ctx, messages.StartImport{
		ChatID: chat.ID, UserID: bob, Source: messages.ImportSourceTelegram, MessageCount: 1,
		FirstAt: importEnd.Add(-time.Hour), LastAt: importEnd.Add(time.Hour), Senders: []messages.ImportSenderInput{{Key: "x", Name: "X", Me: true}},
	}); !errors.Is(err, messages.ErrConflict) {
		t.Fatalf("overlapping import: %v", err)
	}

	if err := f.svc.RemoveImport(ctx, bob, ci.ID); !errors.Is(err, messages.ErrForbidden) {
		t.Fatalf("bob removed alice's import: %v", err)
	}
	if err := f.svc.RemoveImport(ctx, alice, ci.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := f.history(chat.ID, bob); len(got) != 1 || got[0].ID != native.ID {
		t.Fatalf("history after remove has %d messages", len(got))
	}
	again := f.start(chat.ID, alice, importStart, importEnd, bob)
	if again.FirstMessageID <= ci.LastMessageID && again.LastMessageID >= ci.FirstMessageID {
		t.Fatalf("new import %d..%d inside the removed one %d..%d", again.FirstMessageID, again.LastMessageID, ci.FirstMessageID, ci.LastMessageID)
	}
	if err := f.raw.DeleteRange(ctx, chat.ID, ci.FirstMessageID, ci.LastMessageID); err != nil {
		t.Fatal(err)
	}
	p, err := f.raw.Page(ctx, scylla.PageQuery{ChatID: chat.ID, Limit: 100})
	if err != nil || len(p.Messages) != 2 || p.Messages[0].Import == nil || p.Messages[0].Import.ID != again.ID {
		t.Fatalf("stored after cleanup: %d %v", len(p.Messages), err)
	}
}

func TestImportTakesTheLargestHistory(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	f.send(chat.ID, alice, "n1", "first message here")
	ci, err := f.svc.StartImport(ctx, messages.StartImport{
		ChatID: chat.ID, UserID: alice, Source: messages.ImportSourceWhatsApp, MessageCount: messages.MaxImportMessages,
		FirstAt: importStart, LastAt: importEnd,
		Senders: []messages.ImportSenderInput{{Key: "a", Name: "Anna", MessageCount: messages.MaxImportMessages, Me: true}},
	})
	if err != nil || ci.MessageCount != messages.MaxImportMessages {
		t.Fatalf("start import of %d messages: %+v %v", messages.MaxImportMessages, ci.ChatImport, err)
	}
}

func (f *importFixture) ownSticker(owner int64, emoji string) int64 {
	f.t.Helper()
	ctx := context.Background()
	side := int32(512)
	a, err := f.pg.Attachments().Create(ctx, postgres.NewAttachment{
		UploaderID: &owner, Purpose: postgres.PurposeSticker, Kind: "image", Filename: "sticker.webp",
		StorageKey: "stickers/" + uuid.NewString(), MimeType: "image/webp", Size: 100, Width: &side, Height: &side,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	pack, err := f.pg.Stickers().CreatePack(ctx, owner, "Imported stickers", "imp"+uuid.NewString()[:8], 50)
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := f.pg.Stickers().AddSticker(ctx, owner, pack.ID, a.ID, emoji, 120)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func TestImportSendsRealStickers(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	f.send(chat.ID, alice, "n1", "hi")
	sticker, foreign := f.ownSticker(alice, "😺"), f.ownSticker(bob, "🐶")
	ci := f.start(chat.ID, alice, importStart, importEnd, bob)
	at := importStart.Add(time.Minute)
	stickerMsg := func(index int, id int64) messages.ImportedMessage {
		return messages.ImportedMessage{Index: index, SenderKey: "b", OriginalAt: at, Type: scylla.TypeSticker, StickerID: &id}
	}
	if _, err := f.svc.AddImportBatch(ctx, alice, ci.ID, 0, []messages.ImportedMessage{stickerMsg(0, foreign)}); !errors.Is(err, messages.ErrInvalid) {
		t.Fatalf("a sticker of someone else's pack the importer never added: %v", err)
	}
	bare := messages.ImportedMessage{Index: 0, SenderKey: "a", OriginalAt: at, Type: scylla.TypeSticker}
	if _, err := f.svc.AddImportBatch(ctx, alice, ci.ID, 0, []messages.ImportedMessage{bare}); !errors.Is(err, messages.ErrInvalid) {
		t.Fatalf("a sticker message without sticker_id: %v", err)
	}
	if next, err := f.svc.AddImportBatch(ctx, alice, ci.ID, 0, []messages.ImportedMessage{stickerMsg(0, sticker), stickerMsg(1, sticker)}); err != nil || next != 2 {
		t.Fatalf("batch: %d %v", next, err)
	}
	var got []scylla.Message
	for _, m := range f.history(chat.ID, alice) {
		if m.Type == scylla.TypeSticker {
			got = append(got, m)
		}
	}
	if len(got) != 2 || got[0].StickerID == nil || *got[0].StickerID != sticker || got[0].Content == nil || *got[0].Content != "😺" ||
		got[0].AttachmentID == nil || got[0].Import == nil || got[0].Import.SenderName != "Bert K." {
		t.Fatalf("imported stickers %+v", got)
	}
}

func TestImportPermissions(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	alice, bob, eve := f.user("alice"), f.user("bob"), f.user("eve")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	f.send(chat.ID, alice, "n1", "hi")
	start := func(user int64, senders []messages.ImportSenderInput) error {
		_, err := f.svc.StartImport(ctx, messages.StartImport{
			ChatID: chat.ID, UserID: user, Source: messages.ImportSourceWhatsApp, MessageCount: 1, FirstAt: importStart, LastAt: importEnd, Senders: senders,
		})
		return err
	}
	me := []messages.ImportSenderInput{{Key: "a", Name: "Anna", Me: true}}
	if err := start(eve, me); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("outsider: %v", err)
	}
	cases := map[string][]messages.ImportSenderInput{
		"bad key":          {{Key: "a b", Name: "Anna", Me: true}},
		"two me":           {{Key: "a", Name: "Anna", Me: true}, {Key: "b", Name: "Bert", Me: true}},
		"assign outsider":  {{Key: "a", Name: "Anna", AssignTo: &eve}},
		"assign yourself":  {{Key: "a", Name: "Anna", AssignTo: &alice}},
		"duplicate key":    {{Key: "a", Name: "Anna"}, {Key: "a", Name: "Bert"}},
		"blank name":       {{Key: "a", Name: "  "}},
		"no senders at al": nil,
	}
	for name, senders := range cases {
		if err := start(alice, senders); !errors.Is(err, messages.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := start(alice, me); err != nil {
		t.Fatal(err)
	}
	if err := start(bob, []messages.ImportSenderInput{{Key: "b", Name: "Bert", Me: true}}); !errors.Is(err, messages.ErrConflict) {
		t.Errorf("second running import: %v", err)
	}
}
