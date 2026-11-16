package connections

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testCode = "messenger-verify=abcdefghijklmnop"

func TestNormalize(t *testing.T) {
	cases := []struct {
		v       Verifier
		in, out string
	}{
		{&Bluesky{}, "@Alice.bsky.social", "alice.bsky.social"},
		{&Bluesky{}, "https://bsky.app/profile/alice.example.com", "alice.example.com"},
		{&Bluesky{}, "not a handle", ""},
		{&Mastodon{}, "@alice@Mastodon.social", "alice@mastodon.social"},
		{&Mastodon{}, "https://fosstodon.org/@bob", "bob@fosstodon.org"},
		{&Mastodon{}, "bob@127.0.0.1", ""},
		{&Mastodon{}, "alice", ""},
		{&Website{}, "Example.COM", "example.com"},
		{&Website{}, "https://blog.example.org/about?x=1", "blog.example.org"},
		{&Website{}, "http://10.0.0.1/", ""},
		{&Website{}, "https://example.com:8443", ""},
		{&Website{}, "localhost", ""},
	}
	for _, c := range cases {
		got, err := c.v.Normalize(c.in)
		if c.out == "" {
			if !errors.Is(err, ErrInvalidHandle) {
				t.Errorf("%s %q: want invalid, got %q", c.v.Name(), c.in, got)
			}
			continue
		}
		if err != nil || got != c.out {
			t.Errorf("%s %q: %q %v", c.v.Name(), c.in, got, err)
		}
	}
}

func TestBlueskyCheck(t *testing.T) {
	description := "hi " + testCode
	srv := jsonServer(t, map[string]func(*http.Request) any{
		"/xrpc/app.bsky.actor.getProfile": func(r *http.Request) any {
			return map[string]string{"did": "did:plc:abc", "handle": r.URL.Query().Get("actor"), "description": description}
		},
	})
	b := &Bluesky{AppViewURL: srv.URL, Client: srv.Client()}
	a, err := b.Check(context.Background(), "alice.bsky.social", testCode)
	if err != nil || a.ExternalID != "did:plc:abc" || a.DisplayName != "@alice.bsky.social" || *a.ProfileURL != "https://bsky.app/profile/alice.bsky.social" {
		t.Fatalf("%+v %v", a, err)
	}
	description = "no code"
	if _, err := b.Check(context.Background(), "alice.bsky.social", testCode); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("missing code: %v", err)
	}
}

func TestMastodonCheck(t *testing.T) {
	note, field := "<p>hello</p>", ""
	srv := jsonServer(t, map[string]func(*http.Request) any{
		"/api/v1/accounts/lookup": func(r *http.Request) any {
			if r.URL.Query().Get("acct") != "alice" {
				return nil
			}
			return map[string]any{"id": "1", "url": "https://mastodon.example/@alice", "note": note,
				"fields": []map[string]string{{"name": "Messenger", "value": field}}}
		},
	})
	m := &Mastodon{Client: srv.Client(), Scheme: "http"}
	acct := "alice@" + strings.TrimPrefix(srv.URL, "http://")
	if _, err := m.Check(context.Background(), acct, testCode); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("no code yet: %v", err)
	}
	field = testCode
	a, err := m.Check(context.Background(), acct, testCode)
	if err != nil || a.ExternalID != acct || *a.ProfileURL != "https://mastodon.example/@alice" {
		t.Fatalf("field %+v %v", a, err)
	}
	field, note = "", "<p>"+strings.ReplaceAll(testCode, "=", "&#61;")+"</p>"
	if _, err := m.Check(context.Background(), acct, testCode); err != nil {
		t.Fatalf("code in the bio: %v", err)
	}
}

type fakeResolver map[string][]string

func (f fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if r, ok := f[name]; ok {
		return r, nil
	}
	return nil, errors.New("no record")
}

func TestWebsiteCheck(t *testing.T) {
	page := "<html><head><title>x</title></head></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, page) }))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	w := &Website{Client: srv.Client(), Resolver: fakeResolver{"dns.example": {"v=spf1 -all", testCode}}, Scheme: "http"}
	a, err := w.Check(context.Background(), "dns.example", testCode)
	if err != nil || a.ExternalID != "dns.example" || *a.ProfileURL != "https://dns.example" {
		t.Fatalf("txt %+v %v", a, err)
	}
	if _, err := w.Check(context.Background(), host, testCode); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("no tag yet: %v", err)
	}
	page = `<html><head><meta content="abcdefghijklmnop" name="messenger-verification"></head></html>`
	if _, err := w.Check(context.Background(), host, testCode); err != nil {
		t.Fatalf("meta tag with the bare code: %v", err)
	}
	page = `<meta name='messenger-verification' content='` + testCode + `'>`
	if _, err := w.Check(context.Background(), host, testCode); err != nil {
		t.Fatalf("meta tag with the full code: %v", err)
	}
	page = `<p>` + testCode + `</p>`
	if _, err := w.Check(context.Background(), host, testCode); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("text on the page is not a tag: %v", err)
	}
}
