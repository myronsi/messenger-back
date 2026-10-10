package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

func TestUsers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	users := s.Users()

	u, err := users.Create(ctx, "Alice_1", "Alice", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 || u.Username != "Alice_1" || u.CreatedAt.IsZero() {
		t.Fatalf("unexpected user: %+v", u)
	}

	if _, err := users.Create(ctx, "alice_1", "Other", "hash"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate username ignoring case: got %v", err)
	}
	for _, bad := range []string{"", "ab", "has space", "ünïcode", "waytoolong_waytoolong_waytoolong_x"} {
		if _, err := users.Create(ctx, bad, "X", "hash"); !errors.Is(err, ErrInvalid) {
			t.Errorf("username %q: got %v, want ErrInvalid", bad, err)
		}
	}

	byName, err := users.GetByUsername(ctx, "ALICE_1")
	if err != nil || byName.ID != u.ID {
		t.Fatalf("lookup is case-insensitive: %+v, %v", byName, err)
	}
	cred, err := users.Credentials(ctx, "alice_1")
	if err != nil || cred.UserID != u.ID || cred.PasswordHash != "hash" {
		t.Fatalf("credentials: %+v, %v", cred, err)
	}
	if _, err := users.Get(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user: got %v", err)
	}

	bio := "hello"
	up, err := users.UpdateProfile(ctx, u.ID, Profile{DisplayName: "Alice B", Bio: &bio})
	if err != nil || up.DisplayName != "Alice B" || up.Bio == nil || *up.Bio != "hello" {
		t.Fatalf("update profile: %+v, %v", up, err)
	}
	if err := users.SetPasswordHash(ctx, u.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if err := users.SetPasswordHash(ctx, 999999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set hash of a missing user: got %v", err)
	}
	if err := users.TouchLastSeen(ctx, u.ID); err != nil {
		t.Fatal(err)
	}

	bob, err := users.Create(ctx, "bob_1", "Bob", "hash")
	if err != nil {
		t.Fatal(err)
	}
	many, err := users.GetMany(ctx, []int64{u.ID, bob.ID, 999999, u.ID})
	if err != nil || len(many) != 2 || many[u.ID].Username != "Alice_1" || many[bob.ID].DisplayName != "Bob" {
		t.Fatalf("get many: %+v, %v", many, err)
	}
	if none, err := users.GetMany(ctx, nil); err != nil || len(none) != 0 {
		t.Fatalf("get none: %v, %v", none, err)
	}
}

func TestDirectChatIsUniquePerPair(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, b := mustUser(t, s, "alice"), mustUser(t, s, "bob")

	first, created, err := s.Chats().CreateDirect(ctx, a.ID, b.ID)
	if err != nil || !created || first.Type != ChatDirect || first.Name != nil {
		t.Fatalf("create: %+v created=%v err=%v", first, created, err)
	}
	again, created, err := s.Chats().CreateDirect(ctx, b.ID, a.ID)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("reversed pair must return the same chat: %+v created=%v err=%v", again, created, err)
	}
	if _, _, err := s.Chats().CreateDirect(ctx, a.ID, a.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self chat: got %v", err)
	}
	if _, _, err := s.Chats().CreateDirect(ctx, a.ID, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: got %v", err)
	}
	ps, err := s.Chats().Participants(ctx, first.ID)
	if err != nil || len(ps) != 2 {
		t.Fatalf("participants: %+v, %v", ps, err)
	}
}

func TestDirectChatConcurrentCreate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, b := mustUser(t, s, "alice"), mustUser(t, s, "bob")

	const n = 8
	ids := make([]int64, n)
	created := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			from, to := a.ID, b.ID
			if i%2 == 1 {
				from, to = to, from
			}
			c, ok, err := s.Chats().CreateDirect(ctx, from, to)
			ids[i], created[i], errs[i] = c.ID, ok, err
		}()
	}
	wg.Wait()

	winners := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("calls produced different chats: %v", ids)
		}
		if created[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("exactly one call must create the chat, %d did", winners)
	}
}

