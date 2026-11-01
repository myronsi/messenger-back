package notifications

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type closeBody struct {
	io.Reader
	closed bool
}

func (b *closeBody) Close() error { b.closed = true; return nil }
func TestWebPushEncryptsPayloadAndClosesProviderResponse(t *testing.T) {
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := NewSender(config.Notifications{VAPIDPublic: public, VAPIDPrivate: config.Secret(private), VAPIDSubject: "mailto:operator@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	body := &closeBody{Reader: strings.NewReader("")}
	called := false
	sender.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		if r.URL.Host != "fcm.googleapis.com" || r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") != "300" {
			t.Fatal("invalid Web Push request")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("private-preview")) || !strings.Contains(r.Header.Get("Authorization"), public) {
			t.Fatal("payload encryption or VAPID authentication missing")
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: body}, nil
	})
	d := postgres.NotificationDevice{Platform: "web", Destination: "https://fcm.googleapis.com/send/example", P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16))}
	if err := sender.Send(context.Background(), d, Payload{ChatID: "7", MessageID: "9", Body: "private-preview"}); err != nil {
		t.Fatal(err)
	}
	if !called || !body.closed {
		t.Fatal("provider response lifecycle is incomplete")
	}
}
