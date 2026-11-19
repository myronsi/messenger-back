package moderation

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	MaxReasonText    = 1000
	MaxStaffRef      = 64
	OutcomeRetention = 90 * 24 * time.Hour
	OutcomesListed   = 50
)

var ErrForbidden = errors.New("not allowed for this subject")

var DecisionReasons = map[string]bool{
	"spam": true, "scam": true, "harassment": true, "impersonation": true, "inappropriate_profile": true, "illegal": true, "other": true,
}

type Decision struct {
	UserID     int64
	Status     Status
	ReasonCode string
	ReasonText string
	StaffRef   string
}

type DecisionResult struct {
	Before View
	After  View
}

type DismissResult struct {
	Dismissed int
	Before    View
	After     View
}

type PackKind = postgres.PackKind

const (
	StickerPack = postgres.StickerPackKind
	EmojiPack   = postgres.EmojiPackKind
)

type Outcome struct {
	ID          int64
	SubjectType string
	SubjectID   int64
	Outcome     string
	CreatedAt   time.Time
	Read        bool
}

type OutcomeEvent struct {
	ID          string    `json:"id"`
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
	Outcome     string    `json:"outcome"`
	CreatedAt   time.Time `json:"created_at"`
	Read        bool      `json:"read"`
}

func outcomeOf(o postgres.ReportOutcome) Outcome {
	return Outcome{ID: o.ID, SubjectType: o.SubjectType, SubjectID: o.SubjectID, Outcome: o.Outcome, CreatedAt: o.CreatedAt, Read: o.ReadAt != nil}
}

func OutcomeEventData(o Outcome) OutcomeEvent {
	return OutcomeEvent{
		ID: formatID(o.ID), SubjectType: o.SubjectType, SubjectID: formatID(o.SubjectID), Outcome: o.Outcome,
		CreatedAt: o.CreatedAt.UTC(), Read: o.Read,
	}
}

func cleanReasonText(text string) (string, error) {
	t := strings.TrimSpace(text)
	if t == "" || utf8.RuneCountInString(t) > MaxReasonText {
		return "", &FieldError{Field: "reason_text", Message: "1 to 1000 characters"}
	}
	if strings.IndexFunc(t, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) >= 0 {
		return "", &FieldError{Field: "reason_text", Message: "no control characters"}
	}
	return t, nil
}

func cleanStaffRef(ref string) (string, error) {
	if len(ref) > MaxStaffRef || strings.IndexFunc(ref, func(r rune) bool { return r < '!' || r > '~' }) >= 0 {
		return "", &FieldError{Field: "staff_ref", Message: "at most 64 printable ASCII characters"}
	}
	return ref, nil
}

func (s *Service) validDecision(d Decision) (Decision, error) {
	if !ValidStatus(string(d.Status)) {
		return d, &FieldError{Field: "status", Message: "unknown status"}
	}
	if !DecisionReasons[d.ReasonCode] {
		return d, &FieldError{Field: "reason_code", Message: "unknown reason"}
	}
	text, err := cleanReasonText(d.ReasonText)
	if err != nil {
		return d, err
	}
	ref, err := cleanStaffRef(d.StaffRef)
	if err != nil {
		return d, err
	}
	d.ReasonText, d.StaffRef = text, ref
	return d, nil
}