func TestCreateGroup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	owner, m1, m2 := mustUser(t, s, "owner"), mustUser(t, s, "member1"), mustUser(t, s, "member2")

	g, err := s.Chats().CreateGroup(ctx, NewGroup{
		OwnerID: owner.ID, Name: "Team", MemberIDs: []int64{m1.ID, m2.ID, m1.ID, owner.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.Type != ChatGroup || g.Name == nil || *g.Name != "Team" {
		t.Fatalf("unexpected group: %+v", g)
	}
	ps, err := s.Chats().Participants(ctx, g.ID)
	if err != nil || len(ps) != 3 {
		t.Fatalf("want owner plus two members, got %+v (%v)", ps, err)
	}
	roles := map[int64]Role{}
	for _, p := range ps {
		roles[p.UserID] = p.Role
	}
	if roles[owner.ID] != RoleOwner || roles[m1.ID] != RoleMember || roles[m2.ID] != RoleMember {
		t.Fatalf("roles: %v", roles)
	}

	list, err := s.Chats().ListForUser(ctx, m1.ID, 10)
	if err != nil || len(list) != 1 || list[0].ID != g.ID {
		t.Fatalf("list for member: %+v, %v", list, err)
	}
}

func TestCreateGroupRollsBack(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	owner := mustUser(t, s, "owner")

	if _, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "Team", MemberIDs: []int64{999999}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown member: got %v", err)
	}
	var chats int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM chats`).Scan(&chats); err != nil {
		t.Fatal(err)
	}
	if chats != 0 {
		t.Fatalf("the failed creation left %d chat rows", chats)
	}
	if _, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "  "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank name: got %v", err)
	}
}

func TestMembershipAndOwnership(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	chats := s.Chats()
	owner, a, b := mustUser(t, s, "owner"), mustUser(t, s, "alice"), mustUser(t, s, "bob")
	g, err := chats.CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "Team", MemberIDs: []int64{a.ID}})
	if err != nil {
		t.Fatal(err)
	}

	if err := chats.AddMember(ctx, g.ID, a.ID); !errors.Is(err, ErrAlreadyParticipant) {
		t.Fatalf("adding twice: got %v", err)
	}
	if err := chats.AddMember(ctx, g.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetRole(ctx, g.ID, a.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetRole(ctx, g.ID, a.ID, RoleOwner); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SetRole must not hand out ownership: got %v", err)
	}
	if err := chats.SetRole(ctx, g.ID, owner.ID, RoleMember); !errors.Is(err, ErrOwnerMustTransfer) {
		t.Fatalf("demoting the owner: got %v", err)
	}
	if err := chats.RemoveMember(ctx, g.ID, owner.ID); !errors.Is(err, ErrOwnerMustTransfer) {
		t.Fatalf("removing the owner: got %v", err)
	}
	if err := chats.TransferOwnership(ctx, g.ID, a.ID, b.ID); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("transfer by a non-owner: got %v", err)
	}

	if err := chats.TransferOwnership(ctx, g.ID, owner.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := chats.Participant(ctx, g.ID, a.ID); p.Role != RoleOwner {
		t.Fatalf("new owner has role %q", p.Role)
	}
	if p, _ := chats.Participant(ctx, g.ID, owner.ID); p.Role != RoleAdmin {
		t.Fatalf("previous owner has role %q", p.Role)
	}
	if err := chats.RemoveMember(ctx, g.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := chats.Participant(ctx, g.ID, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed member: got %v", err)
	}

	direct, _, err := chats.CreateDirect(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := chats.AddMember(ctx, direct.ID, owner.ID); !errors.Is(err, ErrNotGroup) {
		t.Fatalf("adding to a direct chat: got %v", err)
	}
}

func TestMarkReadOnlyMovesForward(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, b := mustUser(t, s, "alice"), mustUser(t, s, "bob")
	c, _, err := s.Chats().CreateDirect(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		id       int64
		advanced bool
	}{{50, true}, {20, false}, {50, false}, {60, true}} {
		advanced, err := s.Chats().MarkRead(ctx, c.ID, a.ID, step.id)
		if err != nil || advanced != step.advanced {
			t.Fatalf("read %d: advanced=%v %v", step.id, advanced, err)
		}
	}
	p, err := s.Chats().Participant(ctx, c.ID, a.ID)
	if err != nil || p.LastReadMessageID == nil || *p.LastReadMessageID != 60 {
		t.Fatalf("read marker: %+v, %v", p, err)
	}
	if _, err := s.Chats().MarkRead(ctx, c.ID, 999999, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not a participant: got %v", err)
	}
}

func TestAttachments(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, b := mustUser(t, s, "alice"), mustUser(t, s, "bob")
	c, _, err := s.Chats().CreateDirect(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}

	w, h, dur := int32(640), int32(480), 3.5
	att, err := s.Attachments().Create(ctx, NewAttachment{
		UploaderID: &a.ID, ChatID: &c.ID, StorageKey: "chats/1/a.png", MimeType: "image/png", Size: 1234,
		Width: &w, Height: &h, Duration: &dur, Waveform: []int16{1, 5, 9},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Attachments().Get(ctx, att.ID)
	if err != nil || got.StorageKey != "chats/1/a.png" || len(got.Waveform) != 3 || *got.Width != 640 {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if _, err := s.Attachments().Create(ctx, NewAttachment{ChatID: &c.ID, StorageKey: "chats/1/a.png", MimeType: "image/png", Size: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate storage key: got %v", err)
	}
	if _, err := s.Attachments().Create(ctx, NewAttachment{ChatID: &c.ID, StorageKey: "k", MimeType: "image/png", Size: -1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative size: got %v", err)
	}
	list, err := s.Attachments().ListByChat(ctx, c.ID, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	keys, err := s.Attachments().Delete(ctx, att.ID)
	if err != nil || len(keys) != 1 || keys[0] != "chats/1/a.png" {
		t.Fatalf("delete: %q, %v", keys, err)
	}
	if _, err := s.Attachments().Delete(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: got %v", err)
	}
}

func TestSecurityEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	ip := netip.MustParseAddr("203.0.113.7")
	ua := "test-agent"

	var ids []int64
	for _, typ := range []string{"login", "password_change", "logout"} {
		e, err := s.SecurityEvents().Record(ctx, NewSecurityEvent{UserID: u.ID, Type: typ, IP: &ip, UserAgent: &ua})
		if err != nil {
			t.Fatal(err)
		}
		if e.IP == nil || *e.IP != ip || string(e.Details) != "{}" {
			t.Fatalf("event: %+v", e)
		}
		ids = append(ids, e.ID)
	}
	page1, err := s.SecurityEvents().List(ctx, u.ID, nil, 2)
	if err != nil || len(page1) != 2 || page1[0].ID != ids[2] || page1[1].ID != ids[1] {
		t.Fatalf("first page: %+v, %v", page1, err)
	}
	page2, err := s.SecurityEvents().List(ctx, u.ID, &page1[1].ID, 2)
	if err != nil || len(page2) != 1 || page2[0].ID != ids[0] {
		t.Fatalf("second page: %+v, %v", page2, err)
	}
}

func TestDeleteAccount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	chats := s.Chats()
	victim, friend, third := mustUser(t, s, "victim"), mustUser(t, s, "friend"), mustUser(t, s, "third")

	direct, _, err := chats.CreateDirect(ctx, victim.ID, friend.ID)
	if err != nil {
		t.Fatal(err)
	}
	kept, _, err := chats.CreateDirect(ctx, friend.ID, third.ID)
	if err != nil {
		t.Fatal(err)
	}
	handedOver, err := chats.CreateGroup(ctx, NewGroup{OwnerID: victim.ID, Name: "Handed over", MemberIDs: []int64{friend.ID, third.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := chats.SetRole(ctx, handedOver.ID, third.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	alone, err := chats.CreateGroup(ctx, NewGroup{OwnerID: victim.ID, Name: "Alone"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := chats.CreateGroup(ctx, NewGroup{OwnerID: friend.ID, Name: "Friend's", MemberIDs: []int64{victim.ID}})
	if err != nil {
		t.Fatal(err)
	}

	atts := s.Attachments()
	inDirect, err := atts.Create(ctx, NewAttachment{UploaderID: &victim.ID, ChatID: &direct.ID, StorageKey: "k/direct", MimeType: "image/png", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	inAlone, err := atts.Create(ctx, NewAttachment{UploaderID: &victim.ID, ChatID: &alone.ID, StorageKey: "k/alone", MimeType: "image/png", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	inMember, err := atts.Create(ctx, NewAttachment{UploaderID: &victim.ID, ChatID: &member.ID, StorageKey: "k/member", MimeType: "image/png", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SecurityEvents().Record(ctx, NewSecurityEvent{UserID: victim.ID, Type: "login"}); err != nil {
		t.Fatal(err)
	}

	res, err := s.Users().DeleteAccount(ctx, victim.ID)
	if err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if len(res.AttachmentKeys) != 2 || !slices.Contains(res.AttachmentKeys, "k/direct") || !slices.Contains(res.AttachmentKeys, "k/alone") {
		t.Fatalf("storage keys to purge: %v", res.AttachmentKeys)
	}

	if _, err := s.Users().Get(ctx, victim.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user still there: %v", err)
	}
	for _, id := range []int64{direct.ID, alone.ID} {
		if _, err := chats.Get(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("chat %d should be gone: %v", id, err)
		}
	}
	for _, id := range []int64{kept.ID, handedOver.ID, member.ID} {
		if _, err := chats.Get(ctx, id); err != nil {
			t.Fatalf("chat %d should survive: %v", id, err)
		}
	}
	if p, err := chats.Participant(ctx, handedOver.ID, third.ID); err != nil || p.Role != RoleOwner {
		t.Fatalf("the admin should inherit the group: %+v, %v", p, err)
	}
	if _, err := chats.Participant(ctx, handedOver.ID, victim.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the leaver is still a participant: %v", err)
	}
	if _, err := atts.Get(ctx, inDirect.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attachment of a deleted chat: %v", err)
	}
	if _, err := atts.Get(ctx, inAlone.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attachment of a deleted group: %v", err)
	}
	if a, err := atts.Get(ctx, inMember.ID); err != nil || a.UploaderID != nil {
		t.Fatalf("attachment in a surviving chat must stay without uploader: %+v, %v", a, err)
	}
	var events int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM user_security_events`).Scan(&events); err != nil || events != 0 {
		t.Fatalf("security events left behind: %d, %v", events, err)
	}

	if _, err := s.Users().DeleteAccount(ctx, victim.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: got %v", err)
	}
}

