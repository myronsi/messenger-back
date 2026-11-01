package messages_test

import (
	"context"
	"slices"
	"testing"
)

func TestStoredMessageHasDurableNotificationIntent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, err := f.pg.Chats().CreateDirect(ctx, alice, bob)
	if err != nil {
		t.Fatal(err)
	}
	m := f.send(chat.ID, alice, "notification-intent", "hello")
	var chatID int64
	var recipients []int64
	err = f.pg.Pool().QueryRow(ctx, `SELECT chat_id,recipients FROM notification_jobs WHERE message_id=$1`, m.ID).Scan(&chatID, &recipients)
	if err != nil || chatID != chat.ID || !slices.Equal(recipients, []int64{bob}) {
		t.Fatalf("missing or incorrect notification intent: %d %v %v", chatID, recipients, err)
	}
	duplicate := f.send(chat.ID, alice, "notification-intent", "hello")
	if duplicate.ID != m.ID {
		t.Fatal("message retry was not idempotent")
	}
	var count int
	if err := f.pg.Pool().QueryRow(ctx, `SELECT count(*) FROM notification_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry duplicated intent: %d %v", count, err)
	}
}
