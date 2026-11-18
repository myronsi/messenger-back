package moderation_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/testenv"
)

type recorder struct {
	mu       sync.Mutex
	events   []events.Event
	notices  map[int64][]moderation.StatusEvent
	statuses []string
	reports  []string
}

func (r *recorder) Emit(_ context.Context, e events.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) NotifyUser(_ context.Context, uid int64, kind string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if kind == "account_status" {
		r.notices[uid] = append(r.notices[uid], data.(moderation.StatusEvent))
	}
}

func (r *recorder) ModerationStatusChange(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statuses = append(r.statuses, status)
}

func (r *recorder) UserReport(reason, bucket string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, reason+"/"+bucket)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

type env struct {
	pg    *postgres.Store
	cache *redis.ModerationCache
	svc   *moderation.Service
	rec   *recorder
	clock *clock
	n     int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pg := testenv.Postgres(t)
	rd, prefix := testenv.Redis(t)
	e := &env{pg: pg, cache: redis.NewModerationCache(rd.Client(), prefix, time.Minute), rec: &recorder{notices: map[int64][]moderation.StatusEvent{}},
		clock: &clock{now: time.Now().Add(48 * time.Hour)}}
	e.svc = moderation.New(moderation.Deps{Store: pg, Cache: e.cache, Events: e.rec, Notifier: e.rec, Metrics: e.rec, Now: e.clock.Now})
	return e
}

