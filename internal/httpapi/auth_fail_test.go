package httpapi

import (
	"net/http"
	"net/http/httptest"
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
