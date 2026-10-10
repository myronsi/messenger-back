package search_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/search"
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

// access answers the searcher from the message service and the social repository.
type access struct {
	*messages.Service
	pg *postgres.Store
}

func (a access) ChatIDs(ctx context.Context, userID int64) ([]int64, error) {
	return a.pg.Social().ChatIDs(ctx, userID)
}

// eventLog hands every event straight to the indexer, like the worker would.
type eventLog struct {
	t  *testing.T
	in *search.Indexer
}

func (l eventLog) Emit(ctx context.Context, e events.Event) error {
	fields := map[string]string{}
	for k, v := range e.Fields() {
		switch x := v.(type) {
		case string:
			fields[k] = x
		case int64:
			fields[k] = strconv.FormatInt(x, 10)
		}
	}
	if err := l.in.Handle(ctx, redis.Entry{ID: "1-0", Fields: fields}); err != nil {
		l.t.Errorf("index %s: %v", e.Type, err)
	}
	return nil
}

type env struct {
	t        *testing.T
	pg       *postgres.Store
	svc      *messages.Service
	ix       *search.Index
	in       *search.Indexer
	searcher *search.Searcher
	repo     *scylla.Messages
}

func newEnv(t *testing.T) *env {
	t.Helper()
	es, alias := testenv.Elastic(t)
	pg := testenv.Postgres(t)
	rd, prefix := testenv.Redis(t)
	repo := scylla.NewMessages(testenv.Scylla(t), 10*time.Second)
	ix := search.NewIndex(es, alias, 0)
	if err := ix.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	in := search.NewIndexer(ix, repo, pg.Attachments(), nil).WithSettle(0)
	gen, _ := ids.NewGenerator(3)
	svc := messages.New(messages.Deps{
		Messages: repo, Store: pg, Members: redis.NewMembers(rd.Client(), prefix, time.Minute), Unread: redis.NewUnread(rd.Client(), prefix),
		Dedup: redis.NewDedup(rd.Client(), prefix, 0), IDs: gen, Events: eventLog{t: t, in: in},
	})
	return &env{t: t, pg: pg, svc: svc, ix: ix, in: in, searcher: search.NewSearcher(ix, access{svc, pg}, repo), repo: repo}
}

