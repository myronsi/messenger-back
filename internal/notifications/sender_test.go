package notifications

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestSubscriptionRejectsArbitraryEndpoints(t *testing.T) {
	p := make([]byte, 65)
	p[0] = 4
	d := postgres.NotificationDevice{Platform: "web", P256dh: base64.RawURLEncoding.EncodeToString(p), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))}
	for _, endpoint := range []string{"http://fcm.googleapis.com/send", "https://127.0.0.1/x", "https://fcm.googleapis.com.evil.test/x", "https://fcm.googleapis.com:444/x", "https://user@fcm.googleapis.com/x"} {
		d.Destination = endpoint
		if ValidateDevice(d) {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	d.Destination = "https://fcm.googleapis.com/send"
	if !ValidateDevice(d) {
		t.Fatal("valid push provider rejected")
	}
	d.Auth = "bad"
	if ValidateDevice(d) {
		t.Fatal("invalid key accepted")
	}
}
func TestProviderErrorsDoNotLeakDestination(t *testing.T) {
	for _, tc := range []struct {
		platform string
		status   int
		body     string
		expired  bool
	}{
		{"web", 410, "secret-token", true}, {"web", 429, "secret-token", false}, {"android", 404, `{"error":{"details":[{"errorCode":"UNREGISTERED"}]}}`, true},
		{"android", 401, "secret-token", false}, {"ios", 400, `{"reason":"BadDeviceToken"}`, true}, {"ios", 403, "secret-token", false},
	} {
		err := deliveryResponse(&http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, tc.platform)
		if err == nil || errors.Is(err, ErrUnregistered) != tc.expired || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("unsafe classification: %v", err)
		}
	}
}
