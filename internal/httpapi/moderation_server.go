package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/myronsi/messenger-back/internal/moderation"
)

type Moderator interface {
	Allow(ctx context.Context, actor int64, action moderation.Action, target int64) error
	Hidden(ctx context.Context, userIDs []int64) (map[int64]bool, error)
}

type ModerationService interface {
	Moderator
	Report(ctx context.Context, reporter, target int64, reason string, note *string, block bool) error
	Unreport(ctx context.Context, reporter, target int64) error
	AccountStatus(ctx context.Context, userID int64) (moderation.View, error)
	Appeal(ctx context.Context, userID int64) (moderation.View, error)
	ReportOutcomes(ctx context.Context, reporterID int64) ([]moderation.Outcome, error)
	MarkOutcomeRead(ctx context.Context, reporterID, id int64) error
}

type ModerationOptions struct {
	Service ModerationService
	Log     *slog.Logger
}

type ModerationServer struct {
	o ModerationOptions
}

func NewModerationServer(o ModerationOptions) *ModerationServer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &ModerationServer{o: o}
}

func WriteRestricted(w http.ResponseWriter, action moderation.Action) {
	p := struct {
		Type        string             `json:"type"`
		Title       string             `json:"title"`
		Status      int                `json:"status"`
		Code        ErrorCode          `json:"code"`
		Restriction AccountRestriction `json:"restriction"`
	}{
		Type: "about:blank", Title: http.StatusText(http.StatusForbidden), Status: http.StatusForbidden,
		Code: ErrorCodeAccountRestricted, Restriction: AccountRestriction(action),
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(p)
}

func writeIfRestricted(w http.ResponseWriter, err error) bool {
	action, ok := moderation.Restriction(err)
	if ok {
		WriteRestricted(w, action)
	}
	return ok
}

func guard(w http.ResponseWriter, r *http.Request, m Moderator, log *slog.Logger, action moderation.Action) bool {
	if m == nil {
		return true
	}
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		return true
	}
	err := m.Allow(r.Context(), p.UserID, action, 0)
	if err == nil {
		return true
	}
	if writeIfRestricted(w, err) {
		return false
	}
	log.WarnContext(r.Context(), "moderation check", "error", err)
	w.Header().Set("Retry-After", "2")
	WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
	return false
}

func (s *ModerationServer) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	var field *moderation.FieldError
	switch {
	case errors.As(err, &field):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: field.Field, Message: field.Message})
	case errors.Is(err, moderation.ErrNotFound):
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
	case errors.Is(err, moderation.ErrRateLimited):
		w.Header().Set("Retry-After", "3600")
		WriteProblem(w, http.StatusTooManyRequests, ErrorCodeRateLimited)
	case errors.Is(err, moderation.ErrAppealTooEarly):
		WriteProblem(w, http.StatusConflict, ErrorCodeConflict)
	case errors.Is(err, moderation.ErrUnavailable):
		s.o.Log.WarnContext(r.Context(), what, "error", err)
		w.Header().Set("Retry-After", "2")
		WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
	default:
		s.o.Log.ErrorContext(r.Context(), what, "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
	}
}

func PresentAccountStatus(v moderation.View) AccountStatus {
	out := AccountStatus{
		Status: AccountStatusLevel(v.Status), CanAppeal: v.CanAppeal,
		Restrictions: make([]AccountRestriction, 0, len(v.Restrictions)),
	}
	for _, a := range v.Restrictions {
		out.Restrictions = append(out.Restrictions, AccountRestriction(a))
	}
	if v.Since != nil {
		t := utc(*v.Since)
		out.Since = &t
	}
	if v.ProbationUntil != nil {
		t := utc(*v.ProbationUntil)
		out.ProbationUntil = &t
	}
	decided := Automatic
	if v.DecidedBy == string(Staff) {
		decided = Staff
		out.ReasonText = v.ReasonText
		if v.ReasonCode != nil {
			code := ModerationReason(*v.ReasonCode)
			out.ReasonCode = &code
		}
	}
	out.DecidedBy = &decided
	return out
}

func PresentReportOutcome(o moderation.Outcome) ReportOutcome {
	return ReportOutcome{
		Id: strconv.FormatInt(o.ID, 10), SubjectType: ReportSubjectType(o.SubjectType), SubjectId: strconv.FormatInt(o.SubjectID, 10),
		Outcome: ReportOutcomeKind(o.Outcome), CreatedAt: utc(o.CreatedAt), Read: o.Read,
	}
}

func (s *ModerationServer) ReportUser(w http.ResponseWriter, r *http.Request, userID UserId, _ ReportUserParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	target, ok := parseUserID(w, userID)
	if !ok {
		return
	}
	var body ReportUserRequest
	if !decode(w, r, &body) {
		return
	}
	block := body.Block == nil || *body.Block
	if err := s.o.Service.Report(r.Context(), p.UserID, target, string(body.Reason), body.Note, block); err != nil {
		s.fail(w, r, "report user", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *ModerationServer) UnreportUser(w http.ResponseWriter, r *http.Request, userID UserId, _ UnreportUserParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	target, ok := parseUserID(w, userID)
	if !ok {
		return
	}
	if err := s.o.Service.Unreport(r.Context(), p.UserID, target); err != nil {
		s.fail(w, r, "withdraw report", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *ModerationServer) GetAccountStatus(w http.ResponseWriter, r *http.Request, _ GetAccountStatusParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	v, err := s.o.Service.AccountStatus(r.Context(), p.UserID)
	if err != nil {
		s.fail(w, r, "account status", err)
		return
	}
	writeJSONStatus(w, http.StatusOK, PresentAccountStatus(v))
}

func (s *ModerationServer) AppealAccountStatus(w http.ResponseWriter, r *http.Request, _ AppealAccountStatusParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	v, err := s.o.Service.Appeal(r.Context(), p.UserID)
	if err != nil {
		s.fail(w, r, "appeal account status", err)
		return
	}
	writeJSONStatus(w, http.StatusOK, PresentAccountStatus(v))
}

func (s *ModerationServer) ListReportOutcomes(w http.ResponseWriter, r *http.Request, _ ListReportOutcomesParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	list, err := s.o.Service.ReportOutcomes(r.Context(), p.UserID)
	if err != nil {
		s.fail(w, r, "report outcomes", err)
		return
	}
	out := ReportOutcomeList{Items: make([]ReportOutcome, len(list))}
	for i, o := range list {
		out.Items[i] = PresentReportOutcome(o)
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (s *ModerationServer) MarkReportOutcomeRead(w http.ResponseWriter, r *http.Request, outcomeID ReportOutcomeId, _ MarkReportOutcomeReadParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "outcome_id", outcomeID)
	if !ok {
		return
	}
	if err := s.o.Service.MarkOutcomeRead(r.Context(), p.UserID, id); err != nil {
		s.fail(w, r, "mark report outcome read", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
