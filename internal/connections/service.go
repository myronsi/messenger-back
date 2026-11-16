package connections

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	StateTTL   = 10 * time.Minute
	ClaimTTL   = time.Hour
	CodePrefix = "messenger-verify="
)

var Order = []string{"github", "spotify", "steam", "xbox", "discord", "twitch", "youtube", "reddit", "roblox", "tiktok", "bluesky", "mastodon", "website"}

const (
	ResultConnected = "connected"
	ResultDenied    = "denied"
	ResultFailed    = "failed"
	ResultLimit     = "limit"
)

type State interface {
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Take(ctx context.Context, key string) ([]byte, bool, error)
	Delete(ctx context.Context, key string) error
}

type Options struct {
	Providers   []Provider
	Verifiers   []Verifier
	Store       postgres.ConnectionRepository
	State       State
	CallbackURL string
	WebURL      string
	Now         func() time.Time
}

type Service struct {
	o         Options
	providers map[string]Provider
	verifiers map[string]Verifier
}

func NewService(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Service{o: o, providers: map[string]Provider{}, verifiers: map[string]Verifier{}}
	if o.CallbackURL != "" && o.WebURL != "" {
		for _, p := range o.Providers {
			s.providers[p.Name()] = p
		}
	}
	for _, v := range o.Verifiers {
		s.verifiers[v.Name()] = v
	}
	return s
}

func (s *Service) Enabled() []string {
	out := []string{}
	for _, name := range Order {
		if s.providers[name] != nil || s.verifiers[name] != nil {
			out = append(out, name)
		}
	}
	return out
}

type pending struct {
	UserID   int64  `json:"u"`
	Provider string `json:"p"`
	Verifier string `json:"v"`
}

type claim struct {
	Identifier string `json:"i"`
	Code       string `json:"c"`
}

type Challenge struct {
	Identifier string
	Code       string
	ExpiresAt  time.Time
}

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func newCode() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return CodePrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

func (s *Service) redirect(provider string) string {
	return strings.TrimSuffix(s.o.CallbackURL, "/") + "/connections/callback/" + provider
}

func (s *Service) full(ctx context.Context, userID int64) error {
	list, err := s.o.Store.List(ctx, userID)
	if err != nil {
		return err
	}
	if len(list) >= postgres.MaxConnections {
		return postgres.ErrConnectionLimit
	}
	return nil
}

func (s *Service) Start(ctx context.Context, userID int64, provider string) (string, error) {
	p, ok := s.providers[provider]
	if !ok {
		if s.verifiers[provider] != nil {
			return "", ErrWrongFlow
		}
		return "", ErrUnknown
	}
	if err := s.full(ctx, userID); err != nil {
		return "", err
	}
	state, verifier := random(32), random(48)
	raw, _ := json.Marshal(pending{UserID: userID, Provider: provider, Verifier: verifier})
	if err := s.o.State.Put(ctx, "conn:state:"+state, raw, StateTTL); err != nil {
		return "", err
	}
	return p.AuthURL(state, pkceChallenge(verifier), s.redirect(provider)), nil
}

func (s *Service) back(provider, result string) string {
	q := url.Values{}
	q.Set("provider", provider)
	q.Set("result", result)
	return strings.TrimSuffix(s.o.WebURL, "/") + "/settings/connections?" + q.Encode()
}

func (s *Service) Callback(ctx context.Context, provider string, query url.Values) string {
	p, ok := s.providers[provider]
	state := query.Get("state")
	if !ok || state == "" {
		return s.back(provider, ResultFailed)
	}
	raw, found, err := s.o.State.Take(ctx, "conn:state:"+state)
	var pend pending
	if err != nil || !found || json.Unmarshal(raw, &pend) != nil || pend.Provider != provider {
		return s.back(provider, ResultFailed)
	}
	account, err := p.Complete(ctx, query, pend.Verifier, s.redirect(provider))
	switch {
	case errors.Is(err, ErrDenied):
		return s.back(provider, ResultDenied)
	case err != nil:
		return s.back(provider, ResultFailed)
	}
	if _, err := s.connect(ctx, pend.UserID, provider, account); err != nil {
		if errors.Is(err, postgres.ErrConnectionLimit) {
			return s.back(provider, ResultLimit)
		}
		return s.back(provider, ResultFailed)
	}
	return s.back(provider, ResultConnected)
}

func (s *Service) connect(ctx context.Context, userID int64, provider string, a Account) (postgres.Connection, error) {
	return s.o.Store.Connect(ctx, userID, postgres.ConnectedAccount{
		Provider: provider, ExternalID: a.ExternalID, DisplayName: a.DisplayName, ProfileURL: a.ProfileURL,
	})
}

func claimKey(userID int64, provider string) string {
	return "conn:claim:" + strconv.FormatInt(userID, 10) + ":" + provider
}

func (s *Service) verifier(provider string) (Verifier, error) {
	v, ok := s.verifiers[provider]
	if ok {
		return v, nil
	}
	if s.providers[provider] != nil {
		return nil, ErrWrongFlow
	}
	return nil, ErrUnknown
}

func (s *Service) Claim(ctx context.Context, userID int64, provider, input string) (Challenge, error) {
	v, err := s.verifier(provider)
	if err != nil {
		return Challenge{}, err
	}
	identifier, err := v.Normalize(input)
	if err != nil {
		return Challenge{}, err
	}
	if err := s.full(ctx, userID); err != nil {
		return Challenge{}, err
	}
	c := claim{Identifier: identifier, Code: newCode()}
	raw, _ := json.Marshal(c)
	if err := s.o.State.Put(ctx, claimKey(userID, provider), raw, ClaimTTL); err != nil {
		return Challenge{}, err
	}
	return Challenge{Identifier: identifier, Code: c.Code, ExpiresAt: s.o.Now().Add(ClaimTTL)}, nil
}

func (s *Service) Verify(ctx context.Context, userID int64, provider string) (postgres.Connection, error) {
	v, err := s.verifier(provider)
	if err != nil {
		return postgres.Connection{}, err
	}
	raw, found, err := s.o.State.Get(ctx, claimKey(userID, provider))
	if err != nil {
		return postgres.Connection{}, err
	}
	var c claim
	if !found || json.Unmarshal(raw, &c) != nil {
		return postgres.Connection{}, ErrNoClaim
	}
	account, err := v.Check(ctx, c.Identifier, c.Code)
	if err != nil {
		return postgres.Connection{}, err
	}
	conn, err := s.connect(ctx, userID, provider, account)
	if err != nil {
		return postgres.Connection{}, err
	}
	_ = s.o.State.Delete(ctx, claimKey(userID, provider))
	return conn, nil
}

func (s *Service) List(ctx context.Context, userID int64) ([]postgres.Connection, error) {
	return s.o.Store.List(ctx, userID)
}

func (s *Service) Delete(ctx context.Context, userID, id int64) error {
	return s.o.Store.Delete(ctx, userID, id)
}

func (s *Service) Reorder(ctx context.Context, userID int64, ids []int64) ([]postgres.Connection, error) {
	return s.o.Store.Reorder(ctx, userID, ids)
}
