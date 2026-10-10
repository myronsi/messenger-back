package scylla

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gocql/gocql"

	"github.com/myronsi/messenger-back/internal/ids"
)

var (
	testOnce     sync.Once
	testKeyspace string
	testErr      error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if testKeyspace != "" {
		dropKeyspace(strings.Split(os.Getenv("TEST_SCYLLA_HOSTS"), ","), testKeyspace)
	}
	os.Exit(code)
}

func dropKeyspace(hosts []string, ks string) {
	cluster := gocql.NewCluster(hosts...)
	cluster.Timeout = 30 * time.Second
	if sess, err := cluster.CreateSession(); err == nil {
		_ = sess.Query(`DROP KEYSPACE IF EXISTS ` + ks).Exec()
		sess.Close()
	}
}

// testRepo connects to TEST_SCYLLA_HOSTS, or skips. The package's tests share one keyspace with the
// migrations applied (schema changes are slow); each test uses chat and user ids of its own.
func testRepo(t *testing.T) *Messages {
	t.Helper()
	hosts := os.Getenv("TEST_SCYLLA_HOSTS")
	if hosts == "" {
		t.Skip("TEST_SCYLLA_HOSTS is not set")
	}
	testOnce.Do(func() { testKeyspace, testErr = createKeyspace(strings.Split(hosts, ",")) })
	if testErr != nil {
		t.Fatalf("scylla: %v", testErr)
	}
	s := New(strings.Split(hosts, ","), testKeyspace, Options{})
	t.Cleanup(func() { _ = s.Close() })
	return NewMessages(s, 10*time.Second)
}

func createKeyspace(hosts []string) (string, error) {
	cluster := gocql.NewCluster(hosts...)
	cluster.Timeout = 30 * time.Second
	cluster.ConnectTimeout = 10 * time.Second
	sess, err := cluster.CreateSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	ks := fmt.Sprintf("test_%d", time.Now().UnixNano())
	if err := sess.Query(`CREATE KEYSPACE ` + ks + ` WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': 1}`).Exec(); err != nil {
		return "", err
	}
	cluster.Keyspace = ks
	ksSess, err := cluster.CreateSession()
	if err != nil {
		return "", err
	}
	defer ksSess.Close()
	files, err := filepath.Glob("../../../migrations/scylla/*.up.cql")
	if err != nil || len(files) == 0 {
		return "", fmt.Errorf("no migrations found: %w", err)
	}
	slices.Sort(files)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		for _, stmt := range strings.Split(string(raw), ";") {
			if strings.TrimSpace(stripComments(stmt)) == "" {
				continue
			}
			if err := ksSess.Query(stmt).Exec(); err != nil {
				return "", fmt.Errorf("%s: %w", filepath.Base(f), err)
			}
		}
	}
	return ks, nil
}

func stripComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			b.WriteString(line)
		}
	}
	return b.String()
}

func newChat() int64 { return rand.Int64N(1<<40) + 1 }

func text(s string) *string { return &s }

