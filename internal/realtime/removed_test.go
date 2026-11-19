package realtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func TestRemovedMessageReachesMembersWithTheReasonOnlyForTheSender(t *testing.T) {
	e := newEnv(t)
	a := e.instance("a")
	ctx := context.Background()
	alice, bob := e.user("alice"), e.user("bob")
	chat, _, err := e.pg.Chats().CreateDirect(ctx, alice, bob)
	if err != nil {
		t.Fatal(err)
	}
	ca := e.online(a, alice)
	cb := e.online(a, bob)
	waitDelivery(t, ca, cb, chat.ID)

	m := scylla.Message{ChatID: chat.ID, ID: 77, SenderID: &alice, Type: scylla.TypeText, Deleted: true,
		Moderation: &scylla.ModerationRemoval{Code: "harassment", Text: "Insults are not allowed."}}
	a.fanout.MessageRemoved(ctx, m, []int64{alice, bob})
	mine := data(ca.next("delete"))
	reason, _ := mine["moderation_reason"].(map[string]any)
	if mine["message_id"] != "77" || mine["scope"] != "everyone" || mine["removed_by_moderation"] != true || reason["code"] != "harassment" {
		t.Fatalf("sender's event: %v", mine)
	}
	theirs := data(cb.next("delete"))
	if theirs["removed_by_moderation"] != true || theirs["message_id"] != fmt.Sprint(77) {
		t.Fatalf("member's event: %v", theirs)
	}
	if _, ok := theirs["moderation_reason"]; ok {
		t.Fatalf("reason leaked to a member: %v", theirs)
	}
}
