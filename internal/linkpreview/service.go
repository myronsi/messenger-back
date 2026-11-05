package linkpreview

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
)

const (
	MaxURLLength   = 2048
	maxPageBytes   = 512 << 10
	MaxImageBytes  = 5 << 20
	previewTTL     = 24 * time.Hour
	missingTTL     = time.Hour
	cachePrefix    = "linkpreview:v1:"
	userAgent      = "MessengerLinkPreview/1.0 (+https://github.com/myronsi/messenger-back)"
	signatureBytes = 24
)

var (
	ErrInvalidURL   = errors.New("linkpreview: invalid url")
	ErrNoPreview    = errors.New("linkpreview: no preview")
	ErrBadSignature = errors.New("linkpreview: bad signature")
	errTemporary    = errors.New("linkpreview: temporary failure")
)

func temporaryStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

type Image struct {
	URL    string `json:"url"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

type Preview struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	Image       *Image `json:"image,omitempty"`
}

type Cache interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
}

type Options struct {
	Cache     Cache
	SignKey   []byte
	AllowAddr func(netip.Addr) bool
	AnyPort   bool
}

type Service struct {
	client *http.Client
	cache  Cache
	key    []byte
}

func New(o Options) *Service {
	allow := o.AllowAddr
	if allow == nil {
		allow = PublicAddr
	}
	return &Service{client: newClient(allow, o.AnyPort), cache: o.Cache, key: o.SignKey}
}

func Normalize(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxURLLength {
		return nil, ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, ErrInvalidURL
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	return u, nil
}

func (s *Service) Preview(ctx context.Context, raw string) (Preview, error) {
	u, err := Normalize(raw)
	if err != nil {
		return Preview{}, err
	}
	key := cacheKey(u.String())
	if s.cache != nil {
		if cached, ok, err := s.cache.Get(ctx, key); err == nil && ok {
			if cached == "" {
				return Preview{}, ErrNoPreview
			}
			var p Preview
			if json.Unmarshal([]byte(cached), &p) == nil {
				return p, nil
			}
		}
	}
	p, err := s.fetch(ctx, u)
	if s.cache != nil && !errors.Is(err, errTemporary) {
		value, ttl := "", missingTTL
		if err == nil {
			body, _ := json.Marshal(p)
			value, ttl = string(body), previewTTL
		}
		if ctx.Err() == nil {
			_ = s.cache.Set(context.WithoutCancel(ctx), key, value, ttl)
		}
	}
	if err != nil {
		return Preview{}, ErrNoPreview
	}
	return p, nil
}

func (s *Service) fetch(ctx context.Context, u *url.URL) (Preview, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Preview{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1")
	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlockedAddress) {
			return Preview{}, err
		}
		return Preview{}, fmt.Errorf("%w: %w", errTemporary, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if temporaryStatus(resp.StatusCode) {
		return Preview{}, fmt.Errorf("%w: status %d", errTemporary, resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if resp.StatusCode != http.StatusOK || (mediaType != "text/html" && mediaType != "application/xhtml+xml") {
		return Preview{}, ErrNoPreview
	}
	body, err := charset.NewReader(io.LimitReader(resp.Body, maxPageBytes), contentType)
	if err != nil {
		return Preview{}, err
	}
	p := parse(body, resp.Request.URL)
	if p.Title == "" && p.Image == nil {
		return Preview{}, ErrNoPreview
	}
	p.URL = resp.Request.URL.String()
	return p, nil
}

func (s *Service) Sign(imageURL string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(imageURL))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:signatureBytes])
}

func (s *Service) Image(ctx context.Context, imageURL, signature string) ([]byte, string, error) {
	if len(s.key) == 0 || !hmac.Equal([]byte(s.Sign(imageURL)), []byte(signature)) {
		return nil, "", ErrBadSignature
	}
	u, err := Normalize(imageURL)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "image/webp,image/png,image/jpeg,image/gif")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrNoPreview, err)
	}
	defer func() { _ = resp.Body.Close() }()
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || !imageTypes[mediaType] || resp.ContentLength > MaxImageBytes {
		return nil, "", ErrNoPreview
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrNoPreview, err)
	}
	if len(body) > MaxImageBytes {
		return nil, "", ErrNoPreview
	}
	if sniffed := http.DetectContentType(body); !imageTypes[sniffed] {
		return nil, "", ErrNoPreview
	}
	return body, mediaType, nil
}

func cacheKey(u string) string {
	sum := sha256.Sum256([]byte(u))
	return cachePrefix + hex.EncodeToString(sum[:])
}

func DeriveKey(secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("link-preview-image"))
	return mac.Sum(nil)
}
