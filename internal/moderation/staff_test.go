package moderation_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func (e *env) reported(t *testing.T, target int64, n int) []int64 {
	t.Helper()
	reporters := make([]int64, n)
	for i := range reporters {
		r := e.user(t)
		e.direct(t, target, r)
		e.report(t, r, target)
		reporters[i] = r
	}
	e.recompute(t, target)
	return reporters
}

func (e *env) outcomes(uid int64) []moderation.OutcomeEvent {
	e.rec.mu.Lock()
	defer e.rec.mu.Unlock()
	return append([]moderation.OutcomeEvent(nil), e.rec.outcomes[uid]...)
}

func TestDecideAppliesStaffDecision(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	target := e.user(t)
	reporters := e.reported(t, target, 6)
	if st := e.state(t, target); st.Status != "limited" {
		t.Fatalf("before the decision %+v", st)
	}
	if err := e.cache.Set(ctx, target, "limited"); err != nil {
		t.Fatal(err)
	}

	res, err := e.svc.Decide(ctx, moderation.Decision{UserID: target, Status: moderation.Restricted, ReasonCode: "spam", ReasonText: "  Repeated spam links  ", StaffRef: "case-7"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Before.Status != moderation.Limited || res.Before.DecidedBy != "automatic" || res.After.Status != moderation.Restricted || res.After.DecidedBy != "staff" {
		t.Fatalf("result %+v", res)
	}
	if res.After.ReasonCode == nil || *res.After.ReasonCode != "spam" || res.After.ReasonText == nil || *res.After.ReasonText != "Repeated spam links" || res.After.CanAppeal {
		t.Fatalf("after %+v", res.After)
	}
	if err := e.svc.Allow(ctx, target, moderation.Upload, 0); err == nil {
		t.Fatal("the cached status was not invalidated")
	}
	v, err := e.svc.AccountStatus(ctx, target)
	if err != nil || v.DecidedBy != "staff" || *v.ReasonText != "Repeated spam links" {
		t.Fatalf("account status %+v (%v)", v, err)
	}
	notices := e.rec.notices[target]
	last := notices[len(notices)-1]
	if last.Status != moderation.Restricted || last.DecidedBy != "staff" || *last.ReasonCode != "spam" {
		t.Fatalf("account_status event %+v", last)
	}
	log, err := e.pg.Moderation().Events(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	manual := log[len(log)-1]
	if manual.Cause != moderation.CauseManual || manual.FromStatus != "limited" || manual.ToStatus != "restricted" || *manual.StaffRef != "case-7" || *manual.ReasonCode != "spam" {
		t.Fatalf("status history %+v", manual)
	}
	for _, r := range reporters {
		got := e.outcomes(r)
		if len(got) != 1 || got[0].Outcome != "action_taken" || got[0].SubjectType != "user" || got[0].SubjectID != strconv.FormatInt(target, 10) {
			t.Fatalf("reporter %d outcomes %+v", r, got)
		}
	}

	if _, err := e.svc.Decide(ctx, moderation.Decision{UserID: target, Status: moderation.Restricted, ReasonCode: "spam", ReasonText: "Confirmed"}); err != nil {
		t.Fatal(err)
	}
	if log, _ := e.pg.Moderation().Events(ctx, target); len(log) != 3 || log[2].FromStatus != "restricted" || log[2].ToStatus != "restricted" {
		t.Fatalf("a confirmation at the same level is recorded: %+v", log)
	}
	if got := e.outcomes(reporters[0]); len(got) != 1 {
		t.Fatalf("a reporter is told once per report: %+v", got)
	}
	if _, err := e.svc.Appeal(ctx, target); !errors.Is(err, moderation.ErrAppealTooEarly) {
		t.Fatalf("appeal of a staff decision: %v", err)
	}

	reverse, err := e.svc.Decide(ctx, moderation.Decision{UserID: target, Status: moderation.Good, ReasonCode: "other", ReasonText: "Reviewed, no violation"})
	if err != nil || reverse.After.Status != moderation.Good {
		t.Fatalf("reverse %+v (%v)", reverse, err)
	}
	if st := e.recompute(t, target); st.Status != "good" || st.DecidedBy != "staff" {
		t.Fatalf("old reports must not undo the reversal: %+v", st)
	}
}

func TestDecideValidates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t)
	long := make([]byte, 1001)
	for i := range long {
		long[i] = 'a'
	}
	cases := []struct {
		d     moderation.Decision
		field string
	}{
		{moderation.Decision{UserID: u, Status: "banned", ReasonCode: "spam", ReasonText: "x"}, "status"},
		{moderation.Decision{UserID: u, Status: moderation.Warned, ReasonCode: "rude", ReasonText: "x"}, "reason_code"},
		{moderation.Decision{UserID: u, Status: moderation.Warned, ReasonCode: "spam", ReasonText: "   "}, "reason_text"},
		{moderation.Decision{UserID: u, Status: moderation.Warned, ReasonCode: "spam", ReasonText: string(long)}, "reason_text"},
		{moderation.Decision{UserID: u, Status: moderation.Warned, ReasonCode: "spam", ReasonText: "a\x00b"}, "reason_text"},
		{moderation.Decision{UserID: u, Status: moderation.Warned, ReasonCode: "spam", ReasonText: "x", StaffRef: "has space"}, "staff_ref"},
	}
	for _, c := range cases {
		var field *moderation.FieldError
		if _, err := e.svc.Decide(ctx, c.d); !errors.As(err, &field) || field.Field != c.field {
			t.Errorf("%+v: %v, want a %s error", c.d, err, c.field)
		}
	}
	if _, err := e.svc.Decide(ctx, moderation.Decision{UserID: 1 << 60, Status: moderation.Warned, ReasonCode: "illegal", ReasonText: "x"}); !errors.Is(err, moderation.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	res, err := e.svc.Decide(ctx, moderation.Decision{UserID: u, Status: moderation.Warned, ReasonCode: "illegal", ReasonText: "Line one\nLine two"})
	if err != nil || res.Before.Status != moderation.Good || res.After.Status != moderation.Warned {
		t.Fatalf("decision on a user without moderation row %+v (%v)", res, err)
	}
}

func TestDismissReports(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	target := e.user(t)
	reporters := e.reported(t, target, 6)

	res, err := e.svc.DismissReports(ctx, target, reporters[:2])
	if err != nil {
		t.Fatal(err)
	}
	if res.Dismissed != 2 || res.Before.Status != moderation.Limited || res.After.Status != moderation.Warned {
		t.Fatalf("partial dismissal %+v", res)
	}
	log, _ := e.pg.Moderation().Events(ctx, target)
	if last := log[len(log)-1]; last.Cause != moderation.CauseDismissed || last.ToStatus != "warned" {
		t.Fatalf("status history %+v", last)
	}
	for i, r := range reporters {
		got := e.outcomes(r)
		if i < 2 && (len(got) != 1 || got[0].Outcome != "no_violation") {
			t.Fatalf("dismissed reporter %d: %+v", r, got)
		}
		if i >= 2 && len(got) != 0 {
			t.Fatalf("reporter %d whose report still counts: %+v", r, got)
		}
	}

	e.report(t, reporters[0], target)
	if st := e.recompute(t, target); st.Score != 4 {
		t.Fatalf("a dismissed report stays dismissed when its reporter reports again: %+v", st)
	}

	res, err = e.svc.DismissReports(ctx, target, nil)
	if err != nil || res.Dismissed != 4 || res.After.Status != moderation.Good {
		t.Fatalf("dismiss all %+v (%v)", res, err)
	}
	if res, err := e.svc.DismissReports(ctx, target, nil); err != nil || res.Dismissed != 0 {
		t.Fatalf("nothing left to dismiss %+v (%v)", res, err)
	}
	var field *moderation.FieldError
	if _, err := e.svc.DismissReports(ctx, target, []int64{}); !errors.As(err, &field) {
		t.Fatalf("empty reporter list: %v", err)
	}
	if _, err := e.svc.DismissReports(ctx, 1<<60, nil); !errors.Is(err, moderation.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestHideAndUnhidePacks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	owner, reporter := e.user(t), e.user(t)
	now := time.Now()

	sticker, err := e.pg.Stickers().CreatePack(ctx, owner, "Stickers", "MODPACK00001", 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pg.Stickers().Report(ctx, postgres.PackReport{PackID: sticker.ID, ReporterID: reporter, Reason: "spam", Since: now.Add(-24 * time.Hour), MaxReports: 20, HideAfter: 5}); err != nil {
		t.Fatal(err)
	}
	emoji, err := e.pg.Emoji().CreatePack(ctx, owner, "Emoji", "MODPACK00002")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.pg.Emoji().Report(ctx, postgres.EmojiReport{PackID: emoji.ID, ReporterID: reporter, Reason: "spam", Since: now.Add(-24 * time.Hour), MaxReports: 20, HideAfter: 5}); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []moderation.PackKind{moderation.StickerPack, moderation.EmojiPack} {
		id := sticker.ID
		if kind == moderation.EmojiPack {
			id = emoji.ID
		}
		hidden := func() bool {
			t.Helper()
			if kind == moderation.StickerPack {
				p, err := e.pg.Stickers().PackBySlug(ctx, *sticker.Slug)
				if err != nil {
					t.Fatal(err)
				}
				return p.Hidden
			}
			p, err := e.pg.Emoji().Pack(ctx, emoji.ID)
			if err != nil {
				t.Fatal(err)
			}
			return p.Hidden
		}
		changed, err := e.svc.HidePack(ctx, kind, id)
		if err != nil || !changed || !hidden() {
			t.Fatalf("%s hide: %v %v", kind, changed, err)
		}
		if changed, err := e.svc.HidePack(ctx, kind, id); err != nil || changed {
			t.Fatalf("%s hide again: %v %v", kind, changed, err)
		}
		changed, err = e.svc.UnhidePack(ctx, kind, id)
		if err != nil || !changed || hidden() {
			t.Fatalf("%s unhide: %v %v", kind, changed, err)
		}
		if _, err := e.svc.HidePack(ctx, kind, 1<<60); !errors.Is(err, moderation.ErrNotFound) {
			t.Fatalf("%s unknown pack: %v", kind, err)
		}
	}
	got := e.outcomes(reporter)
	if len(got) != 2 || got[0].SubjectType != "sticker_pack" || got[1].SubjectType != "emoji_pack" || got[0].Outcome != "action_taken" || got[1].Outcome != "action_taken" {
		t.Fatalf("pack reporter outcomes %+v", got)
	}
	if _, err := e.svc.HidePack(ctx, "chat", sticker.ID); !errors.Is(err, moderation.ErrNotFound) {
		t.Fatalf("unknown pack kind: %v", err)
	}
}

func TestReportOutcomesForReporter(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	target := e.user(t)
	reporters := e.reported(t, target, 1)
	reporter := reporters[0]
	if _, err := e.svc.Decide(ctx, moderation.Decision{UserID: target, Status: moderation.Good, ReasonCode: "other", ReasonText: "Fine"}); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.ReportOutcomes(ctx, reporter)
	if err != nil || len(list) != 1 || list[0].Read || list[0].Outcome != "no_violation" || list[0].SubjectID != target {
		t.Fatalf("outcomes %+v (%v)", list, err)
	}
	if err := e.svc.MarkOutcomeRead(ctx, target, list[0].ID); !errors.Is(err, moderation.ErrNotFound) {
		t.Fatalf("someone else's outcome: %v", err)
	}
	for range 2 {
		if err := e.svc.MarkOutcomeRead(ctx, reporter, list[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := e.svc.ReportOutcomes(ctx, reporter); !list[0].Read {
		t.Fatalf("after reading %+v", list)
	}
	e.clock.set(e.clock.Now().Add(moderation.OutcomeRetention + time.Hour))
	if _, _, err := e.svc.Daily(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.svc.ReportOutcomes(ctx, reporter); len(list) != 0 {
		t.Fatalf("outcomes after the retention %+v", list)
	}
}
