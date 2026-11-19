package internalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type usageSource struct {
	start, end time.Time
	fail       bool
}

func (u *usageSource) Usage(_ context.Context, start, end time.Time) (postgres.UsageFigures, error) {
	u.start, u.end = start, end
	if u.fail {
		return postgres.UsageFigures{}, errors.New("database away")
	}
	return postgres.UsageFigures{ActiveDay: 7, Active30d: 40, StorageBytes: 123456}, nil
}

func TestUsageFigures(t *testing.T) {
	src := &usageSource{}
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	h := Handler(Options{Token: token, Usage: src, Now: func() time.Time { return now }})

	if rec := call(t, h, http.MethodGet, BasePath+"/stats/usage?day=2026-11-18", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without the token: %d", rec.Code)
	}
	for _, q := range []string{"", "?day=yesterday", "?day=2026-11-20"} {
		rec := call(t, h, http.MethodGet, BasePath+"/stats/usage"+q, "Bearer "+token)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"field":"day"`) {
			t.Errorf("%q: %d %s", q, rec.Code, rec.Body)
		}
	}

	rec := call(t, h, http.MethodGet, BasePath+"/stats/usage?day=2026-11-18", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["day"] != "2026-11-18" || body["active_day"] != float64(7) || body["active_30d"] != float64(40) || body["storage_bytes"] != float64(123456) {
		t.Fatalf("body %v", body)
	}
	if !src.start.Equal(time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)) || !src.end.Equal(time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("asked for %v to %v", src.start, src.end)
	}

	call(t, h, http.MethodGet, BasePath+"/stats/usage?day=2026-11-19", "Bearer "+token)
	if !src.end.Equal(now) {
		t.Fatalf("today ends now, got %v", src.end)
	}

	src.fail = true
	rec = call(t, h, http.MethodGet, BasePath+"/stats/usage?day=2026-11-18", "Bearer "+token)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("store failure: %d %v", rec.Code, rec.Header())
	}
}

func TestUsageRouteOnlyWithASource(t *testing.T) {
	h := Handler(Options{Token: token})
	if rec := call(t, h, http.MethodGet, BasePath+"/stats/usage?day=2026-11-18", "Bearer "+token); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}
