package httpapi

import "net/http"

func (a *AuthServer) GetRecoveryOverview(w http.ResponseWriter, r *http.Request, _ GetRecoveryOverviewParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	result, err := a.svc.RecoveryOverview(r.Context(), p)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, result)
}
func (a *AuthServer) GetRecoveryEmail(w http.ResponseWriter, r *http.Request, _ GetRecoveryEmailParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	result, err := a.svc.RecoveryEmailStatus(r.Context(), p)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, result)
}
func (a *AuthServer) SetRecoveryEmail(w http.ResponseWriter, r *http.Request, _ SetRecoveryEmailParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := a.svc.SetRecoveryEmail(r.Context(), a.clientOf(r), p, body.Email, body.Password, body.Code); err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	w.WriteHeader(http.StatusNoContent)
}
func (a *AuthServer) RemoveRecoveryEmail(w http.ResponseWriter, r *http.Request, _ RemoveRecoveryEmailParams) {
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
	if err := a.svc.RemoveRecoveryEmail(r.Context(), a.clientOf(r), p, body.Password, body.Code); err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	w.WriteHeader(http.StatusNoContent)
}
func (a *AuthServer) VerifyRecoveryEmail(w http.ResponseWriter, r *http.Request, _ VerifyRecoveryEmailParams) {
	var body struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := a.svc.VerifyRecoveryEmail(r.Context(), a.clientOf(r), body.Token); err != nil {
		a.fail(w, r, err, false)
		return
	}
	noStore(w)
	w.WriteHeader(http.StatusNoContent)
}
func (a *AuthServer) RequestEmailReset(w http.ResponseWriter, r *http.Request, _ RequestEmailResetParams) {
	var body struct {
		Identifier string `json:"username_or_email"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := a.svc.RequestEmailReset(r.Context(), a.clientOf(r), body.Identifier); err != nil {
		a.fail(w, r, err, false)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"message": "If an account with a verified email exists, we have sent a recovery link."})
}
func (a *AuthServer) ConfirmEmailReset(w http.ResponseWriter, r *http.Request, _ ConfirmEmailResetParams) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"new_password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	tokens, err := a.svc.ResetWithEmail(r.Context(), a.clientOf(r), body.Token, body.Password, body.Code)
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	a.writeRecoveryTokens(w, tokens)
}
