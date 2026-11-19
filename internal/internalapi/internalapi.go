package internalapi

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

const BasePath = "/internal/v1"

type Options struct {
	Token   string
	Version string
	Log     *slog.Logger
}

func Handler(opts Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+BasePath+"/ping", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": "messenger-api", "version": opts.Version})
	})
	return requireToken(opts.Token, opts.Log, mux)
}

func requireToken(token string, log *slog.Logger, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(want) == 0 || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			if log != nil {
				log.WarnContext(r.Context(), "internal api: rejected request", "method", r.Method, "route", r.URL.Path)
			}
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
