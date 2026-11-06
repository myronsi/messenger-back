package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/geoip/geoiptest"
)

func TestSessionsAndSecurityEventsExposeLocationNotIP(t *testing.T) {
	e := newAuthEnvWithGeoIP(t, 0, nil, geoiptest.Default(t))
	laptop := e.register(t, "geo_http")
	if rec := e.do(t, request{method: http.MethodPost, path: "/auth/login", body: map[string]any{"username": "geo_http", "password": password}, remote: "10.0.0.9:4000"}); rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}

	list := e.do(t, request{method: http.MethodGet, path: "/me/sessions", token: laptop.access})
	sessions, err := decodeSessions(list.Body.Bytes())
	if err != nil || list.Code != http.StatusOK || len(sessions) != 2 {
		t.Fatalf("sessions: %d %s", list.Code, list.Body)
	}
	for _, s := range sessions {
		loc, present := s["location"]
		if !present {
			t.Fatalf("location missing: %v", s)
		}
		if s["is_current"] == true {
			want := map[string]any{"city": "Berlin", "country_code": "DE", "country": "Germany"}
			if b, _ := json.Marshal(loc); string(b) != mustJSON(t, want) {
				t.Fatalf("current session location = %s", b)
			}
		} else if loc != nil {
			t.Fatalf("a private address has no location: %v", loc)
		}
	}
	assertNoAddress(t, list.Body.String())

	events := e.do(t, request{method: http.MethodGet, path: "/me/security-events", token: laptop.access})
	if events.Code != http.StatusOK {
		t.Fatalf("security events: %d %s", events.Code, events.Body)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(events.Body.Bytes(), &page); err != nil || len(page.Items) == 0 {
		t.Fatalf("security events: %s", events.Body)
	}
	seen := map[string]any{}
	for _, item := range page.Items {
		loc, present := item["location"]
		if !present {
			t.Fatalf("location missing: %v", item)
		}
		seen[item["type"].(string)] = loc
	}
	if reg, ok := seen["register"].(map[string]any); !ok || reg["city"] != "Berlin" || reg["country_code"] != "DE" {
		t.Fatalf("register event location = %v", seen["register"])
	}
	if seen["login"] != nil {
		t.Fatalf("login from a private address has no location: %v", seen["login"])
	}
	assertNoAddress(t, events.Body.String())
}

func TestSessionsWithoutGeoIPHaveNullLocation(t *testing.T) {
	e := newAuthEnv(t, 0, nil)
	s := e.register(t, "geo_off")
	list := e.do(t, request{method: http.MethodGet, path: "/me/sessions", token: s.access})
	sessions, err := decodeSessions(list.Body.Bytes())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %d %s", list.Code, list.Body)
	}
	if loc, present := sessions[0]["location"]; !present || loc != nil {
		t.Fatalf("want location null, got %v (present %v)", loc, present)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertNoAddress(t *testing.T, body string) {
	t.Helper()
	for _, leak := range []string{"203.0.113", "10.0.0.9", "ip_address", "ip_hint", `"ip"`} {
		if strings.Contains(body, leak) {
			t.Fatalf("response exposes %q: %s", leak, body)
		}
	}
}
