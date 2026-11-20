package notifications

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/config"
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

func TestPermanentProviderErrorsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		platform string
		status   int
		body     string
		want     error
	}{
		{"web", 403, "", ErrRejected}, {"web", 400, "", ErrRejected}, {"web", 413, "", ErrTooLarge},
		{"web", 500, "", nil}, {"web", 429, "", nil},
		{"android", 403, `{"error":{"details":[{"errorCode":"SENDER_ID_MISMATCH"}]}}`, ErrRejected},
		{"android", 500, "", nil},
		{"ios", 400, `{"reason":"DeviceTokenNotForTopic"}`, ErrRejected}, {"ios", 413, `{"reason":"PayloadTooLarge"}`, ErrTooLarge},
		{"ios", 403, `{"reason":"InvalidProviderToken"}`, nil},
	} {
		err := deliveryResponse(&http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, tc.platform)
		permanent := errors.Is(err, ErrRejected) || errors.Is(err, ErrTooLarge)
		if err == nil || tc.want == nil && permanent || tc.want != nil && !errors.Is(err, tc.want) {
			t.Fatalf("%s %d: %v", tc.platform, tc.status, err)
		}
	}
}

func TestPushLifetimeFollowsThePayload(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	for _, tc := range []struct {
		expires int64
		want    int
	}{{0, 300}, {now.Add(15 * time.Minute).Unix(), 900}, {now.Add(12 * time.Hour).Unix(), 43200}, {now.Unix() - 5, 30}, {now.Add(72 * time.Hour).Unix(), 86400}} {
		if got := (Payload{ExpiresAt: tc.expires}).ttl(now); got != tc.want {
			t.Fatalf("expires %d: ttl %d want %d", tc.expires, got, tc.want)
		}
	}
	if (Payload{ChatID: "5", Kind: BirthdayKind}).topic() == (Payload{ChatID: "5"}).topic() {
		t.Fatal("a birthday must not replace a pending message push")
	}
}

func TestAPNsTokenIsReusedWithinItsWindow(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	n := &nativeAuth{ec: key}
	cfg := config.Notifications{APNsTeamID: "team", APNsKeyID: "key"}
	start := time.Unix(1_000_000, 0)
	first, err := n.apnsToken(cfg, start)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := n.apnsToken(cfg, start.Add(39*time.Minute)); again != first {
		t.Fatal("token renewed too early; Apple answers TooManyProviderTokenUpdates")
	}
	if later, _ := n.apnsToken(cfg, start.Add(41*time.Minute)); later == first {
		t.Fatal("token kept too long")
	}
	n.forget("ios")
	if n.apns != "" {
		t.Fatal("a rejected token must be dropped")
	}
}
