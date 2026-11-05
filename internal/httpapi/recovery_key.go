package httpapi

import (
	"net/http"
	"strconv"
)

func (a *AuthServer) GetRecoveryKey(w http.ResponseWriter, r *http.Request, _ GetRecoveryKeyParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	status, err := a.svc.RecoveryKeyStatus(r.Context(), p)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, status)
}

func (a *AuthServer) ReplaceRecoveryKey(w http.ResponseWriter, r *http.Request, _ ReplaceRecoveryKeyParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	key, err := a.svc.ReplaceRecoveryKey(r.Context(), a.clientOf(r), p, body.Password, body.Code)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, map[string]string{"recovery_key": key})
}

func (a *AuthServer) RecoverWithKey(w http.ResponseWriter, r *http.Request, _ RecoverWithKeyParams) {
	var body struct {
		Username    string `json:"username"`
		RecoveryKey string `json:"recovery_key"`
		NewPassword string `json:"new_password"`
		Code        string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	tokens, err := a.svc.RecoverWithKey(r.Context(), a.clientOf(r), body.Username, body.RecoveryKey, body.NewPassword, body.Code)
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	a.setRefreshCookie(w, tokens)
	noStore(w)
	writeJSONStatus(w, http.StatusOK, map[string]any{"access_token": tokens.AccessToken, "token_type": "Bearer", "expires_in": expiresIn(tokens), "recovery_key": tokens.RecoveryKey})
}

func (a *AuthServer) ListSecurityEvents(w http.ResponseWriter, r *http.Request, _ ListSecurityEventsParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	events, err := a.svc.SecurityEvents(r.Context(), p)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	items := make([]map[string]any, 0, len(events))
	for _, e := range events {
		items = append(items, map[string]any{"id": strconv.FormatInt(e.ID, 10), "type": e.Type, "created_at": e.CreatedAt, "user_agent": e.UserAgent})
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, map[string]any{"items": items})
}
