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
	Preview     bool   `json:"preview"`
	SenderID    string `json:"sender_id,omitempty"`
	AvatarToken string `json:"avatar_token,omitempty"`
	ReadToken   string `json:"read_token,omitempty"`
	ImageToken  string `json:"image_token,omitempty"`
	Kind        string `json:"kind,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	ChatType    string `json:"chat_type,omitempty"`
	ChatTitle   string `json:"chat_title,omitempty"`
}

var ErrUnregistered = errors.New("notification destination expired")
var ErrUnavailable = errors.New("notification platform is not configured")

var ErrRejected = errors.New("notification destination rejected")

var ErrTooLarge = errors.New("notification payload rejected")

const (
	defaultTTL = 5 * time.Minute
	minTTL     = 30 * time.Second
	maxTTL     = 24 * time.Hour
)

func (p Payload) ttl(now time.Time) int {
	if p.ExpiresAt == 0 {
		return int(defaultTTL / time.Second)
	}
	left := time.Unix(p.ExpiresAt, 0).Sub(now)
	return int(min(max(left, minTTL), maxTTL) / time.Second)
}

func (p Payload) topic() string {
	if p.Kind == BirthdayKind {
		return "birthday-" + p.ChatID
	}
	return "messenger-" + p.ChatID
}

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
			Subscriber: s.cfg.VAPIDSubject, VAPIDPublicKey: s.cfg.VAPIDPublic, VAPIDPrivateKey: s.cfg.VAPIDPrivate.Reveal(), TTL: p.ttl(time.Now()), HTTPClient: s.client,
			Topic: p.topic(),
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
			"android": map[string]any{"ttl": fmt.Sprintf("%ds", p.ttl(time.Now())), "collapse_key": "chat-" + p.ChatID, "notification": map[string]string{"tag": "chat-" + p.ChatID, "channel_id": channel}}}}
	} else {
		if !s.cfg.IOSEnabled() {
			return ErrUnavailable
		}
		var err error
		token, err = s.native.apnsToken(s.cfg, time.Now())
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
		req.Header.Set("apns-expiration", fmt.Sprint(time.Now().Add(time.Duration(p.ttl(time.Now()))*time.Second).Unix()))
		req.Header.Set("apns-collapse-id", "chat-"+p.ChatID)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return errors.New("native push transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	err = deliveryResponse(resp, d.Platform)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden && d.Platform == "ios" {
		s.native.forget(d.Platform)
	}
	return err
}

type CallPayload struct {
	Type        string `json:"type"`
	Action      string `json:"action"`
	CallID      string `json:"call_id"`
	CallChatID  string `json:"call_chat_id"`
	UserID      string `json:"user_id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Preview     bool   `json:"preview"`
	SenderID    string `json:"sender_id,omitempty"`
	AvatarToken string `json:"avatar_token,omitempty"`
	ExpiresAt   int64  `json:"expires_at"`
	Reason      string `json:"reason,omitempty"`
}

func (s *Sender) SendCall(ctx context.Context, d postgres.NotificationDevice, p CallPayload, ttl time.Duration) error {
	if d.Platform != "web" || !ValidateDevice(d) {
		return ErrUnregistered
	}
	if !s.cfg.WebEnabled() {
		return ErrUnavailable
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	resp, err := webpush.SendNotificationWithContext(ctx, raw, &webpush.Subscription{Endpoint: d.Destination, Keys: webpush.Keys{P256dh: d.P256dh, Auth: d.Auth}}, &webpush.Options{
		Subscriber: s.cfg.VAPIDSubject, VAPIDPublicKey: s.cfg.VAPIDPublic, VAPIDPrivateKey: s.cfg.VAPIDPrivate.Reveal(),
		TTL: max(int(ttl/time.Second), 1), HTTPClient: s.client, Urgency: webpush.UrgencyHigh, Topic: "call-" + p.CallID,
	})
	if err != nil {
		return errors.New("web push transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	return deliveryResponse(resp, "web")
}

func deliveryResponse(resp *http.Response, platform string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	reason := func(names ...string) bool {
		for _, n := range names {
			if bytes.Contains(body, []byte(`"`+n+`"`)) {
				return true
			}
		}
		return false
	}
	var kind error
	switch platform {
	case "web":
		switch resp.StatusCode {
		case http.StatusNotFound, http.StatusGone:
			return ErrUnregistered
		case http.StatusBadRequest, http.StatusForbidden:
			kind = ErrRejected
		case http.StatusRequestEntityTooLarge:
			kind = ErrTooLarge
		}
	case "ios":
		switch {
		case resp.StatusCode == http.StatusGone || reason("BadDeviceToken", "Unregistered"):
			return ErrUnregistered
		case reason("DeviceTokenNotForTopic"):
			kind = ErrRejected
		case resp.StatusCode == http.StatusRequestEntityTooLarge || reason("PayloadTooLarge"):
			kind = ErrTooLarge
		}
	case "android":
		switch {
		case reason("UNREGISTERED"):
			return ErrUnregistered
		case reason("SENDER_ID_MISMATCH"):
			kind = ErrRejected
		}
	}
	// Provider bodies can contain tokens; only the status is safe for logs.
	if kind != nil {
		return fmt.Errorf("%w: %s push returned status %d", kind, platform, resp.StatusCode)
	}
	return fmt.Errorf("%s push returned status %d", platform, resp.StatusCode)
}
