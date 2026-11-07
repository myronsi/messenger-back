package calls

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testKey    = "APIdevkey"
	testSecret = "0123456789abcdef0123456789abcdef"
)

func parseClaims(t *testing.T, token, secret string) map[string]any {
	t.Helper()
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return []byte(secret), nil },
		jwt.WithValidMethods([]string{"HS256"})); err != nil {
		t.Fatalf("token: %v", err)
	}
	return claims
}

func TestParticipantGrantClaims(t *testing.T) {
	g := NewGrants(testKey, testSecret, 10*time.Minute)
	now := time.Now().Truncate(time.Second)
	token, exp, err := g.Participant("call-5", "7:5b0e8f0a-3f1c-4a52-9a8e-0d6e1c3f9b21", now)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("expiry %v", exp)
	}
	c := parseClaims(t, token, testSecret)
	if c["iss"] != testKey || c["sub"] != "7:5b0e8f0a-3f1c-4a52-9a8e-0d6e1c3f9b21" || c["jti"] == "" ||
		c["nbf"].(float64) != float64(now.Unix()) || c["exp"].(float64) != float64(exp.Unix()) {
		t.Fatalf("registered claims: %v", c)
	}
	video := c["video"].(map[string]any)
	want := map[string]any{
		"room": "call-5", "roomJoin": true, "canPublish": true, "canSubscribe": true, "canPublishData": false,
		"canUpdateOwnMetadata": false, "canPublishSources": []any{"microphone"},
	}
	if len(video) != len(want) {
		t.Fatalf("video grant has extra or missing claims: %v", video)
	}
	for k, v := range want {
		got, _ := json.Marshal(video[k])
		exp, _ := json.Marshal(v)
		if string(got) != string(exp) {
			t.Errorf("video.%s = %s, want %s", k, got, exp)
		}
	}
	other, _, _ := g.Participant("call-5", "7:x", now)
	if parseClaims(t, other, testSecret)["jti"] == c["jti"] {
		t.Fatal("token ids repeat")
	}
}

func webhookToken(t *testing.T, key, secret string, body []byte, exp time.Time) string {
	t.Helper()
	sum := sha256.Sum256(body)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": key, "exp": exp.Unix(), "nbf": time.Now().Add(-time.Minute).Unix(),
		"sha256": base64.StdEncoding.EncodeToString(sum[:]),
	}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestWebhookSignature(t *testing.T) {
	g := NewGrants(testKey, testSecret, time.Minute)
	body := []byte(`{"event":"room_finished","id":"EV_1","room":{"name":"call-1"}}`)
	good := webhookToken(t, testKey, testSecret, body, time.Now().Add(5*time.Minute))
	if err := g.VerifyWebhook(good, body); err != nil {
		t.Fatalf("valid webhook: %v", err)
	}
	if err := g.VerifyWebhook("Bearer "+good, body); err != nil {
		t.Fatalf("valid webhook with bearer prefix: %v", err)
	}
	cases := map[string]struct {
		header string
		body   []byte
	}{
		"tampered body":  {good, []byte(`{"event":"room_finished","id":"EV_1","room":{"name":"call-2"}}`)},
		"wrong secret":   {webhookToken(t, testKey, strings.Repeat("x", 32), body, time.Now().Add(time.Minute)), body},
		"wrong key":      {webhookToken(t, "other", testSecret, body, time.Now().Add(time.Minute)), body},
		"expired":        {webhookToken(t, testKey, testSecret, body, time.Now().Add(-time.Hour)), body},
		"missing header": {"", body},
		"garbage":        {"not-a-jwt", body},
	}
	for name, c := range cases {
		if err := g.VerifyWebhook(c.header, c.body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	none, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": testKey, "exp": time.Now().Add(time.Minute).Unix()}).SignedString([]byte(testSecret))
	if g.VerifyWebhook(none, body) == nil {
		t.Error("accepted a token without the body hash")
	}
}

func TestRoomClientSpeaksTwirp(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims := parseClaims(t, auth, testSecret)
		video := claims["video"].(map[string]any)
		if claims["iss"] != testKey || video["roomAdmin"] != true || video["roomCreate"] != true || video["room"] != "call-3" {
			t.Errorf("admin token: %v", claims)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Method != http.MethodPost {
			t.Errorf("request: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		var in map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &in)
		switch r.URL.Path {
		case "/twirp/livekit.RoomService/CreateRoom":
			if in["name"] != "call-3" || in["empty_timeout"].(float64) != 120 || in["departure_timeout"].(float64) != 20 || in["max_participants"].(float64) != 10 {
				t.Errorf("create room: %v", in)
			}
			_, _ = w.Write([]byte(`{"name":"call-3","turn_password":"secret"}`))
		case "/twirp/livekit.RoomService/ListParticipants":
			_, _ = w.Write([]byte(`{"participants":[{"identity":"1:a"},{"identity":"2:b"}]}`))
		case "/twirp/livekit.RoomService/RemoveParticipant", "/twirp/livekit.RoomService/DeleteRoom":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","msg":"room not found"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"internal","msg":"boom"}`))
		}
	}))
	defer srv.Close()
	c := NewRoomClient(srv.URL+"/", NewGrants(testKey, testSecret, time.Minute))
	ctx := context.Background()
	if err := c.CreateRoom(ctx, "call-3", 120*time.Second, 20*time.Second, 10); err != nil {
		t.Fatal(err)
	}
	ids, err := c.ListParticipants(ctx, "call-3")
	if err != nil || len(ids) != 2 || ids[1] != "2:b" {
		t.Fatalf("participants: %v %v", ids, err)
	}
	if err := c.RemoveParticipant(ctx, "call-3", "1:a"); err != nil {
		t.Fatalf("removing a participant that is gone: %v", err)
	}
	if err := c.DeleteRoom(ctx, "call-3"); err != nil {
		t.Fatalf("deleting a room that is gone: %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("calls: %v", calls)
	}
	bad := NewRoomClient(srv.URL, NewGrants(testKey, testSecret, time.Minute))
	if err := bad.call(ctx, "Nope", "call-3", map[string]string{}, nil); err == nil || errors.Is(err, ErrMediaNotFound) || strings.Contains(err.Error(), "boom") {
		t.Fatalf("server error: %v", err)
	}
}
