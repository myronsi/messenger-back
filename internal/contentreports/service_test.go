package contentreports_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/contentreports"
	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/moderation"
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

type counter struct{ got []string }

func (c *counter) ContentReport(kind, reason string) { c.got = append(c.got, kind+":"+reason) }

type fixture struct {
	t       *testing.T
	pg      *postgres.Store
	msgs    *messages.Service
	svc     *contentreports.Service
	metrics *counter
	now     time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pg := testenv.Postgres(t)
	rd, prefix := testenv.Redis(t)
	gen, _ := ids.NewGenerator(1)
	msgs := messages.New(messages.Deps{
		Messages: scylla.NewMessages(testenv.Scylla(t), 10*time.Second), Store: pg,
		Members: redis.NewMembers(rd.Client(), prefix, time.Minute), Unread: redis.NewUnread(rd.Client(), prefix),
		Dedup: redis.NewDedup(rd.Client(), prefix, 0), IDs: gen,
	})
	f := &fixture{t: t, pg: pg, msgs: msgs, metrics: &counter{}, now: time.Now()}
	f.svc = contentreports.New(contentreports.Deps{Store: pg, Messages: msgs, Metrics: f.metrics, Now: func() time.Time { return f.now }})
	return f
}

func (f *fixture) user(name string) int64 {
	f.t.Helper()
	u, err := f.pg.Users().Register(context.Background(), name, name, "hash")
	if err != nil {
		f.t.Fatal(err)
	}
	return u.ID
}

func (f *fixture) send(chat, sender int64, text string) scylla.Message {
	f.t.Helper()
	s, err := f.msgs.Send(context.Background(), messages.SendRequest{ChatID: chat, SenderID: sender, ClientTempID: uuid.NewString(), Type: scylla.TypeText, Content: &text})
	if err != nil {
		f.t.Fatal(err)
	}
	return s.Message
}

func (f *fixture) copyOf(reporter, chatID, messageID int64) (postgres.ContentReport, contentreports.Copy) {
	f.t.Helper()
	r, err := f.pg.ContentReports().Get(context.Background(), reporter, chatID, messageID)
	if err != nil {
		f.t.Fatal(err)
	}
	var c contentreports.Copy
	if err := json.Unmarshal(r.Content, &c); err != nil {
		f.t.Fatal(err)
	}
	return r, c
}

func str(s string) *string { return &s }