func (s *Service) mustExist(ctx context.Context, userID int64) error {
	users, err := s.d.Store.Users().GetMany(ctx, []int64{userID})
	if err != nil {
		return unavailable(err)
	}
	if _, ok := users[userID]; !ok {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Decide(ctx context.Context, d Decision) (DecisionResult, error) {
	d, err := s.validDecision(d)
	if err != nil {
		return DecisionResult{}, err
	}
	if err := s.mustExist(ctx, d.UserID); err != nil {
		return DecisionResult{}, err
	}
	now := s.d.Now()
	before, after, err := s.d.Store.Moderation().Transition(ctx, d.UserID, now.Add(-HalfWeightAge), now, func(cur postgres.ModerationState, facts []postgres.ReportFact) postgres.ModerationState {
		return Decide(cur, facts, d, now)
	})
	if errors.Is(err, postgres.ErrNotFound) {
		return DecisionResult{}, ErrNotFound
	}
	if err != nil {
		return DecisionResult{}, unavailable(err)
	}
	s.changed(ctx, before, after)
	outcome := postgres.OutcomeActionTaken
	if d.Status == Good {
		outcome = postgres.OutcomeNoViolation
	}
	outcomes, err := s.d.Store.Moderation().NotifyUserReporters(ctx, d.UserID, outcome, now)
	if err != nil {
		s.d.Log.WarnContext(ctx, "store report outcomes", "user_id", d.UserID, "error", err)
	}
	s.notifyOutcomes(ctx, outcomes)
	return DecisionResult{Before: s.view(before), After: s.view(after)}, nil
}

func (s *Service) DismissReports(ctx context.Context, userID int64, reporterIDs []int64) (DismissResult, error) {
	if err := s.mustExist(ctx, userID); err != nil {
		return DismissResult{}, err
	}
	if reporterIDs != nil && len(reporterIDs) == 0 {
		return DismissResult{}, &FieldError{Field: "reporter_ids", Message: "leave out to dismiss every report"}
	}
	now := s.d.Now()
	dismissed, outcomes, err := s.d.Store.Moderation().DismissReports(ctx, userID, reporterIDs, now)
	if err != nil {
		return DismissResult{}, unavailable(err)
	}
	s.notifyOutcomes(ctx, outcomes)
	before, after, err := s.recompute(ctx, userID, CauseDismissed)
	if err != nil {
		return DismissResult{}, unavailable(err)
	}
	return DismissResult{Dismissed: dismissed, Before: s.view(before), After: s.view(after)}, nil
}

func packError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, postgres.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, postgres.ErrForbidden):
		return ErrForbidden
	}
	return unavailable(err)
}

func (s *Service) HidePack(ctx context.Context, kind PackKind, packID int64) (bool, error) {
	changed, outcomes, err := s.d.Store.Moderation().HidePack(ctx, kind, packID, s.d.Now())
	if err != nil {
		return false, packError(err)
	}
	s.notifyOutcomes(ctx, outcomes)
	return changed, nil
}

func (s *Service) UnhidePack(ctx context.Context, kind PackKind, packID int64) (bool, error) {
	changed, outcomes, err := s.d.Store.Moderation().UnhidePack(ctx, kind, packID, s.d.Now())
	if err != nil {
		return false, packError(err)
	}
	s.notifyOutcomes(ctx, outcomes)
	return changed, nil
}

func (s *Service) notifyOutcomes(ctx context.Context, outcomes []postgres.ReportOutcome) {
	if s.d.Notifier == nil {
		return
	}
	for _, o := range outcomes {
		s.d.Notifier.NotifyUser(ctx, o.ReporterID, "report_outcome", OutcomeEventData(outcomeOf(o)))
	}
}

func (s *Service) ReportOutcomes(ctx context.Context, reporterID int64) ([]Outcome, error) {
	rows, err := s.d.Store.Moderation().ReportOutcomes(ctx, reporterID, s.d.Now().Add(-OutcomeRetention), OutcomesListed)
	if err != nil {
		return nil, unavailable(err)
	}
	out := make([]Outcome, len(rows))
	for i, r := range rows {
		out[i] = outcomeOf(r)
	}
	return out, nil
}

func (s *Service) MarkOutcomeRead(ctx context.Context, reporterID, id int64) error {
	err := s.d.Store.Moderation().MarkReportOutcomeRead(ctx, reporterID, id, s.d.Now())
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return unavailable(err)
	}
	return nil
}

func formatID(id int64) string { return strconv.FormatInt(id, 10) }
