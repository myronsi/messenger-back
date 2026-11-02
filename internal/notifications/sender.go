// Package notifications delivers short-lived, session-bound message alerts.
package notifications

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type Payload struct {
	Title       string `json:"title"`
	Body        string `json:"body"`
	ChatID      string `json:"chat_id"`
	MessageID   string `json:"message_id"`
	UserID      string `json:"user_id"`
	Sound       bool   `json:"sound"`
	ExpiresAt   int64  `json:"expires_at"`
	AvatarToken string `json:"avatar_token,omitempty"`
	ReadToken   string `json:"read_token,omitempty"`
}

var ErrUnregistered = errors.New("notification destination expired")
var ErrUnavailable = errors.New("notification platform is not configured")

// ValidateDevice rejects arbitrary HTTP destinations; subscription URLs must belong to push providers.
func ValidateDevice(d postgres.NotificationDevice) bool {
	if len(d.Destination) == 0 || len(d.Destination) > 4096 {
		return false
	}
	switch d.Platform {
	case "web":
		u, err := url.Parse(d.Destination)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" {
			return false
		}
		host := strings.ToLower(u.Hostname())
		if host != "fcm.googleapis.com" && host != "updates.push.services.mozilla.com" && host != "web.push.apple.com" &&
			!strings.HasSuffix(host, ".notify.windows.com") {
			return false
		}
		p, e1 := base64.RawURLEncoding.DecodeString(d.P256dh)
		a, e2 := base64.RawURLEncoding.DecodeString(d.Auth)
		return e1 == nil && e2 == nil && len(p) == 65 && p[0] == 4 && len(a) == 16
	case "ios":
		b, err := hex.DecodeString(d.Destination)
		return err == nil && len(b) == 32
	case "android":
		for _, c := range d.Destination {
			valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_:.-", c)
			if !valid {
				return false
			}
		}
		return len(d.Destination) < 4096
	}
	return false
}

type Sender struct {
	cfg    config.Notifications
	client *http.Client
	native *nativeAuth
}

func NewSender(cfg config.Notifications) (*Sender, error) {
	n, err := newNativeAuth(cfg)
	if err != nil {
		return nil, err
	}
	return &Sender{cfg: cfg, native: n, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (s *Sender) Send(ctx context.Context, d postgres.NotificationDevice, p Payload) error {
	if !ValidateDevice(d) {
		return ErrUnregistered
	}
	raw, _ := json.Marshal(p)
	if d.Platform == "web" {
		if !s.cfg.WebEnabled() {
			return ErrUnavailable
		}
		resp, err := webpush.SendNotificationWithContext(ctx, raw, &webpush.Subscription{Endpoint: d.Destination, Keys: webpush.Keys{P256dh: d.P256dh, Auth: d.Auth}}, &webpush.Options{
			Subscriber: s.cfg.VAPIDSubject, VAPIDPublicKey: s.cfg.VAPIDPublic, VAPIDPrivateKey: s.cfg.VAPIDPrivate.Reveal(), TTL: 300, HTTPClient: s.client,
			Topic: "messenger-" + p.ChatID,
		})
		if err != nil {
			return errors.New("web push transport failed")
		}
		defer func() { _ = resp.Body.Close() }()
		return deliveryResponse(resp, "web")
	}
	var endpoint, token string
	var body any
	if d.Platform == "android" {
		if !s.cfg.AndroidEnabled() {
			return ErrUnavailable
		}
		var err error
		token, err = s.native.fcmToken(ctx, s.client)
		if err != nil {
			return err
		}
		endpoint = "https://fcm.googleapis.com/v1/projects/" + url.PathEscape(s.native.fcm.ProjectID) + "/messages:send"
		channel := "messages-silent"
		if p.Sound {
			channel = "messages"
		}
		body = map[string]any{"message": map[string]any{"token": d.Destination, "notification": map[string]string{"title": p.Title, "body": p.Body},
			"data":    map[string]string{"chat_id": p.ChatID, "message_id": p.MessageID, "user_id": p.UserID},
			"android": map[string]any{"ttl": "300s", "collapse_key": "chat-" + p.ChatID, "notification": map[string]string{"tag": "chat-" + p.ChatID, "channel_id": channel}}}}
	} else {
		if !s.cfg.IOSEnabled() {
			return ErrUnavailable
		}
		var err error
		token, err = s.native.apnsToken(s.cfg)
		if err != nil {
			return err
		}
		endpoint = "https://api.push.apple.com/3/device/" + d.Destination
		if s.cfg.APNsSandbox {
			endpoint = "https://api.sandbox.push.apple.com/3/device/" + d.Destination
		}
		aps := map[string]any{"alert": map[string]string{"title": p.Title, "body": p.Body}, "thread-id": "chat-" + p.ChatID}
		if p.Sound {
			aps["sound"] = "default"
		}
		body = map[string]any{"aps": aps, "chat_id": p.ChatID, "message_id": p.MessageID, "user_id": p.UserID}
	}
	raw, _ = json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if d.Platform == "ios" {
		req.Header.Set("apns-topic", s.cfg.APNsTopic)
		req.Header.Set("apns-push-type", "alert")
		req.Header.Set("apns-expiration", fmt.Sprint(time.Now().Add(5*time.Minute).Unix()))
		req.Header.Set("apns-collapse-id", "chat-"+p.ChatID)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return errors.New("native push transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	return deliveryResponse(resp, d.Platform)
}
func deliveryResponse(resp *http.Response, platform string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if platform == "web" && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone) {
		return ErrUnregistered
	}
	if platform == "ios" && (resp.StatusCode == http.StatusGone || bytes.Contains(body, []byte(`"BadDeviceToken"`))) {
		return ErrUnregistered
	}
	if platform == "android" && bytes.Contains(body, []byte(`"UNREGISTERED"`)) {
		return ErrUnregistered
	}
	// Provider bodies can contain tokens; only the status is safe for logs.
	return fmt.Errorf("%s push returned status %d", platform, resp.StatusCode)
}
