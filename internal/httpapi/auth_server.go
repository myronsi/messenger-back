package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/myronsi/messenger-back/internal/auth"
)

// RefreshCookieName is the name of the refresh token cookie (the same as in the Python backend, so
// existing sessions keep working).
const RefreshCookieName = "refresh_token"

// AuthOptions configures AuthServer.
type AuthOptions struct {
	Service *auth.Service
	Log     *slog.Logger
	// BasePath is the base path of the API; the refresh cookie is limited to <BasePath>/auth/refresh.
	BasePath string
	// CookiePath overrides the path of the refresh cookie.
	CookiePath string
	// CookieSecure sets the Secure attribute of the refresh cookie.
	CookieSecure bool
	// TrustedProxies are the reverse proxies whose X-Forwarded-For and X-Real-IP headers are believed.
	TrustedProxies []netip.Prefix
}

// AuthServer implements the authentication, session and two-factor operations of the contract. The
// other operations answer 501 until their features land.
type AuthServer struct {
	Unimplemented
	svc          *auth.Service
	log          *slog.Logger
	basePath     string
	cookiePath   string
	cookieSecure bool

	trustedProxies []netip.Prefix
}

var _ ServerInterface = (*AuthServer)(nil)

// NewAuthServer builds the server.
func NewAuthServer(o AuthOptions) *AuthServer {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	path := o.CookiePath
	if path == "" {
		path = o.BasePath + "/auth/refresh"
	}
	return &AuthServer{svc: o.Service, log: log, basePath: o.BasePath, cookiePath: path, cookieSecure: o.CookieSecure, trustedProxies: o.TrustedProxies}
}

// ---------------------------------------------------------------- request and response helpers

// decode reads a JSON request body into dst; it writes the problem and returns false on failure.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		WriteProblem(w, http.StatusUnsupportedMediaType, ErrorCodeUnsupportedMediaType)
		return false
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeBodyError(w, err)
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("trailing data")
		}
		writeBodyError(w, err)
		return false
	}
	return true
}

func writeBodyError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		WriteProblem(w, http.StatusRequestEntityTooLarge, ErrorCodePayloadTooLarge)
		return
	}
	WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest)
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail maps a service error to a problem. withSession is true on operations that need an access token:
// a wrong password or code there must not be a 401, because clients treat 401 as a lost session and
// would refresh or sign the user out.
func (a *AuthServer) fail(w http.ResponseWriter, r *http.Request, err error, withSession bool) {
	var (
		limited *auth.RateLimitError
		invalid *auth.ValidationError
	)
	credStatus := http.StatusUnauthorized
	if withSession {
		credStatus = http.StatusForbidden
	}
	switch {
	case errors.As(err, &limited):
		secs := int(limited.RetryAfter/time.Second) + 1
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		WriteProblem(w, http.StatusTooManyRequests, ErrorCodeRateLimited)
	case errors.As(err, &invalid):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: invalid.Field, Message: invalid.Message})
	case errors.Is(err, auth.ErrUsernameTaken):
		WriteProblem(w, http.StatusConflict, ErrorCodeAlreadyExists)
	case errors.Is(err, auth.ErrInvalidCredentials):
		WriteProblem(w, credStatus, ErrorCodeInvalidCredentials)
	case errors.Is(err, auth.ErrInvalidTwoFactorCode):
		WriteProblem(w, credStatus, ErrorCodeInvalidTwoFactorCode)
	case errors.Is(err, auth.ErrInvalidChallenge), errors.Is(err, auth.ErrInvalidRefreshToken), errors.Is(err, auth.ErrRefreshSuperseded),
		errors.Is(err, auth.ErrInvalidRecovery), errors.Is(err, auth.ErrUnauthenticated), errors.Is(err, auth.ErrInvalidToken):
		WriteProblem(w, http.StatusUnauthorized, ErrorCodeUnauthenticated)
	case errors.Is(err, auth.ErrSessionNotFound):
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
	case errors.Is(err, auth.ErrTwoFactorEnabled), errors.Is(err, auth.ErrNoPendingTwoFactor),
		errors.Is(err, auth.ErrTwoFactorNotEnabled), errors.Is(err, auth.ErrConflict):
		WriteProblem(w, http.StatusConflict, ErrorCodeConflict)
	default:
		a.log.ErrorContext(r.Context(), "auth request failed", "err", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
	}
}

