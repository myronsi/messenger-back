package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/myronsi/messenger-back/internal/auth"
)

type DeviceProofVerifier interface {
	VerifyDeviceProof(ctx context.Context, raw, method, operation string) (auth.DeviceKey, error)
}

type deviceKeyKey struct{}

func deviceKeyFrom(ctx context.Context) *auth.DeviceKey {
	if k, ok := ctx.Value(deviceKeyKey{}).(auth.DeviceKey); ok {
		return &k
	}
	return nil
}

func verifyDeviceProof(v DeviceProofVerifier, basePath string, log *slog.Logger) MiddlewareFunc {
	authPrefix := basePath + "/auth/"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get(auth.DeviceProofHeader)
			if raw == "" || v == nil || r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, authPrefix) {
				next.ServeHTTP(w, r)
				return
			}
			key, err := v.VerifyDeviceProof(r.Context(), raw, r.Method, strings.TrimPrefix(r.URL.Path, basePath))
			switch {
			case err == nil:
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), deviceKeyKey{}, key)))
			case errors.Is(err, auth.ErrDeviceProofStale):
				WriteProblem(w, http.StatusUnauthorized, ErrorCodeDeviceProofStale)
			case errors.Is(err, auth.ErrInvalidDeviceProof):
				WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest)
			default:
				log.ErrorContext(r.Context(), "verify device proof", "err", err)
				WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
			}
		})
	}
}