func TestGroupNeedsName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `INSERT INTO chats (type) VALUES ('group')`); err == nil {
		t.Fatal("a group without a name must be rejected")
	}
}

// A transfer that races the deletion of its target must not leave a group with members and no owner.
func TestTransferRacesAccountDeletion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	chats := s.Chats()
	owner, a, b := mustUser(t, s, "owner"), mustUser(t, s, "alice"), mustUser(t, s, "bob")

	for i := range 15 {
		g, err := chats.CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "Race", MemberIDs: []int64{a.ID, b.ID}})
		if err != nil {
			t.Fatal(err)
		}
		var deleteErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = chats.TransferOwnership(ctx, g.ID, owner.ID, a.ID)
		}()
		go func() {
			defer wg.Done()
			_, deleteErr = s.Users().DeleteAccount(ctx, a.ID)
		}()
		wg.Wait()
		if deleteErr != nil {
			t.Fatalf("round %d: delete account: %v", i, deleteErr)
		}

		var owners, members int
		err = s.Pool().QueryRow(ctx,
			`SELECT count(*) FILTER (WHERE role = 'owner'), count(*) FROM participants WHERE chat_id = $1`, g.ID).Scan(&owners, &members)
		if err != nil {
			t.Fatal(err)
		}
		if members > 0 && owners != 1 {
			t.Fatalf("round %d: group has %d members and %d owners", i, members, owners)
		}
		// Bring the deleted user back for the next round.
		if a, err = s.Users().Create(ctx, fmt.Sprintf("alice%d", i), "Alice", "hash"); err != nil {
			t.Fatal(err)
		}
	}
}