func (a *AuthServer) setRefreshCookie(w http.ResponseWriter, ts *auth.TokenSet) {
	maxAge := int(time.Until(ts.RefreshExpiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is configurable for plain-HTTP development; production requires it
		Name:     RefreshCookieName,
		Value:    ts.RefreshToken,
		Path:     a.cookiePath,
		Expires:  ts.RefreshExpiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *AuthServer) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is configurable for plain-HTTP development; production requires it
		Name:     RefreshCookieName,
		Path:     a.cookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func expiresIn(ts *auth.TokenSet) int {
	secs := int(time.Until(ts.AccessExpiresAt).Seconds())
	if secs < 1 {
		return 1
	}
	return secs
}

func (a *AuthServer) writeTokens(w http.ResponseWriter, status int, ts *auth.TokenSet) {
	a.setRefreshCookie(w, ts)
	noStore(w)
	if status == http.StatusCreated {
		writeJSONStatus(w, status, RegisterResponse{AccessToken: ts.AccessToken, ExpiresIn: expiresIn(ts), TokenType: RegisterResponseTokenTypeBearer})
		return
	}
	writeJSONStatus(w, status, TokenResponse{AccessToken: ts.AccessToken, ExpiresIn: expiresIn(ts), TokenType: TokenResponseTokenTypeBearer})
}

// principal returns the caller; the middleware guarantees it on every route that is not public.
func (a *AuthServer) principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
	}
	return p, ok
}

// ---------------------------------------------------------------- sign in and out

type registerBody struct {
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Password    string  `json:"password"`
	Bio         *string `json:"bio"`
}

// Register implements POST /auth/register.
func (a *AuthServer) Register(w http.ResponseWriter, r *http.Request, _ RegisterParams) {
	var body registerBody
	if !decode(w, r, &body) {
		return
	}
	ts, err := a.svc.Register(r.Context(), a.clientOf(r), body.Username, body.DisplayName, body.Password, body.Bio)
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	a.writeTokens(w, http.StatusCreated, ts)
}

type loginBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login implements POST /auth/login.
func (a *AuthServer) Login(w http.ResponseWriter, r *http.Request, _ LoginParams) {
	var body loginBody
	if !decode(w, r, &body) {
		return
	}
	if body.Username == "" || body.Password == "" {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest)
		return
	}
	res, err := a.svc.Login(r.Context(), a.clientOf(r), body.Username, body.Password)
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	if res.Tokens == nil {
		noStore(w)
		writeJSONStatus(w, http.StatusOK, TwoFactorChallenge{TwoFactorRequired: true, LoginChallenge: res.Challenge})
		return
	}
	a.writeTokens(w, http.StatusOK, res.Tokens)
}

type twoFactorLoginBody struct {
	LoginChallenge string `json:"login_challenge"`
	Code           string `json:"code"`
}

// LoginTwoFactor implements POST /auth/login/2fa.
func (a *AuthServer) LoginTwoFactor(w http.ResponseWriter, r *http.Request, _ LoginTwoFactorParams) {
	var body twoFactorLoginBody
	if !decode(w, r, &body) {
		return
	}
	if body.LoginChallenge == "" || body.Code == "" {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest)
		return
	}
	ts, err := a.svc.LoginTwoFactor(r.Context(), a.clientOf(r), body.LoginChallenge, body.Code)
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	a.writeTokens(w, http.StatusOK, ts)
}