func TestReportMessageCopiesTheMessageAndItsContext(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	group, err := f.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: alice, Name: "Team", MemberIDs: []int64{bob}})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		f.send(group.ID, bob, fmt.Sprint("before ", i))
	}
	upload, err := f.pg.Attachments().Create(ctx, postgres.NewAttachment{UploaderID: &alice, ChatID: &group.ID, StorageKey: "attachments/" + uuid.NewString(), Filename: "offer.pdf", MimeType: "application/pdf", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	caption := "cheap pills"
	sent, err := f.msgs.Send(ctx, messages.SendRequest{ChatID: group.ID, SenderID: alice, ClientTempID: uuid.NewString(), Type: scylla.TypeFile, Content: &caption, AttachmentID: &upload.ID})
	if err != nil {
		t.Fatal(err)
	}
	reported := sent.Message
	for i := range 2 {
		f.send(group.ID, bob, fmt.Sprint("after ", i))
	}
	if err := f.svc.ReportMessage(ctx, bob, group.ID, reported.ID, "scam", str("  sells fake medicine  ")); err != nil {
		t.Fatal(err)
	}
	r, c := f.copyOf(bob, group.ID, reported.ID)
	if r.Kind != postgres.ContentReportMessage || r.TargetID == nil || *r.TargetID != alice || r.Reason != "scam" || r.Note == nil || *r.Note != "sells fake medicine" {
		t.Fatalf("report: %+v", r)
	}
	if c.Chat.Type != "group" || c.Chat.Title == nil || *c.Chat.Title != "Team" || len(c.Messages) != 8 {
		t.Fatalf("copy: %+v", c)
	}
	if c.Messages[0].Text == nil || *c.Messages[0].Text != "before 1" || *c.Messages[7].Text != "after 1" {
		t.Fatalf("context: first %v last %v", *c.Messages[0].Text, *c.Messages[7].Text)
	}
	hit := c.Messages[5]
	if !hit.Reported || hit.SenderName != "alice" || hit.Text == nil || *hit.Text != caption || len(hit.Files) != 1 || hit.Files[0] != "offer.pdf" {
		t.Fatalf("reported message: %+v", hit)
	}
	for i, m := range c.Messages {
		if m.Reported != (i == 5) {
			t.Fatalf("only the reported message is marked: %d", i)
		}
	}
	f.send(group.ID, alice, "later")
	if err := f.svc.ReportMessage(ctx, bob, group.ID, reported.ID, "spam", nil); err != nil {
		t.Fatal(err)
	}
	again, againCopy := f.copyOf(bob, group.ID, reported.ID)
	if again.ID != r.ID || again.Reason != "spam" || again.Note != nil || len(againCopy.Messages) != 8 {
		t.Fatalf("reporting again keeps the first copy: %+v %d", again, len(againCopy.Messages))
	}
	if err := f.svc.Withdraw(ctx, bob, group.ID, reported.ID); err != nil {
		t.Fatal(err)
	}
	if w, _ := f.copyOf(bob, group.ID, reported.ID); w.WithdrawnAt == nil {
		t.Fatal("not withdrawn")
	}
	if len(f.metrics.got) != 1 || f.metrics.got[0] != "message:scam" {
		t.Fatalf("metrics: %v", f.metrics.got)
	}
}

func TestReportMessageRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	m := f.send(chat.ID, alice, "hello")
	var field *moderation.FieldError
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, m.ID, "illegal", nil); !errors.As(err, &field) || field.Field != "note" {
		t.Fatalf("illegal without a note: %v", err)
	}
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, m.ID, "illegal", str("   ")); !errors.As(err, &field) || field.Field != "note" {
		t.Fatalf("illegal with a blank note: %v", err)
	}
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, m.ID, "rude", nil); !errors.As(err, &field) || field.Field != "reason" {
		t.Fatalf("unknown reason: %v", err)
	}
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, m.ID, "other", str(strings.Repeat("x", 1001))); !errors.As(err, &field) {
		t.Fatalf("long note: %v", err)
	}
	if err := f.svc.ReportMessage(ctx, alice, chat.ID, m.ID, "spam", nil); !errors.As(err, &field) || field.Field != "message_id" {
		t.Fatalf("own message: %v", err)
	}
	if err := f.svc.ReportMessage(ctx, carol, chat.ID, m.ID, "spam", nil); !errors.Is(err, moderation.ErrNotFound) {
		t.Fatalf("not a member: %v", err)
	}
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, m.ID+1, "spam", nil); !errors.Is(err, moderation.ErrNotFound) {
		t.Fatalf("unknown message: %v", err)
	}
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, m.ID, "illegal", str("Threatens violence, a crime under section 241 StGB.")); err != nil {
		t.Fatal(err)
	}
	r, c := f.copyOf(bob, chat.ID, m.ID)
	if r.Reason != "illegal" || c.Chat.Title != nil || len(c.Messages) != 1 {
		t.Fatalf("direct chat report: %+v %+v", r, c)
	}
	if err := f.msgs.Delete(ctx, alice, chat.ID, m.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, m.ID, "spam", nil); !errors.Is(err, moderation.ErrNotFound) {
		t.Fatalf("deleted message: %v", err)
	}
}