// Each user created a group, handed it over and left, so only chats.created_by still points at them. Deleting
// both accounts at once must not deadlock on those rows.
func TestConcurrentDeletionOfCreators(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	chats := s.Chats()
	third := mustUser(t, s, "third")

	for i := range 10 {
		a, b := mustUser(t, s, fmt.Sprintf("alice%d", i)), mustUser(t, s, fmt.Sprintf("bob%d", i))
		for _, pair := range [][2]User{{a, b}, {b, a}} {
			creator, heir := pair[0], pair[1]
			g, err := chats.CreateGroup(ctx, NewGroup{OwnerID: creator.ID, Name: "Left", MemberIDs: []int64{heir.ID, third.ID}})
			if err != nil {
				t.Fatal(err)
			}
			if err := chats.TransferOwnership(ctx, g.ID, creator.ID, heir.ID); err != nil {
				t.Fatal(err)
			}
			if err := chats.RemoveMember(ctx, g.ID, creator.ID); err != nil {
				t.Fatal(err)
			}
		}

		errs := make([]error, 2)
		var wg sync.WaitGroup
		for j, u := range []User{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[j] = s.Users().DeleteAccount(ctx, u.ID)
			}()
		}
		wg.Wait()
		for j, err := range errs {
			if err != nil {
				t.Fatalf("round %d, deletion %d: %v", i, j, err)
			}
		}
	}
}

