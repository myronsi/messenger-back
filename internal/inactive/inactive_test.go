package inactive_test

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/inactive"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/testenv"
)

type recorder struct {
	mu      sync.Mutex
	deleted map[int64]postgres.DeletedAccount
	events  []events.Event
}

func (r *recorder) afterDeletion(_ context.Context, userID int64, d postgres.DeletedAccount) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted[userID] = d
}

func (r *recorder) Emit(_ context.Context, e events.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

type env struct {
	t    *testing.T
	ctx  context.Context
	pg   *postgres.Store
	auth *auth.Service
	svc  *inactive.Service
	now  time.Time
	rec  *recorder
}

func newEnv(t *testing.T, enabled, mail bool) *env {
	t.Helper()
	pg := testenv.Postgres(t)
	rd, prefix := testenv.Redis(t)
	a, err := auth.NewService(pg, rd.Client(), auth.Config{
		JWTSecret: []byte(strings.Repeat("j", 40)), EncryptionKey: []byte(strings.Repeat("k", 32)), RecoveryPepper: []byte(strings.Repeat("p", 32)),
		Namespace: prefix, RecoveryMailEnabled: mail, RecoveryWebURL: "https://messenger.example.test",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: context.Background(), pg: pg, auth: a, now: time.Now().Add(-time.Minute), rec: &recorder{deleted: map[int64]postgres.DeletedAccount{}}}
	e.svc = inactive.New(inactive.Deps{
		Store: pg.InactiveAccounts(), Accounts: a, AfterDeletion: e.rec.afterDeletion, Events: e.rec,
		Enabled: enabled, Mail: mail, Now: func() time.Time { return e.now },
	})
	return e
}

func (e *env) user(name string, lastActive time.Time) int64 {
	e.t.Helper()
	u, err := e.pg.Users().Create(e.ctx, name, name, "unused")
	if err != nil {
		e.t.Fatal(err)
	}
	e.setActivity(u.ID, lastActive)
	return u.ID
}

func (e *env) setActivity(userID int64, at time.Time) {
	e.t.Helper()
	if _, err := e.pg.Pool().Exec(e.ctx, `UPDATE users SET created_at = $2, last_seen_at = $2 WHERE id = $1`, userID, at); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.pg.Pool().Exec(e.ctx, `UPDATE user_sessions SET created_at = $2, last_active_at = $2 WHERE user_id = $1`, userID, at); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) email(userID int64, address string, verified bool) {
	e.t.Helper()
	var at *time.Time
	if verified {
		now := time.Now()
		at = &now
	}
	if _, err := e.pg.Pool().Exec(e.ctx, `INSERT INTO recovery_emails (user_id, address, verified_at) VALUES ($1, $2, $3)`, userID, address, at); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) run() inactive.Result {
	e.t.Helper()
	res, err := e.svc.Run(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) exists(userID int64) bool {
	e.t.Helper()
	_, err := e.pg.Users().Get(e.ctx, userID)
	if errors.Is(err, postgres.ErrNotFound) {
		return false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return true
}

func (e *env) queuedWarnings() int {
	e.t.Helper()
	var n int
	if err := e.pg.Pool().QueryRow(e.ctx, `SELECT count(*) FROM recovery_mail_queue WHERE kind = 'inactive_account'`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) warned(userID int64) bool {
	e.t.Helper()
	var ok bool
	if err := e.pg.Pool().QueryRow(e.ctx, `SELECT EXISTS (SELECT 1 FROM inactive_account_warnings WHERE user_id = $1)`, userID).Scan(&ok); err != nil {
		e.t.Fatal(err)
	}
	return ok
}

func yearsAgo(now time.Time, days int) time.Time {
	return now.AddDate(-inactive.Years, 0, days)
}

func TestInactiveAccountIsDeletedLikeDeleteAccount(t *testing.T) {
	e := newEnv(t, true, false)
	gone := e.user("gone", yearsAgo(e.now, -1))
	friend := e.user("friend", e.now.Add(-time.Hour))
	chat, _, err := e.pg.Chats().CreateDirect(e.ctx, gone, friend)
	if err != nil {
		t.Fatal(err)
	}

	res := e.run()

	if res.Deleted != 1 || e.exists(gone) || !e.exists(friend) {
		t.Fatalf("deleted %d, gone exists %v, friend exists %v", res.Deleted, e.exists(gone), e.exists(friend))
	}
	d, ok := e.rec.deleted[gone]
	if !ok || !slices.Equal(d.DeletedChats[chat.ID], []int64{friend}) {
		t.Fatalf("cleanup after deletion: %v %+v", ok, d)
	}
	if !slices.ContainsFunc(e.rec.events, func(ev events.Event) bool { return ev.Type == events.UserDeleted && ev.UserID == gone }) {
		t.Fatalf("user.deleted not emitted: %+v", e.rec.events)
	}
}

func TestRecentlyActiveAccountsAreKept(t *testing.T) {
	e := newEnv(t, true, false)
	seen := e.user("seen", yearsAgo(e.now, -400))
	if _, err := e.pg.Pool().Exec(e.ctx, `UPDATE users SET last_seen_at = $2 WHERE id = $1`, seen, e.now.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	ts, err := e.auth.Register(e.ctx, auth.Client{IP: netip.MustParseAddr("10.0.0.1")}, "session", "Session", "correct horse battery", nil)
	if err != nil {
		t.Fatal(err)
	}
	e.setActivity(ts.User.ID, yearsAgo(e.now, -400))
	if _, err := e.pg.Pool().Exec(e.ctx, `UPDATE user_sessions SET last_active_at = $2 WHERE user_id = $1`, ts.User.ID, e.now.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	fresh := e.user("fresh", e.now.AddDate(0, 0, -1))

	if res := e.run(); res.Deleted != 0 || res.Warned != 0 {
		t.Fatalf("active accounts touched: %+v", res)
	}
	for _, id := range []int64{seen, ts.User.ID, fresh} {
		if !e.exists(id) {
			t.Fatalf("account %d active a day ago was deleted", id)
		}
	}
}

func TestWarnedAccountThatSignsInIsKept(t *testing.T) {
	e := newEnv(t, true, true)
	ts, err := e.auth.Register(e.ctx, auth.Client{IP: netip.MustParseAddr("10.0.0.1")}, "returns", "Returns", "correct horse battery", nil)
	if err != nil {
		t.Fatal(err)
	}
	back := ts.User.ID
	e.setActivity(back, yearsAgo(e.now, 29))
	e.email(back, "returns@example.test", true)
	silent := e.user("silent", yearsAgo(e.now, 29))
	e.email(silent, "silent@example.test", true)

	if res := e.run(); res.Warned != 2 || res.Deleted != 0 {
		t.Fatalf("warning run: %+v", res)
	}
	if _, err := e.auth.Login(e.ctx, auth.Client{IP: netip.MustParseAddr("10.0.0.2")}, "returns", "correct horse battery"); err != nil {
		t.Fatal(err)
	}

	e.now = inactive.DeletionDay(e.now).Add(time.Hour)
	res := e.run()

	if res.Revived != 1 || res.Deleted != 1 {
		t.Fatalf("deletion run: %+v", res)
	}
	if !e.exists(back) || e.warned(back) {
		t.Fatalf("signed-in account: exists %v, still warned %v", e.exists(back), e.warned(back))
	}
	if e.exists(silent) {
		t.Fatal("warned account that stayed away was kept")
	}
}

func TestWarningIsQueuedOnceAndOnlyForAVerifiedEmail(t *testing.T) {
	e := newEnv(t, true, true)
	verified := e.user("verified", yearsAgo(e.now, 29))
	e.email(verified, "verified@example.test", true)
	pending := e.user("pending", yearsAgo(e.now, 29))
	e.email(pending, "pending@example.test", false)
	none := e.user("none", yearsAgo(e.now, 29))

	if res := e.run(); res.Warned != 1 || res.Deleted != 0 {
		t.Fatalf("first run: %+v", res)
	}
	if res := e.run(); res.Warned != 0 || res.Deleted != 0 {
		t.Fatalf("second run: %+v", res)
	}
	if n := e.queuedWarnings(); n != 1 || !e.warned(verified) || e.warned(pending) || e.warned(none) {
		t.Fatalf("queued %d, warned: verified %v pending %v none %v", n, e.warned(verified), e.warned(pending), e.warned(none))
	}

	e.now = e.now.AddDate(0, 0, 30)
	if res := e.run(); res.Deleted != 2 || res.Warned != 0 {
		t.Fatalf("30 days later: %+v", res)
	}
	if !e.exists(verified) || e.exists(pending) || e.exists(none) {
		t.Fatalf("exists: verified %v pending %v none %v", e.exists(verified), e.exists(pending), e.exists(none))
	}
	if n := e.queuedWarnings(); n != 1 {
		t.Fatalf("queued %d warnings", n)
	}
}

func TestWarningEmailNamesTheDeletionDate(t *testing.T) {
	e := newEnv(t, true, true)
	quiet := e.user("quiet", yearsAgo(e.now, 29))
	e.email(quiet, "quiet@example.test", true)
	if res := e.run(); res.Warned != 1 {
		t.Fatalf("run: %+v", res)
	}

	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	var to, subject, body string
	e.auth.RunRecoveryMail(ctx, func(_ context.Context, a, s, b string) error {
		to, subject, body = a, s, b
		cancel()
		return nil
	})

	date := inactive.DeletionDay(e.now).Format("2 January 2006")
	if to != "quiet@example.test" || !strings.Contains(subject, date) {
		t.Fatalf("mail to %q, subject %q", to, subject)
	}
	for _, want := range []string{"@quiet", date, "not used for " + inactive.Period, "Sign in to keep it", "https://messenger.example.test"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestWithoutMailAccountsAreDeletedWithoutWarning(t *testing.T) {
	e := newEnv(t, true, false)
	soon := e.user("soon", yearsAgo(e.now, 29))
	e.email(soon, "soon@example.test", true)
	due := e.user("due", yearsAgo(e.now, -1))
	e.email(due, "due@example.test", true)

	if res := e.run(); res.Warned != 0 || res.Deleted != 1 {
		t.Fatalf("run: %+v", res)
	}
	if !e.exists(soon) || e.exists(due) || e.queuedWarnings() != 0 {
		t.Fatalf("soon exists %v, due exists %v, queued %d", e.exists(soon), e.exists(due), e.queuedWarnings())
	}
}

func TestNothingHappensWhenTheSettingIsOff(t *testing.T) {
	e := newEnv(t, false, true)
	old := e.user("old", yearsAgo(e.now, -100))
	warnable := e.user("warnable", yearsAgo(e.now, 29))
	e.email(warnable, "warnable@example.test", true)

	plan, err := e.svc.Plan(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Warn != 1 || plan.Delete != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	if res := e.run(); res != (inactive.Result{}) {
		t.Fatalf("run while off: %+v", res)
	}
	if !e.exists(old) || !e.exists(warnable) || e.warned(warnable) || e.queuedWarnings() != 0 || len(e.rec.events) != 0 {
		t.Fatal("the job changed something while the setting is off")
	}
}
