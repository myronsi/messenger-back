package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/myronsi/messenger-back/internal/auth"
)

func (a *AuthServer) StartDeviceRecovery(w http.ResponseWriter, r *http.Request, _ StartDeviceRecoveryParams) {
	var body struct {
		Username string `json:"username"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := a.svc.StartDeviceRecovery(r.Context(), a.clientOf(r), body.Username)
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, result)
}

func (a *AuthServer) DeviceRecoveryStatus(w http.ResponseWriter, r *http.Request, requestId string, _ DeviceRecoveryStatusParams) {
	status, err := a.svc.DeviceRecoveryStatus(r.Context(), a.clientOf(r), requestId)
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, map[string]string{"status": status})
}

func (a *AuthServer) ApproveDeviceRecovery(w http.ResponseWriter, r *http.Request, requestId string, _ ApproveDeviceRecoveryParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Match   int    `json:"match_number"`
		Code    string `json:"code"`
		Passkey *struct {
			ChallengeID string          `json:"challenge_id"`
			Credential  json.RawMessage `json:"credential"`
		} `json:"passkey"`
	}
	if !decode(w, r, &body) {
		return
	}
	approval := auth.RecoveryApproval{Match: body.Match, Code: body.Code}
	if body.Passkey != nil {
		approval.ChallengeID, approval.Credential = body.Passkey.ChallengeID, body.Passkey.Credential
	}
	status, err := a.svc.DecideDeviceRecovery(r.Context(), a.clientOf(r), p, requestId, approval, false)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, map[string]string{"status": status})
}

func (a *AuthServer) DeviceRecoveryApprovalOptions(w http.ResponseWriter, r *http.Request, requestId string, _ DeviceRecoveryApprovalOptionsParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	options, err := a.svc.DeviceRecoveryApprovalOptions(r.Context(), a.clientOf(r), p, requestId)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, options)
}

func (a *AuthServer) DenyDeviceRecovery(w http.ResponseWriter, r *http.Request, requestId string, _ DenyDeviceRecoveryParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	status, err := a.svc.DecideDeviceRecovery(r.Context(), a.clientOf(r), p, requestId, auth.RecoveryApproval{}, true)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, map[string]string{"status": status})
}

func (a *AuthServer) CancelDeviceRecovery(w http.ResponseWriter, r *http.Request, requestId string, _ CancelDeviceRecoveryParams) {
	if err := a.svc.CancelDeviceRecovery(r.Context(), a.clientOf(r), requestId); err != nil {
		a.fail(w, r, err, false)
		return
	}
	noStore(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *AuthServer) CompleteDeviceRecovery(w http.ResponseWriter, r *http.Request, requestId string, _ CompleteDeviceRecoveryParams) {
	var body struct {
		Password string `json:"new_password"`
	}
	if !decode(w, r, &body) {
		return
	}
	tokens, err := a.svc.CompleteDeviceRecovery(r.Context(), a.clientOf(r), requestId, body.Password)
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	a.writeRecoveryTokens(w, tokens)
}

func (a *AuthServer) writeRecoveryTokens(w http.ResponseWriter, tokens *auth.TokenSet) {
	a.setRefreshCookie(w, tokens)
	noStore(w)
	writeJSONStatus(w, http.StatusOK, map[string]any{"access_token": tokens.AccessToken, "token_type": "Bearer", "expires_in": expiresIn(tokens), "recovery_key": tokens.RecoveryKey, "username": tokens.User.Username})
}