// A former creator rejoins the group while their account is deleted: AddMember must not take the chat lock
// before the user lock DeleteAccount already holds.
func TestAddMemberRacesAccountDeletion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	chats := s.Chats()
	heir, third := mustUser(t, s, "heir"), mustUser(t, s, "third")

	for i := range 10 {
		c := mustUser(t, s, fmt.Sprintf("creator%d", i))
		g, err := chats.CreateGroup(ctx, NewGroup{OwnerID: c.ID, Name: "Rejoin", MemberIDs: []int64{heir.ID, third.ID}})
		if err != nil {
			t.Fatal(err)
		}
		if err := chats.TransferOwnership(ctx, g.ID, c.ID, heir.ID); err != nil {
			t.Fatal(err)
		}
		if err := chats.RemoveMember(ctx, g.ID, c.ID); err != nil {
			t.Fatal(err)
		}

		var addErr, delErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			addErr = chats.AddMember(ctx, g.ID, c.ID)
		}()
		go func() {
			defer wg.Done()
			_, delErr = s.Users().DeleteAccount(ctx, c.ID)
		}()
		wg.Wait()
		if delErr != nil {
			t.Fatalf("round %d: delete account: %v", i, delErr)
		}
		if addErr != nil && !errors.Is(addErr, ErrNotFound) {
			t.Fatalf("round %d: add member: %v", i, addErr)
		}
	}
}

// Each user owns a one-member group that holds a file uploaded by the other user. Deleting both accounts at
// once makes each deletion rewrite uploader_id in the group the other deletion is removing.
func TestConcurrentDeletionOfCrossUploaders(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := range 10 {
		a, b := mustUser(t, s, fmt.Sprintf("alice%d", i)), mustUser(t, s, fmt.Sprintf("bob%d", i))
		for j, pair := range [][2]User{{a, b}, {b, a}} {
			owner, uploader := pair[0], pair[1]
			g, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "Solo"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Attachments().Create(ctx, NewAttachment{
				UploaderID: &uploader.ID, ChatID: &g.ID, StorageKey: fmt.Sprintf("k/%d/%d", i, j), MimeType: "image/png", Size: 1,
			}); err != nil {
				t.Fatal(err)
			}
		}

		errs := make([]error, 2)
		var wg sync.WaitGroup
		for j, u := range []User{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[j] = s.Users().DeleteAccount(ctx, u.ID)
			}()
		}
		wg.Wait()
		for j, err := range errs {
			if err != nil {
				t.Fatalf("round %d, deletion %d: %v", i, j, err)
			}
		}
	}
}

// deleteBoth deletes both accounts at the same time and fails the test if either deletion fails.
func deleteBoth(t *testing.T, s *Store, round int, a, b User) {
	t.Helper()
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for j, u := range []User{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[j] = s.Users().DeleteAccount(context.Background(), u.ID)
		}()
	}
	wg.Wait()
	for j, err := range errs {
		if err != nil {
			t.Fatalf("round %d, deletion %d: %v", round, j, err)
		}
	}
}

