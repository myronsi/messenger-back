package messages_test

import (
	"context"
	"testing"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestWritingInAGroupIsTakingPart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, bob, carol := f.user("owner"), f.user("bob"), f.user("carol")
	g, err := f.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: owner, Name: "G", MemberIDs: []int64{bob}})
	if err != nil {
		t.Fatal(err)
	}
	engaged := func(chat, uid int64) bool {
		t.Helper()
		p, err := f.pg.Chats().Participant(ctx, chat, uid)
		if err != nil {
			t.Fatal(err)
		}
		return p.Engaged
	}
	if engaged(g.ID, bob) || !engaged(g.ID, owner) {
		t.Fatal("only the owner takes part in a new group")
	}
	f.send(g.ID, bob, "b-1", "hi")
	if !engaged(g.ID, bob) {
		t.Fatal("bob wrote in the group")
	}
	direct, _, err := f.pg.Chats().CreateDirect(ctx, owner, carol)
	if err != nil {
		t.Fatal(err)
	}
	f.send(direct.ID, carol, "c-1", "hi")
	if engaged(direct.ID, carol) {
		t.Fatal("direct chats keep their own rule")
	}
}
