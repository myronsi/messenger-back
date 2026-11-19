package moderation

import (
	"context"
	"errors"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	MaxReviewText      = 500
	ReviewRequestsMax  = 3
	ReviewRequestRange = 30 * 24 * time.Hour
)

var ErrReviewNotAllowed = errors.New("review request not allowed")

func (s *Service) RequestReview(ctx context.Context, userID int64, text string) (View, error) {
	clean, err := cleanText("text", &text, MaxReviewText)
	if err != nil {
		return View{}, err
	}
	if clean == nil {
		return View{}, &FieldError{Field: "text", Message: "must not be empty"}
	}
	st, err := s.d.Store.Moderation().State(ctx, userID)
	if err != nil {
		return View{}, unavailable(err)
	}
	if ParseStatus(st.Status) == Good {
		return View{}, ErrReviewNotAllowed
	}
	now := s.d.Now()
	err = s.d.Store.ReviewRequests().Create(ctx, postgres.NewReviewRequest{UserID: userID, Status: st.Status, Text: *clean, Now: now}, now.Add(-ReviewRequestRange), ReviewRequestsMax)
	switch {
	case errors.Is(err, postgres.ErrConflict):
		return View{}, ErrReviewNotAllowed
	case errors.Is(err, postgres.ErrLimit):
		return View{}, ErrRateLimited
	case err != nil:
		return View{}, unavailable(err)
	}
	s.d.Log.InfoContext(ctx, "account review requested", "user_id", userID, "status", st.Status)
	v, err := s.withReview(ctx, s.view(st))
	if err != nil {
		return v, err
	}
	if s.d.Notifier != nil {
		s.d.Notifier.NotifyUser(ctx, userID, "account_status", EventData(v))
	}
	return v, nil
}

func (s *Service) withReview(ctx context.Context, v View) (View, error) {
	rs, err := s.d.Store.ReviewRequests().State(ctx, v.UserID, s.d.Now().Add(-ReviewRequestRange))
	if err != nil {
		return v, unavailable(err)
	}
	v.ReviewRequestedAt = rs.OpenSince
	v.CanRequestReview = v.Status != Good && rs.OpenSince == nil && rs.Recent < ReviewRequestsMax
	return v, nil
}

func (s *Service) closeReviewIfGood(ctx context.Context, before, after postgres.ModerationState) {
	if ParseStatus(after.Status) != Good || ParseStatus(before.Status) == Good {
		return
	}
	if _, err := s.d.Store.ReviewRequests().Close(ctx, after.UserID, postgres.ReviewClosedStatusGood, s.d.Now()); err != nil {
		s.d.Log.WarnContext(ctx, "close account review request", "user_id", after.UserID, "error", err)
	}
}
