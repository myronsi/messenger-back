package messages_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestImportOneRunningPerUser(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	first, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	second, _, _ := f.pg.Chats().CreateDirect(ctx, alice, carol)
	f.send(first.ID, alice, "n1", "hi")
	f.send(second.ID, alice, "n2", "hi")
	ci := f.start(first.ID, alice, importStart, importEnd, bob)
	_, err := f.svc.StartImport(ctx, messages.StartImport{
		ChatID: second.ID, UserID: alice, Source: messages.ImportSourceWhatsApp, MessageCount: 1, FirstAt: importStart, LastAt: importEnd,
		Senders: []messages.ImportSenderInput{{Key: "a", Name: "Anna", Me: true}},
	})
	if !errors.Is(err, messages.ErrImportBusy) || !errors.Is(err, messages.ErrConflict) {
		t.Fatalf("second running import of the same user: %v", err)
	}
	if _, err := f.svc.StartImport(ctx, messages.StartImport{
		ChatID: second.ID, UserID: carol, Source: messages.ImportSourceWhatsApp, MessageCount: 1, FirstAt: importStart, LastAt: importEnd,
		Senders: []messages.ImportSenderInput{{Key: "c", Name: "Carla", Me: true}},
	}); err != nil {
		t.Fatalf("another user may import at the same time: %v", err)
	}
	if err := f.svc.RemoveImport(ctx, alice, ci.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.StartImport(ctx, messages.StartImport{
		ChatID: first.ID, UserID: alice, Source: messages.ImportSourceWhatsApp, MessageCount: 1, FirstAt: importStart, LastAt: importEnd,
		Senders: []messages.ImportSenderInput{{Key: "a", Name: "Anna", Me: true}},
	}); err != nil {
		t.Fatalf("after removing the running import: %v", err)
	}
}

func TestIdleImportsAreRemoved(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	f.send(chat.ID, alice, "n1", "hi")
	ci := f.start(chat.ID, alice, importStart, importEnd, bob)
	if _, err := f.svc.AddImportBatch(ctx, alice, ci.ID, 0, importBatch(importStart)[:1]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if n, err := f.svc.ExpireIdleImports(ctx, now.Add(messages.ImportIdleTimeout-time.Hour)); err != nil || n != 0 {
		t.Fatalf("an import with recent progress was removed: %d %v", n, err)
	}
	if n, err := f.svc.ExpireIdleImports(ctx, now.Add(messages.ImportIdleTimeout+time.Hour)); err != nil || n != 1 {
		t.Fatalf("idle import: %d %v", n, err)
	}
	got, err := f.pg.Imports().Get(ctx, ci.ID)
	if err != nil || got.State != postgres.ImportRemoving {
		t.Fatalf("idle import state %q %v", got.State, err)
	}
	if _, err := f.svc.AddImportBatch(ctx, alice, ci.ID, 1, importBatch(importStart)[1:2]); err == nil {
		t.Fatal("a removed import took more messages")
	}
	if n, err := f.svc.ExpireIdleImports(ctx, now.Add(messages.ImportIdleTimeout+time.Hour)); err != nil || n != 0 {
		t.Fatalf("removed twice: %d %v", n, err)
	}
}