// RefreshToken implements POST /auth/refresh.
func (a *AuthServer) RefreshToken(w http.ResponseWriter, r *http.Request, _ RefreshTokenParams) {
	cookie, err := r.Cookie(RefreshCookieName)
	if err != nil || cookie.Value == "" {
		WriteProblem(w, http.StatusUnauthorized, ErrorCodeUnauthenticated)
		return
	}
	ts, err := a.svc.Refresh(r.Context(), a.clientOf(r), cookie.Value)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidRefreshToken) || errors.Is(err, auth.ErrRevocationIncomplete) {
			a.clearRefreshCookie(w)
		}
		a.fail(w, r, err, false)
		return
	}
	a.writeTokens(w, http.StatusOK, ts)
}

// Logout implements POST /auth/logout: by access token, or by the refresh cookie when the access token
// is missing or expired.
func (a *AuthServer) Logout(w http.ResponseWriter, r *http.Request, _ LogoutParams) {
	client := a.clientOf(r)
	var err error
	switch p, ok := PrincipalFrom(r.Context()); {
	case ok:
		err = a.svc.Logout(r.Context(), client, p)
	default:
		cookie, cerr := r.Cookie(RefreshCookieName)
		if cerr != nil || cookie.Value == "" {
			writeUnauthorized(w, ErrorCodeUnauthenticated)
			return
		}
		err = a.svc.LogoutByRefreshToken(r.Context(), client, cookie.Value)
	}
	if err != nil {
		a.fail(w, r, err, false)
		return
	}
	a.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

type resetBody struct {
	RecoveryToken string `json:"recovery_token"`
	NewPassword   string `json:"new_password"`
}

// ResetPassword implements POST /auth/reset-password.
func (a *AuthServer) ResetPassword(w http.ResponseWriter, r *http.Request, _ ResetPasswordParams) {
	var body resetBody
	if !decode(w, r, &body) {
		return
	}
	if body.RecoveryToken == "" {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest)
		return
	}
	if err := a.svc.ResetPassword(r.Context(), a.clientOf(r), body.RecoveryToken, body.NewPassword); err != nil {
		a.fail(w, r, err, false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- the account

// GetMe implements GET /me.
func (a *AuthServer) GetMe(w http.ResponseWriter, r *http.Request, _ GetMeParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	u, err := a.svc.Me(r.Context(), p)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	id := strconv.FormatInt(u.ID, 10)
	me := Me{
		Id:          id,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Bio:         u.Bio,
		CreatedAt:   u.CreatedAt,
		IsOnline:    true,
	}
	if !u.LastSeenAt.IsZero() {
		seen := u.LastSeenAt
		me.LastSeen = &seen
	}
	if u.AvatarURL != nil {
		path := a.basePath + "/users/" + id + "/avatar"
		me.AvatarUrl = &path
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, me)
}

type passwordChangeBody struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword implements PUT /me/password.
func (a *AuthServer) ChangePassword(w http.ResponseWriter, r *http.Request, _ ChangePasswordParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body passwordChangeBody
	if !decode(w, r, &body) {
		return
	}
	if err := a.svc.ChangePassword(r.Context(), a.clientOf(r), p, body.CurrentPassword, body.NewPassword); err != nil {
		a.fail(w, r, err, true)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- sessions and security settings

// ListSessions implements GET /me/sessions.
func (a *AuthServer) ListSessions(w http.ResponseWriter, r *http.Request, _ ListSessionsParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	list, err := a.svc.Sessions(r.Context(), p)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	out := make([]Session, 0, len(list))
	for _, s := range list {
		exp := s.ExpiresAt
		out = append(out, Session{
			Id:         s.ID,
			Device:     s.UserAgent,
			CreatedAt:  s.CreatedAt,
			LastUsedAt: s.LastActiveAt,
			ExpiresAt:  &exp,
			IsCurrent:  s.Current,
		})
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, ListSessions200JSONResponse{Items: out})
}

// RevokeSession implements DELETE /me/sessions/{session_id}.
func (a *AuthServer) RevokeSession(w http.ResponseWriter, r *http.Request, sessionID SessionId, _ RevokeSessionParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if err := a.svc.RevokeSession(r.Context(), a.clientOf(r), p, sessionID); err != nil {
		a.fail(w, r, err, true)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RevokeOtherSessions implements DELETE /me/sessions.
func (a *AuthServer) RevokeOtherSessions(w http.ResponseWriter, r *http.Request, _ RevokeOtherSessionsParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if err := a.svc.RevokeOtherSessions(r.Context(), a.clientOf(r), p); err != nil {
		a.fail(w, r, err, true)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetSecuritySettings implements GET /me/security.
func (a *AuthServer) GetSecuritySettings(w http.ResponseWriter, r *http.Request, _ GetSecuritySettingsParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	s, err := a.svc.Security(r.Context(), p)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, SecuritySettings{SessionDurationDays: s.SessionDurationDays, TwoFactorEnabled: s.TwoFactorEnabled})
}

type securityBody struct {
	SessionDurationDays int `json:"session_duration_days"`
}

// UpdateSecuritySettings implements PATCH /me/security.
func (a *AuthServer) UpdateSecuritySettings(w http.ResponseWriter, r *http.Request, _ UpdateSecuritySettingsParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body securityBody
	if !decode(w, r, &body) {
		return
	}
	if err := a.svc.SetSessionDuration(r.Context(), a.clientOf(r), p, body.SessionDurationDays); err != nil {
		a.fail(w, r, err, true)
		return
	}
	s, err := a.svc.Security(r.Context(), p)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, SecuritySettings{SessionDurationDays: s.SessionDurationDays, TwoFactorEnabled: s.TwoFactorEnabled})
}

// ---------------------------------------------------------------- two-factor authentication

type passwordBody struct {
	Password string `json:"password"`
}

// SetupTwoFactor implements POST /me/2fa/setup.
func (a *AuthServer) SetupTwoFactor(w http.ResponseWriter, r *http.Request, _ SetupTwoFactorParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body passwordBody
	if !decode(w, r, &body) {
		return
	}
	setup, err := a.svc.SetupTwoFactor(r.Context(), p, body.Password)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, TwoFactorSetup{Secret: setup.Secret, OtpauthUri: setup.URI})
}

type codeBody struct {
	Code string `json:"code"`
}

// ConfirmTwoFactor implements POST /me/2fa/confirm. A wrong code is a 400: the session itself is fine.
func (a *AuthServer) ConfirmTwoFactor(w http.ResponseWriter, r *http.Request, _ ConfirmTwoFactorParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body codeBody
	if !decode(w, r, &body) {
		return
	}
	codes, err := a.svc.ConfirmTwoFactor(r.Context(), a.clientOf(r), p, body.Code)
	if errors.Is(err, auth.ErrInvalidTwoFactorCode) {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidTwoFactorCode)
		return
	}
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, TwoFactorRecoveryCodes{RecoveryCodes: codes})
}

type disableBody struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// DisableTwoFactor implements POST /me/2fa/disable.
func (a *AuthServer) DisableTwoFactor(w http.ResponseWriter, r *http.Request, _ DisableTwoFactorParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var body disableBody
	if !decode(w, r, &body) {
		return
	}
	if err := a.svc.DisableTwoFactor(r.Context(), a.clientOf(r), p, body.Password, body.Code); err != nil {
		a.fail(w, r, err, true)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CreateWebSocketTicket implements POST /auth/ws-ticket.
func (a *AuthServer) CreateWebSocketTicket(w http.ResponseWriter, r *http.Request, _ CreateWebSocketTicketParams) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	ticket, ttl, err := a.svc.IssueTicket(r.Context(), p)
	if err != nil {
		a.fail(w, r, err, true)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, WebSocketTicket{Ticket: ticket, ExpiresIn: int(ttl.Seconds())})
}
