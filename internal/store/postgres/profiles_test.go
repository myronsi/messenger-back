package postgres

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestChatLinksIndex(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, b := mustUser(t, s, "linker"), mustUser(t, s, "linked")
	chat, _, err := s.Chats().CreateDirect(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	set := func(id int64, urls ...string) {
		t.Helper()
		if err := s.Attachments().SetMessageURLs(ctx, chat.ID, id, urls, at); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(before int64, limit int) []int64 {
		t.Helper()
		got, err := s.Attachments().LinkMessageIDs(ctx, chat.ID, before, limit)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	set(10, "https://a.com", "https://b.com")
	set(20, "https://a.com")
	set(30, "https://c.com")
	if got := ids(0, 10); !slices.Equal(got, []int64{30, 20, 10}) {
		t.Fatalf("all: %v", got)
	}
	if got := ids(30, 1); !slices.Equal(got, []int64{20}) {
		t.Fatalf("before 30: %v", got)
	}
	set(20)
	if got := ids(0, 10); !slices.Equal(got, []int64{30, 10}) {
		t.Fatalf("after clearing a message's links: %v", got)
	}
	if err := s.Attachments().UnlinkMessage(ctx, chat.ID, 30); err != nil {
		t.Fatal(err)
	}
	set(40, "https://d.com")
	if err := s.Attachments().UnlinkMessagesUpTo(ctx, chat.ID, 10); err != nil {
		t.Fatal(err)
	}
	if got := ids(0, 10); !slices.Equal(got, []int64{40}) {
		t.Fatalf("after unlinking: %v", got)
	}
}

func TestGroupMembersPageAndCommonGroups(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	owner, zed, amy, mod, outsider := mustUser(t, s, "owner"), mustUser(t, s, "zed"), mustUser(t, s, "amy"), mustUser(t, s, "mod"), mustUser(t, s, "outsider")
	team, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "Team", MemberIDs: []int64{zed.ID, amy.ID, mod.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Chats().SetRole(ctx, team.ID, mod.ID, RoleModerator); err != nil {
		t.Fatal(err)
	}
	page := func(query string, after *MemberCursor, limit int) ([]int64, *MemberCursor, int64) {
		t.Helper()
		rows, total, err := s.Chats().GroupMembersPage(ctx, team.ID, query, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]int64, len(rows))
		for i, r := range rows {
			out[i] = r.UserID
		}
		if len(rows) == 0 {
			return out, nil, total
		}
		return out, &rows[len(rows)-1].Cursor, total
	}
	got, cur, total := page("", nil, 2)
	if !slices.Equal(got, []int64{owner.ID, mod.ID}) || total != 4 {
		t.Fatalf("first page: %v %d", got, total)
	}
	if got, _, _ = page("", cur, 2); !slices.Equal(got, []int64{amy.ID, zed.ID}) {
		t.Fatalf("second page: %v", got)
	}
	if got, _, total = page("Z", nil, 10); !slices.Equal(got, []int64{zed.ID}) || total != 1 {
		t.Fatalf("filter: %v %d", got, total)
	}
	if got, _, total = page("100%_", nil, 10); len(got) != 0 || total != 0 {
		t.Fatalf("wildcards are literal: %v %d", got, total)
	}

	pair, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "Pair", MemberIDs: []int64{zed.ID}})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := s.Chats().CommonGroups(ctx, owner.ID, zed.ID, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].ID != pair.ID || groups[1].ID != team.ID || groups[1].MemberCount != 4 || groups[0].Name != "Pair" {
		t.Fatalf("common groups: %+v", groups)
	}
	after := &CommonGroupCursor{At: groups[0].LastActivityAt, ID: groups[0].ID}
	if rest, err := s.Chats().CommonGroups(ctx, owner.ID, zed.ID, after, 10); err != nil || len(rest) != 1 || rest[0].ID != team.ID {
		t.Fatalf("after the first: %+v %v", rest, err)
	}
	if none, err := s.Chats().CommonGroups(ctx, owner.ID, outsider.ID, nil, 10); err != nil || len(none) != 0 {
		t.Fatalf("outsider: %+v %v", none, err)
	}
}
