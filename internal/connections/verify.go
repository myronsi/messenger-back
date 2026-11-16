package connections

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const maxPage = 512 << 10

var (
	domainPattern  = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
	userPattern    = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	metaTagPattern = regexp.MustCompile(`(?is)<meta\s[^>]*name\s*=\s*["']messenger-verification["'][^>]*>`)
	contentPattern = regexp.MustCompile(`(?is)content\s*=\s*["']([^"']*)["']`)
)

type Verifier interface {
	Name() string
	Normalize(input string) (string, error)
	Check(ctx context.Context, identifier, code string) (Account, error)
}

func validDomain(host string) bool {
	return len(host) <= 253 && domainPattern.MatchString(host) && net.ParseIP(host) == nil
}

type Bluesky struct {
	AppViewURL string
	Client     *http.Client
}

func NewBluesky(client *http.Client) *Bluesky {
	return &Bluesky{AppViewURL: "https://public.api.bsky.app", Client: client}
}

func (b *Bluesky) Name() string { return "bluesky" }

func (b *Bluesky) Normalize(input string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(input))
	h = strings.TrimPrefix(h, "https://bsky.app/profile/")
	h = strings.TrimSuffix(strings.TrimPrefix(h, "@"), "/")
	if !validDomain(h) {
		return "", ErrInvalidHandle
	}
	return h, nil
}

func (b *Bluesky) Check(ctx context.Context, handle, code string) (Account, error) {
	var p struct {
		DID         string `json:"did"`
		Handle      string `json:"handle"`
		Description string `json:"description"`
	}
	u := b.AppViewURL + "/xrpc/app.bsky.actor.getProfile?actor=" + url.QueryEscape(handle)
	if err := getJSON(ctx, b.Client, u, "", nil, &p); err != nil {
		return Account{}, fmt.Errorf("%w: %w", ErrNotVerified, err)
	}
	if !strings.Contains(p.Description, code) || !strings.EqualFold(p.Handle, handle) {
		return Account{}, ErrNotVerified
	}
	return cleanAccount(Account{ExternalID: p.DID, DisplayName: "@" + handle, ProfileURL: str("https://bsky.app/profile/" + handle)})
}

type Mastodon struct {
	Client *http.Client
	Scheme string
}

func NewMastodon(client *http.Client) *Mastodon { return &Mastodon{Client: client, Scheme: "https"} }

func (m *Mastodon) Name() string { return "mastodon" }

func (m *Mastodon) Normalize(input string) (string, error) {
	s := strings.TrimSpace(input)
	var user, host string
	if u, err := url.Parse(s); err == nil && u.Scheme == "https" && u.Host != "" {
		host = u.Host
		user = strings.TrimPrefix(strings.Trim(u.Path, "/"), "@")
	} else {
		parts := strings.Split(strings.TrimPrefix(s, "@"), "@")
		if len(parts) != 2 {
			return "", ErrInvalidHandle
		}
		user, host = parts[0], parts[1]
	}
	host = strings.ToLower(host)
	if !userPattern.MatchString(user) || !validDomain(host) {
		return "", ErrInvalidHandle
	}
	return user + "@" + host, nil
}

func (m *Mastodon) Check(ctx context.Context, acct, code string) (Account, error) {
	user, host, _ := strings.Cut(acct, "@")
	var a struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Note   string `json:"note"`
		Fields []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"fields"`
	}
	u := m.Scheme + "://" + host + "/api/v1/accounts/lookup?acct=" + url.QueryEscape(user)
	if err := getJSON(ctx, m.Client, u, "", nil, &a); err != nil {
		return Account{}, fmt.Errorf("%w: %w", ErrNotVerified, err)
	}
	found := strings.Contains(html.UnescapeString(a.Note), code)
	for _, f := range a.Fields {
		found = found || strings.Contains(html.UnescapeString(f.Value), code) || strings.Contains(f.Name, code)
	}
	if !found {
		return Account{}, ErrNotVerified
	}
	return cleanAccount(Account{ExternalID: acct, DisplayName: "@" + acct, ProfileURL: str(a.URL)})
}

type TXTResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

type Website struct {
	Client   *http.Client
	Resolver TXTResolver
	Scheme   string
}

func NewWebsite(client *http.Client) *Website {
	return &Website{Client: client, Resolver: net.DefaultResolver, Scheme: "https"}
}

func (w *Website) Name() string { return "website" }

func (w *Website) Normalize(input string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(input))
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Port() != "" || u.User != nil {
		return "", ErrInvalidHandle
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	if !validDomain(host) {
		return "", ErrInvalidHandle
	}
	return host, nil
}

func (w *Website) Check(ctx context.Context, host, code string) (Account, error) {
	account := Account{ExternalID: host, DisplayName: host, ProfileURL: str("https://" + host)}
	if records, err := w.Resolver.LookupTXT(ctx, host); err == nil {
		for _, r := range records {
			if strings.Contains(r, code) {
				return cleanAccount(account)
			}
		}
	}
	if w.pageHasCode(ctx, host, code) {
		return cleanAccount(account)
	}
	return Account{}, ErrNotVerified
}

func (w *Website) pageHasCode(ctx context.Context, host, code string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.Scheme+"://"+host+"/", nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := w.Client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
	if err != nil {
		return false
	}
	_, want, _ := strings.Cut(code, "=")
	for _, tag := range metaTagPattern.FindAllString(string(body), -1) {
		if m := contentPattern.FindStringSubmatch(tag); m != nil {
			if v := html.UnescapeString(m[1]); v == code || v == want {
				return true
			}
		}
	}
	return false
}