func TestReportGroupCopiesTheGroupProfile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	group, err := f.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: alice, Name: "Free money", Description: "Send us your card", MemberIDs: []int64{bob}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReportGroup(ctx, carol, group.ID, "scam", nil); !errors.Is(err, moderation.ErrNotFound) {
		t.Fatalf("not a member: %v", err)
	}
	direct, _, _ := f.pg.Chats().CreateDirect(ctx, bob, carol)
	if err := f.svc.ReportGroup(ctx, bob, direct.ID, "scam", nil); !errors.Is(err, moderation.ErrNotFound) {
		t.Fatalf("direct chat: %v", err)
	}
	if err := f.svc.ReportGroup(ctx, bob, group.ID, "scam", str("asks for card numbers")); err != nil {
		t.Fatal(err)
	}
	r, c := f.copyOf(bob, group.ID, 0)
	if r.Kind != postgres.ContentReportGroup || r.TargetID == nil || *r.TargetID != alice || c.Group == nil {
		t.Fatalf("group report: %+v %+v", r, c)
	}
	if c.Group.Title != "Free money" || c.Group.Description != "Send us your card" || c.Group.MemberCount != 2 || c.Group.OwnerID == nil || *c.Group.OwnerID != fmt.Sprint(alice) {
		t.Fatalf("group copy: %+v", c.Group)
	}
	if err := f.svc.Withdraw(ctx, bob, group.ID, 0); err != nil {
		t.Fatal(err)
	}
}

func TestReportsShareTheDailyLimitWithUserReports(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	var sent []scylla.Message
	for i := range moderation.MaxReportsPerDay + 1 {
		sent = append(sent, f.send(chat.ID, alice, fmt.Sprint("spam ", i)))
	}
	for i := range moderation.MaxReportsPerDay - 1 {
		if err := f.svc.ReportMessage(ctx, bob, chat.ID, sent[i].ID, "spam", nil); err != nil {
			t.Fatalf("report %d: %v", i, err)
		}
	}
	mod := moderation.New(moderation.Deps{Store: f.pg})
	if err := mod.Report(ctx, bob, alice, "spam", nil, false); err != nil {
		t.Fatalf("the 20th report is a user report: %v", err)
	}
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, sent[moderation.MaxReportsPerDay].ID, "spam", nil); !errors.Is(err, moderation.ErrRateLimited) {
		t.Fatalf("21st report: %v", err)
	}
	if err := mod.Report(ctx, bob, f.user("carol"), "spam", nil, false); !errors.Is(err, moderation.ErrRateLimited) {
		t.Fatalf("user report after 19 message reports and 1 user report: %v", err)
	}
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, sent[0].ID, "scam", nil); err != nil {
		t.Fatalf("changing an existing report stays within the limit: %v", err)
	}
	if _, err := f.pg.ContentReports().Get(ctx, bob, chat.ID, sent[moderation.MaxReportsPerDay].ID); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("the refused report was stored: %v", err)
	}
	f.now = f.now.Add(25 * time.Hour)
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, sent[moderation.MaxReportsPerDay].ID, "spam", nil); err != nil {
		t.Fatalf("next day: %v", err)
	}
}

func TestPurgeDeletesCopiesAfterSixMonths(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	old, recent := f.send(chat.ID, alice, "old"), f.send(chat.ID, alice, "recent")
	start := f.now
	f.now = start.AddDate(0, -6, -1)
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, old.ID, "spam", nil); err != nil {
		t.Fatal(err)
	}
	f.now = start.AddDate(0, -6, 1)
	if err := f.svc.ReportMessage(ctx, bob, chat.ID, recent.ID, "spam", nil); err != nil {
		t.Fatal(err)
	}
	f.now = start
	n, err := f.svc.Purge(ctx)
	if err != nil || n < 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if _, err := f.pg.ContentReports().Get(ctx, bob, chat.ID, old.ID); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("old copy kept: %v", err)
	}
	if _, err := f.pg.ContentReports().Get(ctx, bob, chat.ID, recent.ID); err != nil {
		t.Fatalf("recent copy deleted: %v", err)
	}
}