func (e *env) user(t *testing.T) int64 {
	t.Helper()
	e.n++
	u, err := e.pg.Users().Create(context.Background(), fmt.Sprintf("user_%d", e.n), fmt.Sprintf("User %d", e.n), "hash")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func (e *env) direct(t *testing.T, creator, other int64) {
	t.Helper()
	if _, _, _, err := e.pg.Chats().OpenDirect(context.Background(), creator, other, false); err != nil {
		t.Fatal(err)
	}
}

func (e *env) state(t *testing.T, uid int64) postgres.ModerationState {
	t.Helper()
	st, err := e.pg.Moderation().State(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (e *env) recompute(t *testing.T, uid int64) postgres.ModerationState {
	t.Helper()
	if err := e.svc.Recompute(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	return e.state(t, uid)
}

func (e *env) report(t *testing.T, reporter, target int64) {
	t.Helper()
	if err := e.svc.Report(context.Background(), reporter, target, "spam", nil, false); err != nil {
		t.Fatalf("report %d -> %d: %v", reporter, target, err)
	}
}

func (e *env) setStatus(t *testing.T, uid int64, st moderation.Status) {
	t.Helper()
	now := e.clock.Now()
	_, _, err := e.pg.Moderation().Transition(context.Background(), uid, now, now, func(cur postgres.ModerationState, _ []postgres.ReportFact) postgres.ModerationState {
		cur.Status, cur.Since, cur.Cause = string(st), now, "test"
		return cur
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.cache.Invalidate(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
}

func TestReportWeightByContext(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	target, opened, grouped, stranger := e.user(t), e.user(t), e.user(t), e.user(t)
	e.direct(t, target, opened)
	if _, err := e.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: grouped, Name: "g", MemberIDs: []int64{target}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		reporter int64
		score    float64
	}{{opened, 1}, {grouped, 1.5}, {stranger, 1.75}} {
		e.report(t, c.reporter, target)
		if got := e.recompute(t, target).Score; got != c.score {
			t.Fatalf("after the report of %d the score is %v, want %v", c.reporter, got, c.score)
		}
	}
	if want := []string{"spam/full", "spam/medium", "spam/low"}; strings.Join(e.rec.reports, ",") != strings.Join(want, ",") {
		t.Fatalf("report metrics %v, want %v", e.rec.reports, want)
	}
	if len(e.rec.events) != 3 || e.rec.events[0].Type != events.UserReported || e.rec.events[0].UserID != target || e.rec.events[0].ActorID != opened {
		t.Fatalf("events %+v", e.rec.events)
	}
}

func TestReportRequestFromTargetCountsAsContact(t *testing.T) {
	e := newEnv(t)
	target, reporter := e.user(t), e.user(t)
	if _, _, err := e.pg.Approvals().RequestDirect(context.Background(), target, reporter, nil); err != nil {
		t.Fatal(err)
	}
	e.report(t, reporter, target)
	if got := e.recompute(t, target).Score; got != 1 {
		t.Fatalf("score %v, want 1 for a request from the target", got)
	}
}

func TestNewReporterWeighsNothing(t *testing.T) {
	e := newEnv(t)
	target, reporter := e.user(t), e.user(t)
	e.direct(t, target, reporter)
	e.clock.set(time.Now())
	e.report(t, reporter, target)
	if got := e.recompute(t, target).Score; got != 0 {
		t.Fatalf("a report of a new account weighs %v", got)
	}
}

func TestReportRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	me, other := e.user(t), e.user(t)
	var field *moderation.FieldError
	if err := e.svc.Report(ctx, me, me, "spam", nil, true); !errors.As(err, &field) || field.Field != "user_id" {
		t.Fatalf("self report: %v", err)
	}
	if err := e.svc.Report(ctx, me, other, "rude", nil, true); !errors.As(err, &field) || field.Field != "reason" {
		t.Fatalf("unknown reason: %v", err)
	}
	long := strings.Repeat("x", 501)
	if err := e.svc.Report(ctx, me, other, "spam", &long, true); !errors.As(err, &field) || field.Field != "note" {
		t.Fatalf("long note: %v", err)
	}
	if err := e.svc.Report(ctx, me, 999999, "spam", nil, true); !errors.Is(err, moderation.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	note := "  sends links  "
	if err := e.svc.Report(ctx, me, other, "scam", &note, false); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := e.pg.Social().BlockedEither(ctx, me, other); blocked {
		t.Fatal("block false still blocked")
	}
	if err := e.svc.Report(ctx, me, other, "harassment", nil, true); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := e.pg.Social().BlockedEither(ctx, me, other); !blocked {
		t.Fatal("block true did not block")
	}
	if len(e.rec.reports) != 1 {
		t.Fatalf("reporting again counted as a new report: %v", e.rec.reports)
	}
	if err := e.svc.Unreport(ctx, me, other); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Unreport(ctx, me, other); err != nil {
		t.Fatalf("withdrawing twice: %v", err)
	}
}

func TestWithdrawAndReportAgain(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	target, reporter := e.user(t), e.user(t)
	e.direct(t, target, reporter)
	e.report(t, reporter, target)
	if got := e.recompute(t, target).Score; got != 1 {
		t.Fatalf("score %v, want 1", got)
	}
	if err := e.svc.Unreport(ctx, reporter, target); err != nil {
		t.Fatal(err)
	}
	if got := e.recompute(t, target).Score; got != 0 {
		t.Fatalf("a withdrawn report still counts: %v", got)
	}
	e.clock.set(e.clock.Now().Add(40 * 24 * time.Hour))
	e.report(t, reporter, target)
	if got := e.recompute(t, target).Score; got != 0.5 {
		t.Fatalf("reporting again refreshed the report: score %v, want 0.5", got)
	}
}

func TestWithdrawingDoesNotFreeTheDailyLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	me := e.user(t)
	var first int64
	for i := range moderation.MaxReportsPerDay {
		target := e.user(t)
		if i == 0 {
			first = target
		}
		if err := e.svc.Report(ctx, me, target, "spam", nil, false); err != nil {
			t.Fatalf("report %d: %v", i+1, err)
		}
	}
	if err := e.svc.Unreport(ctx, me, first); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Report(ctx, me, e.user(t), "spam", nil, false); !errors.Is(err, moderation.ErrRateLimited) {
		t.Fatalf("a report after withdrawing one: %v", err)
	}
}

type claimer struct{ ok bool }

func (c claimer) Claim(context.Context, string, time.Duration) (bool, error) { return c.ok, nil }

func TestDailyJobRunsOncePerSlot(t *testing.T) {
	svc := moderation.New(moderation.Deps{Store: brokenStore{}, Cache: brokenCache{}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.RunDaily(ctx, slog.New(slog.DiscardHandler), time.Hour, claimer{ok: false})
}

func TestReportRateLimit(t *testing.T) {
	e := newEnv(t)
	me := e.user(t)
	for i := range moderation.MaxReportsPerDay {
		if err := e.svc.Report(context.Background(), me, e.user(t), "spam", nil, false); err != nil {
			t.Fatalf("report %d: %v", i+1, err)
		}
	}
	last := e.user(t)
	if err := e.svc.Report(context.Background(), me, last, "spam", nil, false); !errors.Is(err, moderation.ErrRateLimited) {
		t.Fatalf("report 21: %v", err)
	}
	e.clock.set(e.clock.Now().Add(25 * time.Hour))
	if err := e.svc.Report(context.Background(), me, last, "spam", nil, false); err != nil {
		t.Fatalf("a day later: %v", err)
	}
}

func TestBurstOfUnconnectedReports(t *testing.T) {
	e := newEnv(t)
	target := e.user(t)
	for range moderation.BurstMax {
		e.report(t, e.user(t), target)
	}
	if got := e.recompute(t, target).Score; got != 1.25 {
		t.Fatalf("five unconnected reports: score %v, want 1.25", got)
	}
	e.report(t, e.user(t), target)
	if got := e.recompute(t, target).Score; got != 0 {
		t.Fatalf("a burst of six: score %v, want 0", got)
	}
	connected := e.user(t)
	e.direct(t, target, connected)
	e.report(t, connected, target)
	if got := e.recompute(t, target).Score; got != 1 {
		t.Fatalf("a connected report in the burst: score %v, want 1", got)
	}
}

func TestRecomputeChangesStatus(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	target := e.user(t)
	if err := e.cache.Set(ctx, target, "good"); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		r := e.user(t)
		e.direct(t, target, r)
		e.report(t, r, target)
	}
	st := e.recompute(t, target)
	if st.Status != "limited" || st.Score != 6 {
		t.Fatalf("state %+v, want limited with 6", st)
	}
	if _, ok, _ := e.cache.Get(ctx, target); ok {
		t.Fatal("the cached status was not invalidated")
	}
	if len(e.rec.statuses) != 1 || e.rec.statuses[0] != "limited" {
		t.Fatalf("status metrics %v", e.rec.statuses)
	}
	notices := e.rec.notices[target]
	if len(notices) != 1 || notices[0].Status != moderation.Limited || len(notices[0].Restrictions) != 3 || !notices[0].CanAppeal {
		t.Fatalf("account_status events %+v", notices)
	}
	log, err := e.pg.Moderation().Events(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 || log[0].FromStatus != "good" || log[0].ToStatus != "limited" || log[0].Cause != moderation.CauseReports {
		t.Fatalf("audit log %+v", log)
	}
	e.recompute(t, target)
	if log, _ := e.pg.Moderation().Events(ctx, target); len(log) != 1 {
		t.Fatalf("recomputing without change logged again: %+v", log)
	}
}

func TestHandleUserReported(t *testing.T) {
	e := newEnv(t)
	target := e.user(t)
	for range 3 {
		r := e.user(t)
		e.direct(t, target, r)
		e.report(t, r, target)
	}
	entry := redis.Entry{ID: "1-0", Fields: map[string]string{"type": events.UserReported, "user_id": fmt.Sprint(target), "at": "0"}}
	if err := e.svc.Handle(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if st := e.state(t, target); st.Status != "warned" {
		t.Fatalf("status %s, want warned", st.Status)
	}
	if err := e.svc.Handle(context.Background(), redis.Entry{ID: "2-0", Fields: map[string]string{"type": "nope"}}); err != nil {
		t.Fatalf("malformed entry: %v", err)
	}
}

func TestAllowMatrix(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	stranger := e.user(t)
	type row struct {
		action  moderation.Action
		contact bool
		want    map[moderation.Status]bool
	}
	all := func(good, warned, limited, restricted, suspended bool) map[moderation.Status]bool {
		return map[moderation.Status]bool{moderation.Good: good, moderation.Warned: warned, moderation.Limited: limited, moderation.Restricted: restricted, moderation.Suspended: suspended}
	}
	rows := []row{
		{moderation.Send, false, all(true, true, true, true, false)},
		{moderation.StartDirect, true, all(true, true, true, true, false)},
		{moderation.StartDirect, false, all(true, true, false, false, false)},
		{moderation.InviteToGroup, true, all(true, true, true, true, false)},
		{moderation.InviteToGroup, false, all(true, true, false, false, false)},
		{moderation.Call, true, all(true, true, true, true, false)},
		{moderation.Call, false, all(true, true, false, false, false)},
		{moderation.CreateGroup, false, all(true, true, true, false, false)},
		{moderation.Upload, false, all(true, true, true, false, false)},
		{moderation.Forward, false, all(true, true, true, false, false)},
		{moderation.EditProfile, false, all(true, true, true, false, false)},
		{moderation.PublishPack, false, all(true, true, true, false, false)},
		{moderation.ManageGroup, false, all(true, true, true, true, false)},
	}
	for _, st := range []moderation.Status{moderation.Good, moderation.Warned, moderation.Limited, moderation.Restricted, moderation.Suspended} {
		actor, contact := e.user(t), e.user(t)
		e.direct(t, contact, actor)
		e.setStatus(t, actor, st)
		for _, r := range rows {
			target := stranger
			if r.contact {
				target = contact
			}
			err := e.svc.Allow(ctx, actor, r.action, target)
			action, restricted := moderation.Restriction(err)
			if err != nil && !restricted {
				t.Fatalf("%s %s: %v", st, r.action, err)
			}
			if restricted && action != r.action {
				t.Fatalf("%s %s: restriction names %s", st, r.action, action)
			}
			if got := err == nil; got != r.want[st] {
				t.Errorf("%s %s (contact %v): allowed %v, want %v", st, r.action, r.contact, got, r.want[st])
			}
		}
		groupCall := e.svc.Allow(ctx, actor, moderation.Call, 0) == nil
		if want := st.Level() < moderation.Restricted.Level(); groupCall != want {
			t.Errorf("%s group call allowed %v, want %v", st, groupCall, want)
		}
		if own := e.svc.Allow(ctx, actor, moderation.StartDirect, actor) == nil; own != (st != moderation.Suspended) {
			t.Errorf("%s saved messages allowed %v", st, own)
		}
	}
}

func TestLimitedDirectChatsPerDay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.clock.set(time.Now())
	actor, contact := e.user(t), e.user(t)
	e.direct(t, contact, actor)
	e.setStatus(t, actor, moderation.Limited)
	e.direct(t, actor, actor)
	for i := range moderation.DirectChatsPerDay {
		if err := e.svc.Allow(ctx, actor, moderation.StartDirect, contact); err != nil {
			t.Fatalf("chat %d: %v", i+1, err)
		}
		e.direct(t, actor, e.user(t))
	}
	if _, ok := moderation.Restriction(e.svc.Allow(ctx, actor, moderation.StartDirect, contact)); !ok {
		t.Fatal("a sixth new direct chat in a day was allowed")
	}
	e.clock.set(time.Now().Add(25 * time.Hour))
	if err := e.svc.Allow(ctx, actor, moderation.StartDirect, contact); err != nil {
		t.Fatalf("a day later: %v", err)
	}
}

func TestRoutesToRequests(t *testing.T) {
	e := newEnv(t)
	for st, want := range map[moderation.Status]bool{moderation.Good: false, moderation.Warned: false, moderation.Limited: true, moderation.Restricted: true} {
		u := e.user(t)
		e.setStatus(t, u, st)
		got, err := e.svc.RoutesToRequests(context.Background(), u)
		if err != nil || got != want {
			t.Errorf("%s routes to requests %v (%v), want %v", st, got, err, want)
		}
	}
}

func TestAppeal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	good := e.user(t)
	if _, err := e.svc.Appeal(ctx, good); !errors.Is(err, moderation.ErrAppealTooEarly) {
		t.Fatalf("appeal of a good account: %v", err)
	}
	u := e.user(t)
	e.setStatus(t, u, moderation.Restricted)
	before, err := e.svc.AccountStatus(ctx, u)
	if err != nil || !before.CanAppeal || before.Status != moderation.Restricted || before.Since == nil {
		t.Fatalf("status before the appeal %+v (%v)", before, err)
	}
	v, err := e.svc.Appeal(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != moderation.Limited || v.CanAppeal || v.ProbationUntil == nil {
		t.Fatalf("after the appeal %+v", v)
	}
	if _, err := e.svc.Appeal(ctx, u); !errors.Is(err, moderation.ErrAppealTooEarly) {
		t.Fatalf("second appeal: %v", err)
	}
	if n := e.rec.notices[u]; len(n) != 1 || n[0].Status != moderation.Limited || n[0].ProbationUntil == nil {
		t.Fatalf("account_status after the appeal %+v", n)
	}
	if err := e.svc.Allow(ctx, u, moderation.Upload, 0); err != nil {
		t.Fatalf("the lower level does not apply at once: %v", err)
	}
}

func TestDailyRetentionAndDecay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	reporter, target, limited := e.user(t), e.user(t), e.user(t)
	now := e.clock.Now()
	_, err := e.pg.Moderation().SaveReport(ctx, postgres.UserReport{
		ReporterID: reporter, TargetID: target, Reason: "spam", Context: postgres.ContextNone, Weight: 0.25, Snapshot: []byte("{}"),
		Now: now.Add(-181 * 24 * time.Hour),
	}, postgres.ReportRules{RateSince: now.Add(-24 * time.Hour), MaxInRate: 20})
	if err != nil {
		t.Fatal(err)
	}
	e.report(t, e.user(t), target)
	e.setStatus(t, limited, moderation.Limited)
	checked, purged, err := e.svc.Daily(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 || checked != 1 {
		t.Fatalf("daily job checked %d and purged %d, want 1 and 1", checked, purged)
	}
	if st := e.state(t, limited); st.Status != "good" {
		t.Fatalf("a limited account without reports stays %s", st.Status)
	}
}

func TestLabelsAndHidden(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ids := map[moderation.Status]int64{}
	for _, st := range []moderation.Status{moderation.Good, moderation.Limited, moderation.Restricted, moderation.Suspended} {
		ids[st] = e.user(t)
		e.setStatus(t, ids[st], st)
	}
	list := []int64{ids[moderation.Good], ids[moderation.Limited], ids[moderation.Restricted], ids[moderation.Suspended]}
	hidden, err := e.svc.Hidden(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if hidden[ids[moderation.Good]] || hidden[ids[moderation.Limited]] || !hidden[ids[moderation.Restricted]] || !hidden[ids[moderation.Suspended]] {
		t.Fatalf("hidden %v", hidden)
	}
	labels, err := e.svc.Labels(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 1 || !labels[ids[moderation.Suspended]] {
		t.Fatalf("labels %v", labels)
	}
	if st, ok, _ := e.cache.Get(ctx, ids[moderation.Good]); !ok || st != "good" {
		t.Fatalf("labels did not fill the cache: %q %v", st, ok)
	}
	again, err := e.svc.Labels(ctx, list)
	if err != nil || len(again) != 1 || !again[ids[moderation.Suspended]] {
		t.Fatalf("labels from the cache %v (%v)", again, err)
	}
}

type brokenRepo struct{ postgres.ModerationRepository }

func (brokenRepo) State(context.Context, int64) (postgres.ModerationState, error) {
	return postgres.ModerationState{}, errors.New("postgres down")
}

type brokenStore struct{ moderation.Store }

func (brokenStore) Moderation() postgres.ModerationRepository { return brokenRepo{} }

type brokenCache struct{ status string }

func (c brokenCache) Get(context.Context, int64) (string, bool, error) {
	if c.status != "" {
		return c.status, true, nil
	}
	return "", false, errors.New("redis down")
}

func (brokenCache) Set(context.Context, int64, string) error { return errors.New("redis down") }

func (brokenCache) GetMany(context.Context, []int64) (map[int64]string, error) {
	return nil, errors.New("redis down")
}

func (brokenCache) SetMany(context.Context, map[int64]string) error { return errors.New("redis down") }

func (brokenCache) Invalidate(context.Context, int64) error { return errors.New("redis down") }

func TestAllowFailsClosed(t *testing.T) {
	svc := moderation.New(moderation.Deps{Store: brokenStore{}, Cache: brokenCache{}})
	if err := svc.Allow(context.Background(), 1, moderation.Send, 0); !errors.Is(err, moderation.ErrUnavailable) {
		t.Fatalf("without Redis and PostgreSQL: %v", err)
	}
	cached := moderation.New(moderation.Deps{Store: brokenStore{}, Cache: brokenCache{status: "warned"}})
	if err := cached.Allow(context.Background(), 1, moderation.Send, 0); err != nil {
		t.Fatalf("with a cached status: %v", err)
	}
}
