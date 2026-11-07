package calls

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	adminTokenTTL = time.Minute
	webhookLeeway = 30 * time.Second
)

var (
	ErrMediaNotFound    = errors.New("media room or participant not found")
	errWebhookSignature = errors.New("webhook signature invalid")
)

type Grants struct {
	key    string
	secret []byte
	ttl    time.Duration
}

func NewGrants(apiKey, apiSecret string, ttl time.Duration) *Grants {
	return &Grants{key: apiKey, secret: []byte(apiSecret), ttl: ttl}
}

type videoGrant struct {
	Room                 string   `json:"room,omitempty"`
	RoomJoin             bool     `json:"roomJoin,omitempty"`
	RoomAdmin            bool     `json:"roomAdmin,omitempty"`
	RoomCreate           bool     `json:"roomCreate,omitempty"`
	RoomList             bool     `json:"roomList,omitempty"`
	CanPublish           *bool    `json:"canPublish,omitempty"`
	CanSubscribe         *bool    `json:"canSubscribe,omitempty"`
	CanPublishData       *bool    `json:"canPublishData,omitempty"`
	CanPublishSources    []string `json:"canPublishSources,omitempty"`
	CanUpdateOwnMetadata *bool    `json:"canUpdateOwnMetadata,omitempty"`
}

type grantClaims struct {
	jwt.RegisteredClaims
	Video  *videoGrant `json:"video,omitempty"`
	Sha256 string      `json:"sha256,omitempty"`
}

func flag(v bool) *bool { return &v }

func randomID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (g *Grants) sign(c grantClaims) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(g.secret)
}

func (g *Grants) Participant(room, identity string, now time.Time) (string, time.Time, error) {
	exp := now.Add(g.ttl)
	token, err := g.sign(grantClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: g.key, Subject: identity, ID: randomID(),
			NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp),
		},
		Video: &videoGrant{
			Room: room, RoomJoin: true, CanPublish: flag(true), CanSubscribe: flag(true), CanPublishData: flag(false),
			CanPublishSources: []string{"microphone"}, CanUpdateOwnMetadata: flag(false),
		},
	})
	return token, exp, err
}

func (g *Grants) admin(room string) (string, error) {
	now := time.Now()
	return g.sign(grantClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: g.key, ID: randomID(), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(adminTokenTTL)),
		},
		Video: &videoGrant{Room: room, RoomAdmin: true, RoomCreate: true, RoomList: true},
	})
}

func (g *Grants) VerifyWebhook(authorization string, body []byte) error {
	raw := strings.TrimSpace(authorization)
	if len(raw) > 7 && strings.EqualFold(raw[:7], "bearer ") {
		raw = strings.TrimSpace(raw[7:])
	}
	if raw == "" {
		return errWebhookSignature
	}
	var c grantClaims
	_, err := jwt.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) { return g.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(g.key),
		jwt.WithExpirationRequired(), jwt.WithLeeway(webhookLeeway))
	if err != nil {
		return errWebhookSignature
	}
	sum := sha256.Sum256(body)
	want := base64.StdEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(c.Sha256), []byte(want)) != 1 {
		return errWebhookSignature
	}
	return nil
}

type RoomClient struct {
	base   string
	grants *Grants
	http   *http.Client
}

func NewRoomClient(apiURL string, grants *Grants) *RoomClient {
	return &RoomClient{
		base: strings.TrimRight(apiURL, "/"), grants: grants,
		http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (c *RoomClient) call(ctx context.Context, method, room string, in, out any) error {
	token, err := c.grants.admin(room)
	if err != nil {
		return err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/twirp/livekit.RoomService/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("media %s: transport failed", method)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil {
			return nil
		}
		return json.Unmarshal(raw, out)
	}
	var twirpErr struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &twirpErr)
	if twirpErr.Code == "not_found" || resp.StatusCode == http.StatusNotFound {
		return ErrMediaNotFound
	}
	return fmt.Errorf("media %s: status %d (%s)", method, resp.StatusCode, twirpErr.Code)
}

func (c *RoomClient) CreateRoom(ctx context.Context, room string, emptyTimeout, departureTimeout time.Duration, maxParticipants int) error {
	return c.call(ctx, "CreateRoom", room, map[string]any{
		"name": room, "empty_timeout": int(emptyTimeout / time.Second), "departure_timeout": int(departureTimeout / time.Second),
		"max_participants": maxParticipants,
	}, nil)
}

func (c *RoomClient) RemoveParticipant(ctx context.Context, room, identity string) error {
	err := c.call(ctx, "RemoveParticipant", room, map[string]string{"room": room, "identity": identity}, nil)
	if errors.Is(err, ErrMediaNotFound) {
		return nil
	}
	return err
}

func (c *RoomClient) DeleteRoom(ctx context.Context, room string) error {
	err := c.call(ctx, "DeleteRoom", room, map[string]string{"room": room}, nil)
	if errors.Is(err, ErrMediaNotFound) {
		return nil
	}
	return err
}

func (c *RoomClient) ListParticipants(ctx context.Context, room string) ([]string, error) {
	var out struct {
		Participants []struct {
			Identity string `json:"identity"`
		} `json:"participants"`
	}
	err := c.call(ctx, "ListParticipants", room, map[string]string{"room": room}, &out)
	if errors.Is(err, ErrMediaNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Participants))
	for _, p := range out.Participants {
		ids = append(ids, p.Identity)
	}
	return ids, nil
}
