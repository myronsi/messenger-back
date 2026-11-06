package realtime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/geoip"
)

func recoveryRequestEvent(t *testing.T, notice auth.RecoveryRequestNotice) []byte {
	t.Helper()
	data, err := json.Marshal(notice)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"type": "recovery_request", "event_id": "1", "chat_id": nil, "data": json.RawMessage(data)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRecoveryRequestEventCarriesLocation(t *testing.T) {
	city, code, country := "Berlin", "DE", "Germany"
	notice := auth.RecoveryRequestNotice{
		ID: "z8uFZt0xFYtjm_iO3vI9xFYxsy7xM4K5uM1g1s8YbYI", UserAgent: "Chrome on Windows", Choices: []int{17, 42, 85},
		ExpiresAt: time.Date(2026, 11, 5, 20, 0, 0, 0, time.UTC),
	}
	for name, loc := range map[string]*geoip.Location{
		"known":   {City: &city, CountryCode: &code, Country: &country},
		"partial": {CountryCode: &code, Country: &country},
		"unknown": nil,
	} {
		t.Run(name, func(t *testing.T) {
			notice.Location = loc
			if err := eventViolation(recoveryRequestEvent(t, notice)); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, bad := range []string{
		`{"type":"recovery_request","event_id":"1","chat_id":null,"data":{"request_id":"z8uFZt0xFYtjm_iO3vI9xFYxsy7xM4K5uM1g1s8YbYI","user_agent":"x","ip_hint":"192.0.2.0/24","choices":[17,42,85],"expires_at":"2026-11-05T20:00:00Z"}}`,
		`{"type":"recovery_request","event_id":"1","chat_id":null,"data":{"request_id":"z8uFZt0xFYtjm_iO3vI9xFYxsy7xM4K5uM1g1s8YbYI","user_agent":"x","location":{"city":"Berlin","country_code":"Germany","country":"Germany"},"choices":[17,42,85],"expires_at":"2026-11-05T20:00:00Z"}}`,
	} {
		if eventViolation([]byte(bad)) == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
