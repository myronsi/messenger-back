package connections

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrDenied        = errors.New("connections: the user declined at the provider")
	ErrFailed        = errors.New("connections: the provider did not confirm the account")
	ErrNotVerified   = errors.New("connections: the code was not found")
	ErrUnknown       = errors.New("connections: provider not enabled")
	ErrWrongFlow     = errors.New("connections: this provider uses another flow")
	ErrInvalidHandle = errors.New("connections: not a valid account or domain")
	ErrNoClaim       = errors.New("connections: no open claim")
)

const (
	maxBody        = 1 << 20
	maxNameLength  = 100
	requestTimeout = 10 * time.Second
)

type Account struct {
	ExternalID  string
	DisplayName string
	ProfileURL  *string
}

type Provider interface {
	Name() string
	AuthURL(state, challenge, redirect string) string
	Complete(ctx context.Context, query url.Values, verifier, redirect string) (Account, error)
}

type ProfileFunc func(ctx context.Context, c *http.Client, token string) (Account, error)

type OAuth struct {
	ProviderName string
	ClientID     string
	ClientSecret string
	AuthorizeURL string
	ExchangeURL  string
	Scopes       []string
	ExtraAuth    url.Values
	BasicAuth    bool
	Profile      ProfileFunc
	Client       *http.Client
}

func (o *OAuth) Name() string { return o.ProviderName }

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (o *OAuth) AuthURL(state, challenge, redirect string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", o.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	if len(o.Scopes) > 0 {
		q.Set("scope", strings.Join(o.Scopes, " "))
	}
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	for k, vs := range o.ExtraAuth {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return o.AuthorizeURL + "?" + q.Encode()
}

func (o *OAuth) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: requestTimeout}
}

func (o *OAuth) Complete(ctx context.Context, query url.Values, verifier, redirect string) (Account, error) {
	if e := query.Get("error"); e != "" {
		if e == "access_denied" {
			return Account{}, ErrDenied
		}
		return Account{}, fmt.Errorf("%w: %s", ErrFailed, e)
	}
	code := query.Get("code")
	if code == "" {
		return Account{}, fmt.Errorf("%w: no code", ErrFailed)
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("code_verifier", verifier)
	form.Set("client_id", o.ClientID)
	if !o.BasicAuth {
		form.Set("client_secret", o.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.ExchangeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Account{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if o.BasicAuth {
		req.SetBasicAuth(url.QueryEscape(o.ClientID), url.QueryEscape(o.ClientSecret))
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := doJSON(o.client(), req, &token); err != nil {
		return Account{}, fmt.Errorf("%w: token: %w", ErrFailed, err)
	}
	if token.AccessToken == "" {
		return Account{}, fmt.Errorf("%w: no access token", ErrFailed)
	}
	a, err := o.Profile(ctx, o.client(), token.AccessToken)
	if err != nil {
		return Account{}, fmt.Errorf("%w: profile: %w", ErrFailed, err)
	}
	return cleanAccount(a)
}

const userAgent = "messenger-connections/1.0"

func doJSON(c *http.Client, req *http.Request, dst any) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.Unmarshal(body, dst)
}

func getJSON(ctx context.Context, c *http.Client, u, token string, header http.Header, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", userAgent)
	}
	return doJSON(c, req, dst)
}

func httpsURL(raw string) *string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(raw) > 500 {
		return nil
	}
	s := u.String()
	return &s
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s))
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

func cleanAccount(a Account) (Account, error) {
	a.ExternalID = strings.TrimSpace(a.ExternalID)
	a.DisplayName = truncate(a.DisplayName, maxNameLength)
	if a.DisplayName == "" {
		a.DisplayName = truncate(a.ExternalID, maxNameLength)
	}
	if a.ExternalID == "" || len(a.ExternalID) > 300 || a.DisplayName == "" {
		return Account{}, fmt.Errorf("%w: incomplete account", ErrFailed)
	}
	if a.ProfileURL != nil {
		a.ProfileURL = httpsURL(*a.ProfileURL)
	}
	return a, nil
}
