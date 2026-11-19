package moderation_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestRequestReview(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	good := e.user(t)
	if _, err := e.svc.RequestReview(ctx, good, "please look"); !errors.Is(err, moderation.ErrReviewNotAllowed) {
		t.Fatalf("request for a good account: %v", err)
	}
	u := e.user(t)
	e.setStatus(t, u, moderation.Restricted)
	before, err := e.svc.AccountStatus(ctx, u)
	if err != nil || !before.CanRequestReview || before.ReviewRequestedAt != nil {
		t.Fatalf("before the request %+v (%v)", before, err)
	}
	var field *moderation.FieldError
	for _, text := range []string{"   ", strings.Repeat("x", 501), "bad\x00text"} {
		if _, err := e.svc.RequestReview(ctx, u, text); !errors.As(err, &field) || field.Field != "text" {
			t.Fatalf("text %q: %v", text, err)
		}
	}
	v, err := e.svc.RequestReview(ctx, u, "  I only sent one link.\nPlease check.  ")
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != moderation.Restricted || v.CanRequestReview || v.ReviewRequestedAt == nil || v.ReviewRequestedAt.Sub(e.clock.Now()).Abs() > time.Millisecond {
		t.Fatalf("after the request %+v", v)
	}
	if _, err := e.svc.RequestReview(ctx, u, "again"); !errors.Is(err, moderation.ErrReviewNotAllowed) {
		t.Fatalf("second open request: %v", err)
	}
	if n := e.rec.notices[u]; len(n) != 1 || n[0].ReviewRequestedAt == nil || n[0].CanRequestReview {
		t.Fatalf("account_status after the request %+v", n)
	}
	after, err := e.svc.AccountStatus(ctx, u)
	if err != nil || after.ReviewRequestedAt == nil || after.CanRequestReview {
		t.Fatalf("status read after the request %+v (%v)", after, err)
	}
}

func TestReviewRequestClosesWhenStatusIsGoodAgain(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t)
	e.setStatus(t, u, moderation.Warned)
	if _, err := e.svc.RequestReview(ctx, u, "why"); err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.Appeal(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != moderation.Good || v.ReviewRequestedAt != nil || v.CanRequestReview {
		t.Fatalf("after the status returned to good %+v", v)
	}
	if n := e.rec.notices[u]; len(n) != 2 || n[1].ReviewRequestedAt != nil {
		t.Fatalf("account_status after the status returned to good %+v", n)
	}
	e.setStatus(t, u, moderation.Limited)
	if _, err := e.svc.RequestReview(ctx, u, "again"); err != nil {
		t.Fatalf("new request after the old one closed: %v", err)
	}
}

func TestReviewRequestLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t)
	e.setStatus(t, u, moderation.Suspended)
	for i := range moderation.ReviewRequestsMax {
		if _, err := e.svc.RequestReview(ctx, u, "request"); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if closed, err := e.pg.ReviewRequests().Close(ctx, u, postgres.ReviewClosedDecided, e.clock.Now()); err != nil || !closed {
			t.Fatalf("close %d: %v %v", i+1, closed, err)
		}
		e.clock.set(e.clock.Now().Add(time.Hour))
	}
	if v, err := e.svc.AccountStatus(ctx, u); err != nil || v.CanRequestReview {
		t.Fatalf("status at the limit %+v (%v)", v, err)
	}
	if _, err := e.svc.RequestReview(ctx, u, "one more"); !errors.Is(err, moderation.ErrRateLimited) {
		t.Fatalf("request over the limit: %v", err)
	}
	e.clock.set(e.clock.Now().Add(moderation.ReviewRequestRange))
	if _, err := e.svc.RequestReview(ctx, u, "a month later"); err != nil {
		t.Fatalf("request after the window: %v", err)
	}
}

func TestConcurrentReviewRequestsOpenOne(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t)
	e.setStatus(t, u, moderation.Limited)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() { _, errs[i] = e.svc.RequestReview(ctx, u, "at once") })
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, moderation.ErrReviewNotAllowed):
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d requests opened, want 1", ok)
	}
}

func TestStaffDecisionClosesTheReviewRequest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t)
	e.setStatus(t, u, moderation.Restricted)
	if _, err := e.svc.RequestReview(ctx, u, "please look"); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Decide(ctx, moderation.Decision{UserID: u, Status: moderation.Good, ReasonCode: "other", ReasonText: "Reviewed, no violation"})
	if err != nil {
		t.Fatal(err)
	}
	if res.After.Status != moderation.Good {
		t.Fatalf("after %+v", res.After)
	}
	v, err := e.svc.AccountStatus(ctx, u)
	if err != nil || v.ReviewRequestedAt != nil {
		t.Fatalf("status after the decision %+v (%v)", v, err)
	}
	n := e.rec.notices[u]
	if last := n[len(n)-1]; last.ReviewRequestedAt != nil || last.DecidedBy != "staff" || last.ReasonText == nil {
		t.Fatalf("account_status after the decision %+v", last)
	}
	e.setStatus(t, u, moderation.Limited)
	if _, err := e.svc.RequestReview(ctx, u, "again"); err != nil {
		t.Fatalf("a new request after the decision: %v", err)
	}
	var reason string
	if err := e.pg.Pool().QueryRow(ctx, `SELECT close_reason FROM account_review_requests WHERE user_id = $1 AND closed_at IS NOT NULL`, u).Scan(&reason); err != nil || reason != "decided" {
		t.Fatalf("close reason %q (%v)", reason, err)
	}
}
