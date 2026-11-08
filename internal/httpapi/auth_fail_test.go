package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/auth"
)

func TestFailKeepsSessionRoutesOffUnauthorized(t *testing.T) {
	a := &AuthServer{}
	for _, tc := range []struct {
		err         error
		withSession bool
		want        int
	}{
		{auth.ErrInvalidChallenge, true, http.StatusBadRequest},
		{auth.ErrInvalidChallenge, false, http.StatusUnauthorized},
		{auth.ErrRecoveryRequestNotFound, true, http.StatusNotFound},
		{auth.ErrApprovalSessionStale, true, http.StatusForbidden},
	} {
		w := httptest.NewRecorder()
		a.fail(w, httptest.NewRequest(http.MethodPost, "/", nil), tc.err, tc.withSession)
		if w.Code != tc.want {
			t.Errorf("%v with session %v: %d, want %d", tc.err, tc.withSession, w.Code, tc.want)
		}
	}
}

func TestFailNamesTheMissingRecoveryApprovalProof(t *testing.T) {
	a := &AuthServer{}
	for _, tc := range []struct {
		err  error
		code string
	}{
		{auth.ErrUserVerificationRequired, "user_verification_required"},
		{auth.ErrApprovalSessionTooNew, "session_too_new"},
	} {
		w := httptest.NewRecorder()
		a.fail(w, httptest.NewRequest(http.MethodPost, "/", nil), tc.err, true)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%v: %d %s, want 403 %s", tc.err, w.Code, w.Body.String(), tc.code)
		}
	}
}
