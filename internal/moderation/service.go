package moderation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	MaxNote          = 500
	MaxReportsPerDay = 20
)

var (
	ErrNotFound       = errors.New("not found")
	ErrInvalid        = errors.New("invalid report")
	ErrRateLimited    = errors.New("too many reports")
	ErrUnavailable    = errors.New("moderation temporarily unavailable")
	ErrAppealTooEarly = errors.New("appeal not allowed yet")
)

var Reasons = map[string]bool{
	"spam": true, "scam": true, "harassment": true, "impersonation": true, "inappropriate_profile": true, "other": true,
}

type ErrRestricted struct {
	Action Action
}

func (e *ErrRestricted) Error() string { return "account restricted: " + string(e.Action) }

func Restriction(err error) (Action, bool) {
	var r *ErrRestricted
	if errors.As(err, &r) {
		return r.Action, true
	}
	return "", false
}

type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

func (e *FieldError) Unwrap() error { return ErrInvalid }

func unavailable(err error) error { return fmt.Errorf("%w: %w", ErrUnavailable, err) }

type Store interface {
	Moderation() postgres.ModerationRepository
	Users() postgres.UserRepository
	Social() postgres.SocialRepository
	ReviewRequests() postgres.ReviewRequestRepository
}

type Cache interface {
	Get(ctx context.Context, userID int64) (string, bool, error)
	Set(ctx context.Context, userID int64, status string) error
	GetMany(ctx context.Context, userIDs []int64) (map[int64]string, error)
	SetMany(ctx context.Context, statuses map[int64]string) error
	Invalidate(ctx context.Context, userID int64) error
}

type EventLog interface {
	Emit(ctx context.Context, e events.Event) error
}

type Notifier interface {
	NotifyUser(ctx context.Context, uid int64, kind string, data any)
}

type Metrics interface {
	ModerationStatusChange(status string)
	UserReport(reason, weightBucket string)
}

type Deps struct {
	Store     Store
	Cache     Cache
	Events    EventLog
	Notifier  Notifier
	Metrics   Metrics
	OnBlocked func(ctx context.Context, blocker, blocked int64)
	Log       *slog.Logger
	Now       func() time.Time
}

type Service struct {
	d Deps
}

func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

func (s *Service) status(ctx context.Context, userID int64) (Status, error) {
	if s.d.Cache != nil {
		if v, ok, err := s.d.Cache.Get(ctx, userID); err == nil && ok {
			return ParseStatus(v), nil
		}
	}
	st, err := s.d.Store.Moderation().State(ctx, userID)
	if err != nil {
		return Good, unavailable(err)
	}
	if s.d.Cache != nil {
		if err := s.d.Cache.Set(ctx, userID, st.Status); err != nil {
			s.d.Log.WarnContext(ctx, "cache moderation status", "error", err)
		}
	}
	return ParseStatus(st.Status), nil
}

func (s *Service) Allow(ctx context.Context, actor int64, action Action, target int64) error {
	st, err := s.status(ctx, actor)
	if err != nil {
		return err
	}
	switch st {
	case Good, Warned:
		return nil
	case Suspended:
		return &ErrRestricted{Action: action}
	}
	switch action {
	case Send, ManageGroup:
		return nil
	case StartDirect, InviteToGroup, Call:
		if target == actor {
			return nil
		}
		if target == 0 {
			if action == Call && st == Restricted {
				return &ErrRestricted{Action: action}
			}
			return nil
		}
		shared, err := s.d.Store.Social().SharedChatViewers(ctx, target, []int64{actor})
		if err != nil {
			return unavailable(err)
		}
		if !shared[actor] {
			return &ErrRestricted{Action: action}
		}
		if action != StartDirect {
			return nil
		}
		n, err := s.d.Store.Moderation().DirectChatsStartedSince(ctx, actor, s.d.Now().Add(-24*time.Hour))
		if err != nil {
			return unavailable(err)
		}
		if n >= DirectChatsPerDay {
			return &ErrRestricted{Action: action}
		}
		return nil
	}
	if st.Level() >= Restricted.Level() {
		return &ErrRestricted{Action: action}
	}
	return nil
}

func (s *Service) RoutesToRequests(ctx context.Context, actor int64) (bool, error) {
	st, err := s.status(ctx, actor)
	if err != nil {
		return false, err
	}
	return st.Level() >= Limited.Level(), nil
}