// seed stores n text messages from sender into the chat, spread over several buckets (one every gap),
// and returns their ids, oldest first.
func seed(t *testing.T, r *Messages, chatID, sender int64, n int, start time.Time, gap time.Duration) []int64 {
	t.Helper()
	out := make([]int64, n)
	for i := range n {
		at := start.Add(time.Duration(i) * gap)
		id := ids.MinAt(at) + int64(i%4096)
		if err := r.Insert(context.Background(), Message{
			ChatID: chatID, ID: id, SenderID: &sender, Type: TypeText, Content: text(fmt.Sprint(i)), CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
		out[i] = id
	}
	return out
}

func idsOf(ms []Message) []int64 {
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func TestInsertGetAndLocate(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat, sender := newChat(), int64(7)
	g, _ := ids.NewGenerator(1)
	id, _ := g.Next()
	att := gocql.TimeUUID()
	reply := int64(5)
	m := Message{
		ChatID: chat, ID: id, SenderID: &sender, Type: TypeFile, Content: text("caption"), AttachmentID: &att,
		ReplyTo: &reply, Forwarded: &Forwarded{MessageID: 3, SenderName: "Alice"}, CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := r.Insert(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := r.Insert(ctx, m); err != nil { // idempotent
		t.Fatal(err)
	}
	got, err := r.Get(ctx, chat, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeFile || *got.Content != "caption" || *got.AttachmentID != att || *got.ReplyTo != 5 ||
		got.Forwarded == nil || got.Forwarded.MessageID != 3 || got.Forwarded.SenderID != nil || got.Forwarded.SenderName != "Alice" ||
		!got.CreatedAt.Equal(m.CreatedAt) || got.Deleted || got.EditedAt != nil || *got.SenderID != sender {
		t.Fatalf("round trip: %+v", got)
	}
	if c, err := r.Locate(ctx, id); err != nil || c != chat {
		t.Fatalf("locate: %d %v", c, err)
	}
	if _, err := r.Get(ctx, chat+1, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message found in another chat: %v", err)
	}
	if _, err := r.Locate(ctx, id+1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("locate unknown: %v", err)
	}
	if err := r.Insert(ctx, Message{ChatID: chat, ID: id, Type: "html", CreatedAt: time.Now()}); err == nil {
		t.Fatal("accepted an unknown type")
	}
}

func TestPagingAcrossBuckets(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat := newChat()
	// 30 messages, one every 2 days: 6 buckets, with gaps.
	all := seed(t, r, chat, 1, 30, time.Now().Add(-90*24*time.Hour), 3*24*time.Hour)

	p, err := r.Page(ctx, PageQuery{ChatID: chat, Viewer: 2, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(idsOf(p.Messages), all[20:]) || !p.HasOlder || p.HasNewer {
		t.Fatalf("newest: %v older=%v newer=%v", idsOf(p.Messages), p.HasOlder, p.HasNewer)
	}
	p, _ = r.Page(ctx, PageQuery{ChatID: chat, Viewer: 2, Limit: 10, Before: all[20]})
	if !slices.Equal(idsOf(p.Messages), all[10:20]) || !p.HasOlder || !p.HasNewer {
		t.Fatalf("before: %v", idsOf(p.Messages))
	}
	p, _ = r.Page(ctx, PageQuery{ChatID: chat, Viewer: 2, Limit: 10, Before: all[5]})
	if !slices.Equal(idsOf(p.Messages), all[:5]) || p.HasOlder {
		t.Fatalf("oldest: %v older=%v", idsOf(p.Messages), p.HasOlder)
	}
	p, _ = r.Page(ctx, PageQuery{ChatID: chat, Viewer: 2, Limit: 10, After: all[3]})
	if !slices.Equal(idsOf(p.Messages), all[4:14]) || !p.HasNewer {
		t.Fatalf("after: %v", idsOf(p.Messages))
	}
	p, _ = r.Page(ctx, PageQuery{ChatID: chat, Viewer: 2, Limit: 10, After: all[25]})
	if !slices.Equal(idsOf(p.Messages), all[26:]) || p.HasNewer {
		t.Fatalf("after, last page: %v newer=%v", idsOf(p.Messages), p.HasNewer)
	}
	p, err = r.Page(ctx, PageQuery{ChatID: chat, Viewer: 2, Limit: 5, Around: all[15]})
	if err != nil || !slices.Equal(idsOf(p.Messages), all[13:18]) || !p.HasOlder || !p.HasNewer {
		t.Fatalf("around: %v %v", idsOf(p.Messages), err)
	}
	// Near the ends the window moves over, so the page stays full.
	p, _ = r.Page(ctx, PageQuery{ChatID: chat, Viewer: 2, Limit: 5, Around: all[1]})
	if !slices.Equal(idsOf(p.Messages), all[0:5]) || p.HasOlder || !p.HasNewer {
		t.Fatalf("around the second oldest: %v older=%v newer=%v", idsOf(p.Messages), p.HasOlder, p.HasNewer)
	}
	p, _ = r.Page(ctx, PageQuery{ChatID: chat, Viewer: 2, Limit: 5, Around: all[29]})
	if !slices.Equal(idsOf(p.Messages), all[25:30]) || !p.HasOlder || p.HasNewer {
		t.Fatalf("around the newest: %v older=%v newer=%v", idsOf(p.Messages), p.HasOlder, p.HasNewer)
	}
	// A hidden target leaves its slot to the others.
	if err := r.Hide(ctx, 9, chat, all[1], HiddenDeletedForMe); err != nil {
		t.Fatal(err)
	}
	p, _ = r.Page(ctx, PageQuery{ChatID: chat, Viewer: 9, Limit: 5, Around: all[1]})
	if !slices.Equal(idsOf(p.Messages), []int64{all[0], all[2], all[3], all[4], all[5]}) {
		t.Fatalf("around a hidden message: %v", idsOf(p.Messages))
	}
	if _, err := r.Page(ctx, PageQuery{ChatID: chat, Around: all[15] + 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("around a missing message: %v", err)
	}
	if _, err := r.Page(ctx, PageQuery{ChatID: chat, Before: 1, After: 2}); err == nil {
		t.Fatal("accepted two cursors")
	}
	empty, err := r.Page(ctx, PageQuery{ChatID: newChat()})
	if err != nil || len(empty.Messages) != 0 || empty.HasOlder {
		t.Fatalf("empty chat: %+v %v", empty, err)
	}
}

func TestHiddenMessagesAreLeftOut(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat := newChat()
	all := seed(t, r, chat, 1, 40, time.Now().Add(-time.Hour), time.Second)
	// The viewer deleted 25 of the newest messages for themselves; the page still fills up.
	for _, id := range all[15:40] {
		if err := r.Hide(ctx, 2, chat, id, HiddenDeletedForMe); err != nil {
			t.Fatal(err)
		}
	}
	p, err := r.Page(ctx, PageQuery{ChatID: chat, Viewer: 2, Limit: 10})
	if err != nil || !slices.Equal(idsOf(p.Messages), all[5:15]) {
		t.Fatalf("viewer 2: %v %v", idsOf(p.Messages), err)
	}
	// Others still see them.
	p, _ = r.Page(ctx, PageQuery{ChatID: chat, Viewer: 3, Limit: 10})
	if !slices.Equal(idsOf(p.Messages), all[30:]) {
		t.Fatalf("viewer 3: %v", idsOf(p.Messages))
	}

	// Undelivered messages become visible on delivery; deleted-for-me ones stay hidden.
	if err := r.Hide(ctx, 4, chat, all[39], HiddenNotDelivered); err != nil {
		t.Fatal(err)
	}
	if err := r.Hide(ctx, 4, chat, all[38], HiddenDeletedForMe); err != nil {
		t.Fatal(err)
	}
	for _, id := range all[38:] {
		if err := r.Unhide(ctx, 4, chat, id); err != nil {
			t.Fatal(err)
		}
	}
	p, _ = r.Page(ctx, PageQuery{ChatID: chat, Viewer: 4, Limit: 2})
	if !slices.Equal(idsOf(p.Messages), []int64{all[37], all[39]}) {
		t.Fatalf("viewer 4: %v", idsOf(p.Messages))
	}
	if err := r.Hide(ctx, 4, chat, all[0], "whatever"); err == nil {
		t.Fatal("accepted an unknown reason")
	}
}

func TestEditAndDelete(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat := newChat()
	id := seed(t, r, chat, 1, 1, time.Now(), 0)[0]
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := r.Edit(ctx, chat, id, "edited", at); err != nil {
		t.Fatal(err)
	}
	m, _ := r.Get(ctx, chat, id)
	if *m.Content != "edited" || m.EditedAt == nil || !m.EditedAt.Equal(at) {
		t.Fatalf("after edit: %+v", m)
	}
	if err := r.Delete(ctx, chat, id); err != nil {
		t.Fatal(err)
	}
	m, _ = r.Get(ctx, chat, id)
	if !m.Deleted || m.Content != nil || m.AttachmentID != nil {
		t.Fatalf("after delete: %+v", m)
	}
	if err := r.Edit(ctx, chat, id, "back", at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit of a deleted message: %v", err)
	}
	p, _ := r.Page(ctx, PageQuery{ChatID: chat})
	if len(p.Messages) != 1 || !p.Messages[0].Deleted {
		t.Fatal("a deleted message keeps its place")
	}
	if err := r.Edit(ctx, chat, id+1, "x", at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit of a missing message: %v", err)
	}
	if err := r.Delete(ctx, chat, id+1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete of a missing message: %v", err)
	}
}

// Edits racing a delete for everyone never bring the text back.
func TestEditRacingDelete(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat := newChat()
	id := seed(t, r, chat, 1, 1, time.Now(), 0)[0]
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() { _ = r.Edit(ctx, chat, id, fmt.Sprint("edit ", i), time.Now()) })
	}
	wg.Go(func() {
		if err := r.Delete(ctx, chat, id); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	m, _ := r.Get(ctx, chat, id)
	if !m.Deleted {
		t.Fatal("not deleted")
	}
	// Read the raw column: scanMessage hides the content of deleted rows anyway.
	sess, err := r.s.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var content *string
	if err := sess.Query(`SELECT content FROM messages WHERE chat_id = ? AND bucket = ? AND message_id = ?`,
		chat, BucketOf(ids.Time(id)), id).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != nil {
		t.Fatalf("text survived the delete: %q", *content)
	}
}

// Two users reacting at the same moment never overwrite each other.
func TestConcurrentReactions(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat := newChat()
	msgs := seed(t, r, chat, 1, 2, time.Now(), time.Millisecond)
	var wg sync.WaitGroup
	for uid := int64(1); uid <= 20; uid++ {
		wg.Go(func() {
			if err := r.AddReaction(ctx, chat, msgs[0], uid, "👍", time.Now()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := r.AddReaction(ctx, chat, msgs[0], 1, "🎉", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.AddReaction(ctx, chat, msgs[1], 2, "👀", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.RemoveReaction(ctx, chat, msgs[0], 20, "👍"); err != nil {
		t.Fatal(err)
	}
	got, err := r.Reactions(ctx, chat, msgs)
	if err != nil {
		t.Fatal(err)
	}
	thumbs := 0
	for _, rx := range got[msgs[0]] {
		if rx.Emoji == "👍" {
			thumbs++
		}
	}
	if thumbs != 19 || len(got[msgs[0]]) != 20 || len(got[msgs[1]]) != 1 {
		t.Fatalf("reactions: %d thumbs, %d on the first, %d on the second", thumbs, len(got[msgs[0]]), len(got[msgs[1]]))
	}
	if none, err := r.Reactions(ctx, chat, nil); err != nil || len(none) != 0 {
		t.Fatal("reactions of no messages")
	}
}

func TestV1MessagesSortBeforeSnowflakes(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat := newChat()
	sender := int64(1)
	old := time.Now().Add(-400 * 24 * time.Hour)
	// Imported v1 messages: small serial ids, buckets from their creation time.
	for i, id := range []int64{101, 102, 103} {
		if err := r.Insert(ctx, Message{ChatID: chat, ID: id, SenderID: &sender, Type: TypeText, Content: text("v1"), CreatedAt: old.Add(time.Duration(i) * 30 * 24 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	fresh := seed(t, r, chat, 1, 2, time.Now(), time.Millisecond)
	p, err := r.Page(ctx, PageQuery{ChatID: chat})
	if err != nil || !slices.Equal(idsOf(p.Messages), append([]int64{101, 102, 103}, fresh...)) {
		t.Fatalf("mixed history: %v %v", idsOf(p.Messages), err)
	}
	p, _ = r.Page(ctx, PageQuery{ChatID: chat, Before: 103})
	if !slices.Equal(idsOf(p.Messages), []int64{101, 102}) {
		t.Fatalf("before a v1 id: %v", idsOf(p.Messages))
	}
	p, _ = r.Page(ctx, PageQuery{ChatID: chat, After: 102})
	if !slices.Equal(idsOf(p.Messages), append([]int64{103}, fresh...)) {
		t.Fatalf("after a v1 id: %v", idsOf(p.Messages))
	}
	if err := r.Edit(ctx, chat, 102, "edited v1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if m, _ := r.Get(ctx, chat, 102); *m.Content != "edited v1" {
		t.Fatal("v1 message not edited")
	}
}

func TestCountAfter(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat := newChat()
	const me, other = 1, 2
	start := time.Now().Add(-30 * 24 * time.Hour)
	theirs := seed(t, r, chat, other, 10, start, 2*24*time.Hour)
	mine := seed(t, r, chat, me, 3, time.Now(), time.Millisecond)
	if err := r.Delete(ctx, chat, theirs[9]); err != nil {
		t.Fatal(err)
	}
	if err := r.Hide(ctx, me, chat, theirs[8], HiddenDeletedForMe); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		after int64
		limit int
		want  int
	}{
		{0, 100, 8},         // all of theirs, minus the deleted and the hidden one; never my own
		{theirs[3], 100, 4}, // 4..7
		{theirs[3], 2, 2},   // capped
		{mine[2], 100, 0},   // read up to the newest
		{theirs[7], 100, 0}, // the rest is deleted or hidden
		{theirs[0], 0, 0},   // nothing asked
	}
	for _, c := range cases {
		n, err := r.CountAfter(ctx, chat, me, c.after, c.limit)
		if err != nil || n != c.want {
			t.Errorf("after %d limit %d: %d (want %d) %v", c.after, c.limit, n, c.want, err)
		}
	}
}

// A retry of an insert that arrives after the message was deleted (or edited) must not bring it back.
func TestLateInsertRetryLosesToLaterChanges(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat := newChat()
	sender := int64(1)
	g, _ := ids.NewGenerator(2)
	id, _ := g.Next()
	m := Message{ChatID: chat, ID: id, SenderID: &sender, Type: TypeText, Content: text("secret"), CreatedAt: ids.Time(id)}
	if err := r.Insert(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, chat, id); err != nil {
		t.Fatal(err)
	}
	if err := r.Insert(ctx, m); err != nil { // the delayed retry
		t.Fatal(err)
	}
	if got, _ := r.Get(ctx, chat, id); !got.Deleted {
		t.Fatal("a late insert undeleted the message")
	}

	other := newChat()
	id2, _ := g.Next()
	m2 := Message{ChatID: other, ID: id2, SenderID: &sender, Type: TypeText, Content: text("x"), CreatedAt: ids.Time(id2)}
	if err := r.Insert(ctx, m2); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteChat(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := r.Insert(ctx, m2); err != nil {
		t.Fatal(err)
	}
	if p, _ := r.Page(ctx, PageQuery{ChatID: other}); len(p.Messages) != 0 {
		t.Fatal("a late insert brought a message back into a deleted chat")
	}
}

func TestDeleteChat(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat, keep := newChat(), newChat()
	gone := seed(t, r, chat, 1, 12, time.Now().Add(-40*24*time.Hour), 3*24*time.Hour)
	kept := seed(t, r, keep, 1, 2, time.Now(), time.Millisecond)
	if err := r.AddReaction(ctx, chat, gone[0], 2, "👍", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteChat(ctx, chat); err != nil {
		t.Fatal(err)
	}
	if p, err := r.Page(ctx, PageQuery{ChatID: chat}); err != nil || len(p.Messages) != 0 {
		t.Fatalf("messages left: %d %v", len(p.Messages), err)
	}
	if _, err := r.Locate(ctx, gone[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("location left: %v", err)
	}
	if rx, _ := r.Reactions(ctx, chat, gone[:1]); len(rx) != 0 {
		t.Fatal("reactions left")
	}
	if p, _ := r.Page(ctx, PageQuery{ChatID: keep}); !slices.Equal(idsOf(p.Messages), kept) {
		t.Fatal("another chat was touched")
	}
	// The chat can be written again (its buckets are not remembered as existing).
	again := seed(t, r, chat, 1, 1, time.Now(), 0)
	if p, _ := r.Page(ctx, PageQuery{ChatID: chat}); !slices.Equal(idsOf(p.Messages), again) {
		t.Fatal("chat not writable after deletion")
	}
}

func TestBucketOf(t *testing.T) {
	t0 := time.UnixMilli(0)
	if BucketOf(t0) != 0 || BucketOf(t0.Add(BucketSpan-time.Millisecond)) != 0 || BucketOf(t0.Add(BucketSpan)) != 1 {
		t.Fatal("bucket boundaries")
	}
}

// "Deleted for me" is final: a late "not delivered" does not replace it and Unhide does not remove it.
func TestDeletedForMeIsFinal(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat := newChat()
	ms := seed(t, r, chat, 1, 2, time.Now(), time.Millisecond)
	const viewer = 5
	// Deleted first, then a delayed "not delivered" arrives.
	if err := r.Hide(ctx, viewer, chat, ms[0], HiddenDeletedForMe); err != nil {
		t.Fatal(err)
	}
	if err := r.Hide(ctx, viewer, chat, ms[0], HiddenNotDelivered); err != nil {
		t.Fatal(err)
	}
	// Not delivered first, then deleted: upgraded.
	if err := r.Hide(ctx, viewer, chat, ms[1], HiddenNotDelivered); err != nil {
		t.Fatal(err)
	}
	if err := r.Hide(ctx, viewer, chat, ms[1], HiddenDeletedForMe); err != nil {
		t.Fatal(err)
	}
	for _, id := range ms {
		if err := r.Unhide(ctx, viewer, chat, id); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := r.Page(ctx, PageQuery{ChatID: chat, Viewer: viewer}); len(p.Messages) != 0 {
		t.Fatalf("deleted messages came back: %v", idsOf(p.Messages))
	}
}

func TestLocateIgnoresLocationWithoutMessage(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	sess, err := r.s.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := ids.NewGenerator(3)
	id, _ := g.Next()
	// What an insert that failed after writing the location leaves behind.
	if err := sess.Query(`INSERT INTO message_locations (message_id, chat_id, bucket) VALUES (?, ?, ?)`, id, newChat(), BucketOf(ids.Time(id))).Exec(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Locate(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("located a message that does not exist: %v", err)
	}
}

func TestDeleteChatRemovesHiddenMarkers(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	chat := newChat()
	ms := seed(t, r, chat, 1, 3, time.Now(), time.Millisecond)
	for _, uid := range []int64{2, 3} {
		if err := r.Hide(ctx, uid, chat, ms[0], HiddenDeletedForMe); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.DeleteChat(ctx, chat); err != nil {
		t.Fatal(err)
	}
	sess, _ := r.s.Session(ctx)
	var n int
	for _, uid := range []int64{2, 3} {
		if err := sess.Query(`SELECT COUNT(*) FROM hidden_messages WHERE user_id = ? AND chat_id = ?`, uid, chat).Scan(&n); err != nil || n != 0 {
			t.Fatalf("hidden rows of user %d left: %d %v", uid, n, err)
		}
	}
	if err := sess.Query(`SELECT COUNT(*) FROM hidden_message_users WHERE chat_id = ?`, chat).Scan(&n); err != nil || n != 0 {
		t.Fatalf("hidden users left: %d %v", n, err)
	}
}
