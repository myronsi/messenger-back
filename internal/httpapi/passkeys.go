package httpapi

import (
	"encoding/json"
	"net/http"
)

func (a *AuthServer) PasskeyRegistrationOptions(w http.ResponseWriter, r *http.Request, _ PasskeyRegistrationOptionsParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
		Name     string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := a.svc.BeginPasskeyRegistration(r.Context(), a.clientOf(r), p, body.Password, body.Code, body.Name)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, result)
}

func (a *AuthServer) VerifyPasskeyRegistration(w http.ResponseWriter, r *http.Request, _ VerifyPasskeyRegistrationParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		ID         string          `json:"challenge_id"`
		Credential json.RawMessage `json:"credential"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := a.svc.FinishPasskeyRegistration(r.Context(), a.clientOf(r), p, body.ID, body.Credential); err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *AuthServer) PasskeyLoginOptions(w http.ResponseWriter, r *http.Request, _ PasskeyLoginOptionsParams) {
	var body struct {
		Username string `json:"username"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := a.svc.BeginPasskeyLogin(r.Context(), a.clientOf(r), body.Username)
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, result)
}

func (a *AuthServer) VerifyPasskeyLogin(w http.ResponseWriter, r *http.Request, _ VerifyPasskeyLoginParams) {
	var body struct {
		ID         string          `json:"challenge_id"`
		Credential json.RawMessage `json:"credential"`
	}
	if !decode(w, r, &body) {
		return
	}
	tokens, err := a.svc.FinishPasskeyLogin(r.Context(), a.clientOf(r), body.ID, body.Credential)
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	a.writeTokens(w, http.StatusOK, tokens)
}

func (a *AuthServer) ListPasskeys(w http.ResponseWriter, r *http.Request, _ ListPasskeysParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	items, err := a.svc.ListPasskeys(r.Context(), p)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, map[string]any{"items": items})
}

func (a *AuthServer) RenamePasskey(w http.ResponseWriter, r *http.Request, passkeyId string, _ RenamePasskeyParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := a.svc.RenamePasskey(r.Context(), a.clientOf(r), p, passkeyId, body.Name); err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *AuthServer) RemovePasskey(w http.ResponseWriter, r *http.Request, passkeyId string, _ RemovePasskeyParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := a.svc.DeletePasskey(r.Context(), a.clientOf(r), p, passkeyId, body.Password); err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	w.WriteHeader(http.StatusNoContent)
}