func (s *Service) Hidden(ctx context.Context, userIDs []int64) (map[int64]bool, error) {
	statuses, err := s.d.Store.Moderation().Statuses(ctx, userIDs)
	if err != nil {
		return nil, unavailable(err)
	}
	out := make(map[int64]bool, len(statuses))
	for id, st := range statuses {
		if ParseStatus(st).Level() >= Restricted.Level() {
			out[id] = true
		}
	}
	return out, nil
}

func (s *Service) Labels(ctx context.Context, userIDs []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	missing := userIDs
	if s.d.Cache != nil {
		if cached, err := s.d.Cache.GetMany(ctx, userIDs); err == nil {
			missing = make([]int64, 0, len(userIDs)-len(cached))
			for _, id := range userIDs {
				st, ok := cached[id]
				switch {
				case !ok:
					missing = append(missing, id)
				case ParseStatus(st) == Suspended:
					out[id] = true
				}
			}
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	statuses, err := s.d.Store.Moderation().Statuses(ctx, missing)
	if err != nil {
		return nil, unavailable(err)
	}
	fill := make(map[int64]string, len(missing))
	for _, id := range missing {
		st := string(Good)
		if v, ok := statuses[id]; ok {
			st = v
		}
		fill[id] = st
		if ParseStatus(st) == Suspended {
			out[id] = true
		}
	}
	if s.d.Cache != nil {
		if err := s.d.Cache.SetMany(ctx, fill); err != nil {
			s.d.Log.WarnContext(ctx, "cache moderation statuses", "error", err)
		}
	}
	return out, nil
}

func cleanNote(note *string) (*string, error) { return cleanText("note", note, MaxNote) }

func cleanText(field string, value *string, maxRunes int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	t := strings.TrimSpace(*value)
	if t == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(t) > maxRunes {
		return nil, &FieldError{Field: field, Message: fmt.Sprintf("at most %d characters", maxRunes)}
	}
	if strings.IndexFunc(t, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) >= 0 {
		return nil, &FieldError{Field: field, Message: "no control characters"}
	}
	return &t, nil
}

type snapshot struct {
	DisplayName        string  `json:"display_name"`
	Username           string  `json:"username"`
	AvatarAttachmentID *string `json:"avatar_attachment_id"`
	Bio                *string `json:"bio"`
}

func snapshotOf(u postgres.User) []byte {
	snap := snapshot{DisplayName: u.DisplayName, Username: u.Username, Bio: u.Bio}
	if u.AvatarAttachmentID != nil {
		id := u.AvatarAttachmentID.String()
		snap.AvatarAttachmentID = &id
	}
	b, _ := json.Marshal(snap)
	return b
}

func (s *Service) Report(ctx context.Context, reporter, target int64, reason string, note *string, block bool) error {
	if !Reasons[reason] {
		return &FieldError{Field: "reason", Message: "unknown reason"}
	}
	if reporter == target {
		return &FieldError{Field: "user_id", Message: "you cannot report yourself"}
	}
	note, err := cleanNote(note)
	if err != nil {
		return err
	}
	users, err := s.d.Store.Users().GetMany(ctx, []int64{reporter, target})
	if err != nil {
		return unavailable(err)
	}
	subject, ok := users[target]
	if !ok {
		return ErrNotFound
	}
	me, ok := users[reporter]
	if !ok {
		return ErrNotFound
	}
	now := s.d.Now()
	repo := s.d.Store.Moderation()
	pair, err := repo.PairContext(ctx, reporter, target)
	if err != nil {
		return unavailable(err)
	}
	reporterState, err := repo.State(ctx, reporter)
	if err != nil {
		return unavailable(err)
	}
	busy, err := repo.ReportedUsersSince(ctx, reporter, target, now.Add(-BusyWindow))
	if err != nil {
		return unavailable(err)
	}
	burst := false
	if pair == postgres.ContextNone {
		n, err := repo.UnconnectedReportsSince(ctx, target, reporter, now.Add(-BurstWindow))
		if err != nil {
			return unavailable(err)
		}
		burst = n+1 > BurstMax
	}
	weight := Weight(WeightInput{
		Context: pair, ReporterAge: now.Sub(me.CreatedAt), ReporterStatus: ParseStatus(reporterState.Status),
		ReportedUsers: busy, Burst: burst,
	})
	inserted, err := repo.SaveReport(ctx, postgres.UserReport{
		ReporterID: reporter, TargetID: target, Reason: reason, Note: note, Context: pair, Weight: weight,
		Snapshot: snapshotOf(subject), Now: now,
	}, postgres.ReportRules{RateSince: now.Add(-24 * time.Hour), MaxInRate: MaxReportsPerDay, Burst: burst, BurstSince: now.Add(-BurstWindow)})
	if errors.Is(err, postgres.ErrLimit) {
		return ErrRateLimited
	}
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return unavailable(err)
	}
	if inserted && s.d.Metrics != nil {
		s.d.Metrics.UserReport(reason, WeightBucket(weight))
	}
	if block {
		if err := s.d.Store.Social().Block(ctx, reporter, target); err != nil {
			return unavailable(err)
		}
		if s.d.OnBlocked != nil {
			s.d.OnBlocked(context.WithoutCancel(ctx), reporter, target)
		}
	}
	s.emit(ctx, target, reporter)
	return nil
}

func (s *Service) Unreport(ctx context.Context, reporter, target int64) error {
	removed, err := s.d.Store.Moderation().WithdrawReport(ctx, reporter, target, s.d.Now())
	if err != nil {
		return unavailable(err)
	}
	if removed {
		s.emit(ctx, target, reporter)
	}
	return nil
}

func (s *Service) emit(ctx context.Context, target, reporter int64) {
	if s.d.Events == nil {
		return
	}
	if err := s.d.Events.Emit(context.WithoutCancel(ctx), events.Event{Type: events.UserReported, UserID: target, ActorID: reporter}); err != nil {
		s.d.Log.WarnContext(ctx, "emit user.reported", "error", err)
	}
}

type View struct {
	UserID            int64
	Status            Status
	Since             *time.Time
	Restrictions      []Action
	CanAppeal         bool
	ProbationUntil    *time.Time
	DecidedBy         string
	ReasonCode        *string
	ReasonText        *string
	ReviewRequestedAt *time.Time
	CanRequestReview  bool
}

func (s *Service) view(st postgres.ModerationState) View {
	v := View{
		UserID: st.UserID, Status: ParseStatus(st.Status), Restrictions: Restrictions(ParseStatus(st.Status)), CanAppeal: CanAppeal(st, s.d.Now()),
		ProbationUntil: st.ProbationUntil, DecidedBy: postgres.DecidedAutomatic,
	}
	if !st.Since.IsZero() {
		since := st.Since
		v.Since = &since
	}
	if st.DecidedBy == postgres.DecidedStaff {
		v.DecidedBy, v.ReasonCode, v.ReasonText = postgres.DecidedStaff, st.ReasonCode, st.ReasonText
	}
	return v
}

func (s *Service) AccountStatus(ctx context.Context, userID int64) (View, error) {
	st, err := s.d.Store.Moderation().State(ctx, userID)
	if err != nil {
		return View{}, unavailable(err)
	}
	return s.withReview(ctx, s.view(st))
}

func (s *Service) Appeal(ctx context.Context, userID int64) (View, error) {
	now := s.d.Now()
	current, err := s.d.Store.Moderation().State(ctx, userID)
	if err != nil {
		return View{}, unavailable(err)
	}
	if !CanAppeal(current, now) {
		return View{}, ErrAppealTooEarly
	}
	refused := false
	before, after, err := s.d.Store.Moderation().Transition(ctx, userID, now.Add(-HalfWeightAge), now, func(cur postgres.ModerationState, _ []postgres.ReportFact) postgres.ModerationState {
		refused = !CanAppeal(cur, now)
		if refused {
			return cur
		}
		return Appeal(cur, now)
	})
	if err != nil {
		return View{}, unavailable(err)
	}
	if refused {
		return View{}, ErrAppealTooEarly
	}
	s.changed(ctx, before, after)
	return s.withReview(ctx, s.view(after))
}

func (s *Service) Recompute(ctx context.Context, userID int64) error {
	_, _, err := s.recompute(ctx, userID, "")
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	return err
}

func (s *Service) recompute(ctx context.Context, userID int64, cause string) (before, after postgres.ModerationState, err error) {
	now := s.d.Now()
	before, after, err = s.d.Store.Moderation().Transition(ctx, userID, now.Add(-HalfWeightAge), now, func(cur postgres.ModerationState, facts []postgres.ReportFact) postgres.ModerationState {
		next := Next(cur, facts, now)
		if cause != "" && next.Status != cur.Status {
			next.Cause = cause
		}
		return next
	})
	if err != nil {
		return before, after, err
	}
	s.changed(ctx, before, after)
	return before, after, nil
}

func sameDecision(a, b postgres.ModerationState) bool {
	return a.DecidedBy == b.DecidedBy && equalTime(a.DecidedAt, b.DecidedAt) && equalString(a.ReasonCode, b.ReasonCode) && equalString(a.ReasonText, b.ReasonText)
}

func equalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *Service) changed(ctx context.Context, before, after postgres.ModerationState) {
	if before.Status == after.Status && equalTime(before.ProbationUntil, after.ProbationUntil) && sameDecision(before, after) {
		return
	}
	if s.d.Cache != nil {
		if err := s.d.Cache.Invalidate(ctx, after.UserID); err != nil {
			s.d.Log.WarnContext(ctx, "invalidate moderation status", "error", err)
		}
	}
	if before.Status != after.Status {
		if s.d.Metrics != nil {
			s.d.Metrics.ModerationStatusChange(after.Status)
		}
		s.d.Log.InfoContext(ctx, "account status changed", "user_id", after.UserID, "from", before.Status, "to", after.Status, "cause", after.Cause, "decided_by", after.DecidedBy)
	}
	s.closeReviewIfGood(ctx, before, after)
	if s.d.Notifier != nil {
		v, err := s.withReview(ctx, s.view(after))
		if err != nil {
			s.d.Log.WarnContext(ctx, "read account review request", "user_id", after.UserID, "error", err)
		}
		s.d.Notifier.NotifyUser(ctx, after.UserID, "account_status", EventData(v))
	}
}

func equalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

type StatusEvent struct {
	Status            Status     `json:"status"`
	Since             *time.Time `json:"since"`
	Restrictions      []Action   `json:"restrictions"`
	CanAppeal         bool       `json:"can_appeal"`
	ProbationUntil    *time.Time `json:"probation_until"`
	DecidedBy         string     `json:"decided_by"`
	ReasonCode        *string    `json:"reason_code"`
	ReasonText        *string    `json:"reason_text"`
	ReviewRequestedAt *time.Time `json:"review_requested_at"`
	CanRequestReview  bool       `json:"can_request_review"`
}

func EventData(v View) StatusEvent {
	out := StatusEvent{
		Status: v.Status, Restrictions: v.Restrictions, CanAppeal: v.CanAppeal, DecidedBy: v.DecidedBy, ReasonCode: v.ReasonCode, ReasonText: v.ReasonText,
		CanRequestReview: v.CanRequestReview,
	}
	if v.ReviewRequestedAt != nil {
		t := v.ReviewRequestedAt.UTC()
		out.ReviewRequestedAt = &t
	}
	if out.DecidedBy == "" {
		out.DecidedBy = postgres.DecidedAutomatic
	}
	if v.Since != nil {
		t := v.Since.UTC()
		out.Since = &t
	}
	if v.ProbationUntil != nil {
		t := v.ProbationUntil.UTC()
		out.ProbationUntil = &t
	}
	return out
}

const duePage = 200

func (s *Service) Daily(ctx context.Context) (checked int, purged int64, err error) {
	repo := s.d.Store.Moderation()
	var after int64
	for {
		ids, err := repo.Due(ctx, after, duePage)
		if err != nil {
			return checked, 0, err
		}
		for _, id := range ids {
			if err := s.Recompute(ctx, id); err != nil {
				return checked, 0, err
			}
			checked++
			after = id
		}
		if len(ids) < duePage {
			break
		}
	}
	purged, err = repo.PurgeReports(ctx, s.d.Now().Add(-ReportRetention))
	if err != nil {
		return checked, purged, err
	}
	outcomes, err := repo.PurgeReportOutcomes(ctx, s.d.Now().Add(-OutcomeRetention))
	return checked, purged + outcomes, err
}
