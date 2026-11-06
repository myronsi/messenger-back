package httpapi

import (
	"net/http"
)

func (a *AuthServer) CreateQrLogin(w http.ResponseWriter, r *http.Request, _ CreateQrLoginParams) {
	result, err := a.svc.StartQRLogin(r.Context(), a.clientOf(r))
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusCreated, result)
}

func (a *AuthServer) PollQrLogin(w http.ResponseWriter, r *http.Request, requestId QrLoginRequestId, _ PollQrLoginParams) {
	var body struct {
		PollToken string `json:"poll_token"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := a.svc.PollQRLogin(r.Context(), a.clientOf(r), requestId, body.PollToken)
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	noStore(w)
	if result.Tokens == nil {
		writeJSONStatus(w, http.StatusOK, map[string]string{"status": result.Status})
		return
	}
	a.setRefreshCookie(w, result.Tokens)
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"status": result.Status, "access_token": result.Tokens.AccessToken, "token_type": TokenResponseTokenTypeBearer, "expires_in": expiresIn(result.Tokens),
	})
}

func (a *AuthServer) GetQrLogin(w http.ResponseWriter, r *http.Request, requestId QrLoginRequestId, _ GetQrLoginParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	info, err := a.svc.ScanQRLogin(r.Context(), p, requestId)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, info)
}

func (a *AuthServer) ApproveQrLogin(w http.ResponseWriter, r *http.Request, requestId QrLoginRequestId, _ ApproveQrLoginParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if r.ContentLength != 0 || r.Header.Get("Content-Type") != "" {
		if !decode(w, r, &body) {
			return
		}
	}
	if err := a.svc.ApproveQRLogin(r.Context(), a.clientOf(r), p, requestId, body.Code); err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *AuthServer) DenyQrLogin(w http.ResponseWriter, r *http.Request, requestId QrLoginRequestId, _ DenyQrLoginParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if err := a.svc.DenyQRLogin(r.Context(), p, requestId); err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	w.WriteHeader(http.StatusNoContent)
}
