package httpapi

import (
	"net/http"

	"github.com/myronsi/messenger-back/internal/moderation"
)

func (s *Server) WithModeration(m *ModerationServer) *Server { s.moderation = m; return s }

func (s *Server) allowed(w http.ResponseWriter, r *http.Request, action moderation.Action) bool {
	if s.moderation == nil {
		return true
	}
	return guard(w, r, s.moderation.o.Service, s.moderation.o.Log, action)
}

func (s *Server) ReportUser(w http.ResponseWriter, r *http.Request, userID UserId, params ReportUserParams) {
	if s.moderation == nil {
		s.AuthServer.ReportUser(w, r, userID, params)
		return
	}
	s.moderation.ReportUser(w, r, userID, params)
}

func (s *Server) UnreportUser(w http.ResponseWriter, r *http.Request, userID UserId, params UnreportUserParams) {
	if s.moderation == nil {
		s.AuthServer.UnreportUser(w, r, userID, params)
		return
	}
	s.moderation.UnreportUser(w, r, userID, params)
}

func (s *Server) GetAccountStatus(w http.ResponseWriter, r *http.Request, params GetAccountStatusParams) {
	if s.moderation == nil {
		s.AuthServer.GetAccountStatus(w, r, params)
		return
	}
	s.moderation.GetAccountStatus(w, r, params)
}

func (s *Server) AppealAccountStatus(w http.ResponseWriter, r *http.Request, params AppealAccountStatusParams) {
	if s.moderation == nil {
		s.AuthServer.AppealAccountStatus(w, r, params)
		return
	}
	s.moderation.AppealAccountStatus(w, r, params)
}

func (s *Server) ListReportOutcomes(w http.ResponseWriter, r *http.Request, params ListReportOutcomesParams) {
	if s.moderation == nil {
		s.AuthServer.ListReportOutcomes(w, r, params)
		return
	}
	s.moderation.ListReportOutcomes(w, r, params)
}

func (s *Server) MarkReportOutcomeRead(w http.ResponseWriter, r *http.Request, outcomeID ReportOutcomeId, params MarkReportOutcomeReadParams) {
	if s.moderation == nil {
		s.AuthServer.MarkReportOutcomeRead(w, r, outcomeID, params)
		return
	}
	s.moderation.MarkReportOutcomeRead(w, r, outcomeID, params)
}