// Reciprocal rows (A blocks B, B blocks A) are removed by two cascades that cannot be ordered up front; the
// deletion has to survive the deadlock PostgreSQL may report.
func TestConcurrentDeletionWithReciprocalRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i := range 15 {
		a, b := mustUser(t, s, fmt.Sprintf("alice%d", i)), mustUser(t, s, fmt.Sprintf("bob%d", i))
		for _, q := range []string{
			`INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2), ($2, $1)`,
			`INSERT INTO user_contact_names (owner_id, target_id, display_name) VALUES ($1, $2, 'x'), ($2, $1, 'y')`,
		} {
			if _, err := s.Pool().Exec(ctx, q, a.ID, b.ID); err != nil {
				t.Fatal(err)
			}
		}
		deleteBoth(t, s, i, a, b)
		var left int
		if err := s.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM user_blocks) + (SELECT count(*) FROM user_contact_names)`).Scan(&left); err != nil || left != 0 {
			t.Fatalf("round %d: %d rows left (%v)", i, left, err)
		}
	}
}

// Each user owns a solo group and invited the other into it.
func TestConcurrentDeletionWithCrossInvitations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i := range 15 {
		a, b := mustUser(t, s, fmt.Sprintf("alice%d", i)), mustUser(t, s, fmt.Sprintf("bob%d", i))
		for _, pair := range [][2]User{{a, b}, {b, a}} {
			owner, guest := pair[0], pair[1]
			g, err := s.Chats().CreateGroup(ctx, NewGroup{OwnerID: owner.ID, Name: "Solo"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Pool().Exec(ctx,
				`INSERT INTO approval_requests (type, requester_id, recipient_id, chat_id) VALUES ('group_invite', $1, $2, $3)`,
				owner.ID, guest.ID, g.ID); err != nil {
				t.Fatal(err)
			}
		}
		deleteBoth(t, s, i, a, b)
	}
}

func TestDeleteChatReturnsAttachmentKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, b := mustUser(t, s, "alice"), mustUser(t, s, "bob")
	c, _, err := s.Chats().CreateDirect(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attachments().Create(ctx, NewAttachment{ChatID: &c.ID, StorageKey: "k/1", MimeType: "image/png", Size: 1}); err != nil {
		t.Fatal(err)
	}
	keys, err := s.Chats().Delete(ctx, c.ID)
	if err != nil || len(keys) != 1 || keys[0] != "k/1" {
		t.Fatalf("delete: %v, %v", keys, err)
	}
	if _, err := s.Chats().Delete(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: got %v", err)
	}
}

func TestQueryTimeout(t *testing.T) {
	s := newTestStore(t)
	short := newStore(s.Pool(), time.Nanosecond)
	_, err := short.Users().Get(context.Background(), 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want a deadline error", err)
	}
	err = short.inTx(context.Background(), func(context.Context, *sqlcdb.Queries) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transaction: got %v, want a deadline error", err)
	}
}

// The garbage collector must not delete an upload that a message is linking at the same moment.
func TestGarbageCollectionWaitsForALinkInFlight(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, b := mustUser(t, s, "gc_a"), mustUser(t, s, "gc_b")
	chat, _, err := s.Chats().CreateDirect(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	att, err := s.Attachments().Create(ctx, NewAttachment{UploaderID: &a.ID, Purpose: "message", Kind: "file", Filename: "f",
		StorageKey: "attachments/" + uuid.NewString(), MimeType: "application/octet-stream", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "INSERT INTO attachment_links (attachment_id, chat_id, message_id) VALUES ($1, $2, 1)", att.ID, chat.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		deleted bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		_, deleted, err := s.Attachments().DeleteIfUnreferenced(ctx, att.ID)
		done <- result{deleted, err}
	}()
	time.Sleep(300 * time.Millisecond) // the collector is waiting for the link's row lock
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || r.deleted {
		t.Fatalf("collector: deleted=%v err=%v", r.deleted, r.err)
	}
	if _, err := s.Attachments().Get(ctx, att.ID); err != nil {
		t.Fatalf("the linked upload is gone: %v", err)
	}
}
