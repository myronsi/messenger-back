package internalapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const BasePath = "/internal/v1"

type UsageSource interface {
	Usage(ctx context.Context, dayStart, dayEnd time.Time) (postgres.UsageFigures, error)
}

type Options struct {
	Token         string
	Version       string
	Log           *slog.Logger
	Moderation    Moderation
	Accounts      AccountDeleter
	AfterDeletion func(ctx context.Context, userID int64, deleted postgres.DeletedAccount)
	Events        EventLog
	PackImages    PackImages
	Usage         UsageSource
	Now           func() time.Time
	Messages      MessageRemover
}

func Handler(opts Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+BasePath+"/ping", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": "messenger-api", "version": opts.Version})
	})
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	actions{moderation: opts.Moderation, accounts: opts.Accounts, afterDeletion: opts.AfterDeletion, events: opts.Events, images: opts.PackImages, log: log}.register(mux)
	if opts.Usage != nil {
		mux.HandleFunc("GET "+BasePath+"/stats/usage", usage(opts))
	}
	if opts.Messages != nil {
		mux.HandleFunc("POST "+BasePath+"/chats/{chat_id}/messages/{message_id}/remove", removeMessage(opts.Messages, log))
	}
	return requireToken(opts.Token, opts.Log, mux)
}

func usage(opts Options) http.HandlerFunc {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return func(w http.ResponseWriter, r *http.Request) {
		day, err := time.Parse(time.DateOnly, r.URL.Query().Get("day"))
		current := now().UTC()
		if err != nil || day.After(current) {
			invalid(w, "day", "a date (YYYY-MM-DD), not in the future")
			return
		}
		end := day.AddDate(0, 0, 1)
		if end.After(current) {
			end = current
		}
		u, err := opts.Usage.Usage(r.Context(), day, end)
		if err != nil {
			if opts.Log != nil {
				opts.Log.ErrorContext(r.Context(), "internal api: usage figures", "error", err)
			}
			w.Header().Set("Retry-After", "2")
			writeJSON(w, http.StatusServiceUnavailable, problem{Error: "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"day": day.Format(time.DateOnly), "as_of": current,
			"active_day": u.ActiveDay, "active_30d": u.Active30d, "storage_bytes": u.StorageBytes,
		})
	}
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
