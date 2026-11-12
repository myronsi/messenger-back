package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func writeJSONCached(w http.ResponseWriter, r *http.Request, v any) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(v); err != nil {
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
		return
	}
	sum := sha256.Sum256(body.Bytes())
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("Cache-Control", "private, no-cache")
	h.Set("ETag", etag)
	h.Add("Vary", "Authorization")
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body.Bytes())
}

func profileVersionOf(ctx context.Context, users interface {
	Get(ctx context.Context, id int64) (postgres.User, error)
}, userID int64) int64 {
	u, err := users.Get(ctx, userID)
	if err != nil {
		return -1
	}
	return u.ProfileVersion
}
