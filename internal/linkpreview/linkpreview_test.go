package linkpreview

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryCache struct {
	mu   sync.Mutex
	data map[string]string
}

func (c *memoryCache) Get(_ context.Context, key string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.data[key]
	return v, ok, nil
}

func (c *memoryCache) Set(_ context.Context, key, value string, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
	return nil
}

func anyAddr(netip.Addr) bool { return true }

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false, "::1": false, "fc00::1": false, "fe80::1": false, "::ffff:127.0.0.1": false,
		"fec0::1": false, "::7f00:1": false, "100::1": false, "2001:0:4136:e378::1": false, "192.88.99.1": false,
	} {
		if got := PublicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("PublicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestParseOpenGraphAndFallbacks(t *testing.T) {
	base, _ := url.Parse("https://example.com/news/1")
	p := parse(strings.NewReader(`<html><head>
		<title>Fallback title</title>
		<meta property="og:title" content="  Big   news ">
		<meta name="description" content="Plain description">
		<meta property="og:site_name" content="Example">
		<meta property="og:image" content="/img/cover.png#x">
		<meta property="og:image:width" content="1200"><meta property="og:image:height" content="630">
		</head><body><meta property="og:description" content="ignored after head"></body></html>`), base)
	if p.Title != "Big news" || p.Description != "Plain description" || p.SiteName != "Example" {
		t.Fatalf("texts: %+v", p)
	}
	if p.Image == nil || p.Image.URL != "https://example.com/img/cover.png" || p.Image.Width != 1200 || p.Image.Height != 630 {
		t.Fatalf("image: %+v", p.Image)
	}

	q := parse(strings.NewReader(`<head><title>Only title</title><meta name="twitter:image" content="javascript:alert(1)"></head>`), base)
	if q.Title != "Only title" || q.Image != nil {
		t.Fatalf("fallback: %+v", q)
	}
	if long := clip(strings.Repeat("a", 400), maxTitle); len([]rune(long)) != maxTitle {
		t.Fatalf("clip: %d", len([]rune(long)))
	}
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPreviewFetchesOnceAndProxiesSignedImages(t *testing.T) {
	var pageHits atomic.Int32
	picture := pngBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/article":
			pageHits.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<head><meta property="og:title" content="Article"><meta property="og:image" content="/cover.png"></head>`))
		case "/old":
			http.Redirect(w, r, "/article", http.StatusFound)
		case "/cover.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(picture)
		case "/evil.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
		case "/fake.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("<html>not a picture</html>"))
		case "/plain":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello"))
		}
	}))
	defer srv.Close()
	cache := &memoryCache{data: map[string]string{}}
	s := New(Options{Cache: cache, SignKey: []byte("test-key"), AllowAddr: anyAddr, AnyPort: true})
	ctx := context.Background()

	p, err := s.Preview(ctx, srv.URL+"/old#top")
	if err != nil || p.Title != "Article" || p.URL != srv.URL+"/article" || p.Image == nil || p.Image.URL != srv.URL+"/cover.png" {
		t.Fatalf("preview: %+v %v", p, err)
	}
	if _, err := s.Preview(ctx, srv.URL+"/old"); err != nil || pageHits.Load() != 1 {
		t.Fatalf("second preview should come from the cache: hits=%d err=%v", pageHits.Load(), err)
	}
	if _, err := s.Preview(ctx, srv.URL+"/plain"); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("non-html page: %v", err)
	}
	if _, err := s.Preview(ctx, "ftp://example.com/x"); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("ftp url: %v", err)
	}

	img := p.Image.URL
	body, contentType, err := s.Image(ctx, img, s.Sign(img))
	if err != nil || contentType != "image/png" || !bytes.Equal(body, picture) {
		t.Fatalf("image: %q %v", contentType, err)
	}
	if _, _, err := s.Image(ctx, img, "forged"); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("forged signature: %v", err)
	}
	for _, path := range []string{"/evil.svg", "/fake.png"} {
		if _, _, err := s.Image(ctx, srv.URL+path, s.Sign(srv.URL+path)); !errors.Is(err, ErrNoPreview) {
			t.Fatalf("%s must be refused: %v", path, err)
		}
	}
}

func TestDefaultPolicyRefusesInternalAddresses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<head><title>internal</title></head>`))
	}))
	defer srv.Close()
	s := New(Options{SignKey: []byte("k"), AnyPort: true})
	if _, err := s.Preview(context.Background(), srv.URL); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("loopback: %v", err)
	}
	if _, _, err := s.Image(context.Background(), srv.URL, s.Sign(srv.URL)); err == nil {
		t.Fatal("loopback image must be refused")
	}
	if hits.Load() != 0 {
		t.Fatalf("the internal server was reached %d times", hits.Load())
	}
	strict := New(Options{SignKey: []byte("k"), AllowAddr: anyAddr})
	if _, err := strict.Preview(context.Background(), srv.URL); !errors.Is(err, ErrNoPreview) || hits.Load() != 0 {
		t.Fatalf("ports other than 80 and 443 must be refused: %v hits=%d", err, hits.Load())
	}
}

func TestRedirectLimitLongImagesAndTemporaryFailures(t *testing.T) {
	var flaky atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/hop/"):
			n := len(strings.TrimPrefix(r.URL.Path, "/hop/"))
			if n == 0 {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(`<head><title>Arrived</title></head>`))
				return
			}
			http.Redirect(w, r, "/hop/"+strings.Repeat("x", n-1), http.StatusFound)
		case r.URL.Path == "/long-image":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<head><title>Long</title><meta property="og:image" content="/img/` + strings.Repeat("a", MaxURLLength) + `.png"></head>`))
		case r.URL.Path == "/flaky":
			if flaky.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<head><title>Back again</title></head>`))
		}
	}))
	defer srv.Close()
	cache := &memoryCache{data: map[string]string{}}
	s := New(Options{Cache: cache, SignKey: []byte("k"), AllowAddr: anyAddr, AnyPort: true})
	ctx := context.Background()

	if p, err := s.Preview(ctx, srv.URL+"/hop/xxx"); err != nil || p.Title != "Arrived" {
		t.Fatalf("three redirects must be followed: %+v %v", p, err)
	}
	if _, err := s.Preview(ctx, srv.URL+"/hop/xxxx"); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("four redirects must be refused: %v", err)
	}
	if p, err := s.Preview(ctx, srv.URL+"/long-image"); err != nil || p.Title != "Long" || p.Image != nil {
		t.Fatalf("an image address the proxy would refuse must be dropped: %+v %v", p, err)
	}
	if _, err := s.Preview(ctx, srv.URL+"/flaky"); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("first answer is a 503: %v", err)
	}
	if p, err := s.Preview(ctx, srv.URL+"/flaky"); err != nil || p.Title != "Back again" {
		t.Fatalf("a temporary failure must not be cached: %+v %v", p, err)
	}
}
