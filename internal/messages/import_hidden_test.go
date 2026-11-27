package messages_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func importIDs(msgs []scylla.Message) map[int64]int {
	out := map[int64]int{}
	for _, m := range msgs {
		if m.Import != nil {
			out[m.Import.ID]++
		}
	}
	return out
}

func (f *importFixture) finishedImport(chat, alice, bob int64, first, last time.Time) messages.ChatImport {
	f.t.Helper()
	ctx := context.Background()
	ci := f.start(chat, alice, first, last, bob)
	if _, err := f.svc.AddImportBatch(ctx, alice, ci.ID, 0, importBatch(first)); err != nil {
		f.t.Fatalf("batch: %v", err)
	}
	if _, err := f.svc.FinishImport(ctx, alice, ci.ID); err != nil {
		f.t.Fatalf("finish: %v", err)
	}
	return ci
}

func TestRunningImportIsHiddenFromOtherMembers(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	native := f.send(chat.ID, alice, "n1", "first message here")

	done := f.finishedImport(chat.ID, alice, bob, importStart, importEnd)
	later := time.Date(2022, 5, 1, 10, 0, 0, 0, time.UTC)
	running := f.start(chat.ID, alice, later, later.Add(time.Hour), bob)
	if running.FirstMessageID <= done.LastMessageID || running.LastMessageID >= native.ID {
		t.Fatalf("running import %d..%d not between %d and %d", running.FirstMessageID, running.LastMessageID, done.LastMessageID, native.ID)
	}
	if _, err := f.svc.AddImportBatch(ctx, alice, running.ID, 0, importBatch(later)); err != nil {
		t.Fatalf("batch: %v", err)
	}

	if got := importIDs(f.history(chat.ID, alice)); got[running.ID] == 0 || got[done.ID] == 0 {
		t.Fatalf("importer sees imports %v", got)
	}
	if got := importIDs(f.history(chat.ID, bob)); got[running.ID] != 0 || got[done.ID] == 0 {
		t.Fatalf("other member sees imports %v", got)
	}

	p, err := f.svc.History(ctx, bob, scylla.PageQuery{ChatID: chat.ID, Before: native.ID, Limit: 2})
	if err != nil || len(p.Messages) != 2 || importIDs(p.Messages)[done.ID] != 2 || !p.HasOlder {
		t.Fatalf("older page skips the running import: %d messages %v %v", len(p.Messages), importIDs(p.Messages), err)
	}
	p, err = f.svc.History(ctx, bob, scylla.PageQuery{ChatID: chat.ID, After: done.LastMessageID, Limit: 3})
	if err != nil || len(p.Messages) != 1 || p.Messages[0].ID != native.ID || p.HasNewer {
		t.Fatalf("newer page skips the running import: %+v %v", p, err)
	}
	if _, err := f.svc.History(ctx, bob, scylla.PageQuery{ChatID: chat.ID, Around: running.FirstMessageID}); !errors.Is(err, messages.ErrNotFound) {
		t.Fatalf("around a hidden message: %v", err)
	}
	if _, err := f.svc.History(ctx, alice, scylla.PageQuery{ChatID: chat.ID, Around: running.FirstMessageID}); err != nil {
		t.Fatalf("importer around own running import: %v", err)
	}

	if _, err := f.svc.FinishImport(ctx, alice, running.ID); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got := importIDs(f.history(chat.ID, bob)); got[running.ID] == 0 {
		t.Fatalf("finished import still hidden: %v", got)
	}
}

func TestRunningImportMediaAndLinksAreHiddenFromOtherMembers(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	livePhoto := f.upload(chat.ID, alice, "image", "image/png")
	live, err := f.svc.Send(ctx, messages.SendRequest{
		ChatID: chat.ID, SenderID: alice, ClientTempID: uuid.NewString(), Type: scylla.TypeFile, AttachmentID: &livePhoto,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.send(chat.ID, alice, "n2", "see https://example.com/live")

	ci := f.start(chat.ID, alice, importStart, importEnd, bob)
	oldPhoto := f.upload(chat.ID, alice, "image", "image/png")
	batch := []messages.ImportedMessage{
		{Index: 0, SenderKey: "a", OriginalAt: importStart, Type: scylla.TypeFile, AttachmentIDs: []uuid.UUID{oldPhoto}},
		{Index: 1, SenderKey: "b", OriginalAt: importStart.Add(time.Minute), Type: scylla.TypeText, Content: str("old https://example.com/old")},
		{Index: 2, SenderKey: "a", OriginalAt: importStart.Add(2 * time.Minute), Type: scylla.TypeText, Content: str("bye")},
	}
	if _, err := f.svc.AddImportBatch(ctx, alice, ci.ID, 0, batch); err != nil {
		t.Fatalf("batch: %v", err)
	}

	mine, _, err := f.svc.Media(ctx, alice, chat.ID, []string{"image"}, 0, 50)
	if err != nil || len(mine) != 2 {
		t.Fatalf("importer media: %d %v", len(mine), err)
	}
	theirs, next, err := f.svc.Media(ctx, bob, chat.ID, []string{"image"}, 0, 50)
	if err != nil || len(theirs) != 1 || theirs[0].Message.ID != live.Message.ID {
		t.Fatalf("other member media: %d %v", len(theirs), err)
	}
	if next != 0 {
		rest, _, err := f.svc.Media(ctx, bob, chat.ID, []string{"image"}, next, 50)
		if err != nil || len(rest) != 0 {
			t.Fatalf("media after the hidden import: %d %v", len(rest), err)
		}
	}

	links, _, err := f.svc.LinkMessages(ctx, alice, chat.ID, 0, 50)
	if err != nil || len(links) != 2 {
		t.Fatalf("importer links: %d %v", len(links), err)
	}
	links, _, err = f.svc.LinkMessages(ctx, bob, chat.ID, 0, 50)
	if err != nil || len(links) != 1 || links[0].Import != nil {
		t.Fatalf("other member links: %d %v", len(links), err)
	}
}