func (e *env) user(name string) int64 {
	u, err := e.pg.Users().Register(context.Background(), name, name, "hash")
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

func (e *env) chat(a, b int64) int64 {
	c, _, err := e.pg.Chats().CreateDirect(context.Background(), a, b)
	if err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

func (e *env) send(chat, from int64, text string) scylla.Message {
	e.t.Helper()
	s, err := e.svc.Send(context.Background(), messages.SendRequest{ChatID: chat, SenderID: from, ClientTempID: text, Type: scylla.TypeText, Content: &text})
	if err != nil {
		e.t.Fatal(err)
	}
	return s.Message
}

func (e *env) find(user int64, q search.Query) []search.Hit {
	e.t.Helper()
	if err := e.ix.Refresh(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	hits, _, err := e.searcher.Search(context.Background(), user, q)
	if err != nil {
		e.t.Fatal(err)
	}
	return hits
}

func contents(hits []search.Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		if h.Message.Content != nil {
			out[i] = *h.Message.Content
		}
	}
	return out
}

func TestSearchFollowsAccessAndChanges(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice, bob, carol := e.user("alice"), e.user("bob"), e.user("carol")
	ab, ac := e.chat(alice, bob), e.chat(alice, carol)
	m1 := e.send(ab, alice, "Hello Bob, the meeting is at noon")
	e.send(ac, alice, "hello carol, meetings everywhere")

	// Each user finds only the messages of their own chats; stemming finds "meetings" for "meeting".
	if got := contents(e.find(bob, search.Query{Text: "hello"})); len(got) != 1 || !strings.Contains(got[0], "Bob") {
		t.Fatalf("bob: %v", got)
	}
	if got := contents(e.find(carol, search.Query{Text: "meeting"})); len(got) != 1 || !strings.Contains(got[0], "carol") {
		t.Fatalf("carol: %v", got)
	}
	if got := e.find(alice, search.Query{Text: "hello"}); len(got) != 2 {
		t.Fatalf("alice: %v", contents(got))
	}
	hits := e.find(alice, search.Query{Text: "noon", ChatID: ab})
	if len(hits) != 1 || hits[0].Highlight == nil || !strings.Contains(*hits[0].Highlight, search.MarkStart+"noon"+search.MarkEnd) {
		t.Fatalf("highlight: %+v", hits)
	}
	if _, _, err := e.searcher.Search(ctx, carol, search.Query{Text: "hello", ChatID: ab}); err == nil {
		t.Fatal("carol searched a chat she is not in")
	}

	// An edit replaces the text in the index.
	if _, err := e.svc.Edit(ctx, alice, ab, m1.ID, "Hello Bob, lunch instead"); err != nil {
		t.Fatal(err)
	}
	if got := e.find(bob, search.Query{Text: "noon"}); len(got) != 0 {
		t.Fatalf("old text still found: %v", contents(got))
	}
	if got := e.find(bob, search.Query{Text: "lunch"}); len(got) != 1 {
		t.Fatalf("new text not found: %v", contents(got))
	}
	// Deleted for bob only: alice still finds it.
	if err := e.svc.Delete(ctx, bob, ab, m1.ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	if got := e.find(bob, search.Query{Text: "lunch"}); len(got) != 0 {
		t.Fatalf("bob finds what he deleted: %v", contents(got))
	}
	if got := e.find(alice, search.Query{Text: "lunch"}); len(got) != 1 {
		t.Fatalf("alice lost it: %v", contents(got))
	}
	// Deleted for everyone.
	if err := e.svc.Delete(ctx, alice, ab, m1.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if got := e.find(alice, search.Query{Text: "lunch"}); len(got) != 0 {
		t.Fatalf("deleted message found: %v", contents(got))
	}

	// A lagging index never shows a deleted message: the store is checked again.
	m3 := e.send(ac, carol, "secret plans")
	if err := e.repo.Delete(ctx, ac, m3.ID); err != nil { // behind the indexer's back
		t.Fatal(err)
	}
	if got := e.find(alice, search.Query{Text: "secret"}); len(got) != 0 {
		t.Fatalf("lagging index: %v", contents(got))
	}
}

func TestSearchPagesAndRebuild(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice, bob := e.user("alice"), e.user("bob")
	ab := e.chat(alice, bob)
	var sent []scylla.Message
	for _, s := range []string{"report one", "report two", "report three"} {
		sent = append(sent, e.send(ab, alice, s))
	}
	if err := e.svc.Delete(ctx, bob, ab, sent[0].ID, messages.ScopeMe); err != nil {
		t.Fatal(err)
	}
	if err := e.ix.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	page, next, err := e.searcher.Search(ctx, alice, search.Query{Text: "report", Limit: 2})
	if err != nil || len(page) != 2 || next == "" || *page[0].Message.Content != "report three" {
		t.Fatalf("first page: %v %q %v", contents(page), next, err)
	}
	rest, end, err := e.searcher.Search(ctx, alice, search.Query{Text: "report", Limit: 2, After: next})
	if err != nil || len(rest) != 1 || end != "" || *rest[0].Message.Content != "report one" {
		t.Fatalf("second page: %v %q %v", contents(rest), end, err)
	}

	// Rebuilding from the message store gives the same answers, deleted-for-me included.
	old, _ := e.ix.Current(ctx)
	name, err := e.in.Rebuild(ctx, e.pg.Chats())
	if err != nil {
		t.Fatal(err)
	}
	if cur, _ := e.ix.Current(ctx); cur != name || cur == old {
		t.Fatalf("alias points to %q, rebuilt %q (was %q)", cur, name, old)
	}
	if got := e.find(alice, search.Query{Text: "report"}); len(got) != 3 {
		t.Fatalf("after the rebuild alice finds %v", contents(got))
	}
	if got := e.find(bob, search.Query{Text: "report"}); len(got) != 2 {
		t.Fatalf("after the rebuild bob finds %v", contents(got))
	}
	if _, _, err := e.searcher.Search(ctx, alice, search.Query{Text: "x"}); err == nil {
		t.Fatal("one-character query accepted")
	}
	// Writes after the switch land in the new index.
	if _, err := e.svc.Edit(ctx, alice, ab, sent[1].ID, "summary two"); err != nil {
		t.Fatal(err)
	}
	if got := e.find(alice, search.Query{Text: "summary"}); len(got) != 1 {
		t.Fatalf("edit after the rebuild: %v", contents(got))
	}
	// One rebuild at a time: the rebuild alias is the lock, and a failed start leaves no index behind.
	if err := e.ix.Lock(ctx, name); err != nil {
		t.Fatal(err)
	}
	if _, err := e.in.Rebuild(ctx, e.pg.Chats()); !errors.Is(err, search.ErrRebuilding) {
		t.Fatalf("second rebuild: %v", err)
	}
	if names, _ := e.ix.Indices(ctx); len(names) != 1 {
		t.Fatalf("indices: %v", names)
	}
}
