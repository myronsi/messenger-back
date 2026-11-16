package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type tokenServer struct {
	*httptest.Server
	challenge string
	verifier  string
}

func newTokenServer(t *testing.T) *tokenServer {
	t.Helper()
	ts := &tokenServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			ts.verifier = r.PostForm.Get("code_verifier")
			if r.PostForm.Get("code") != "good" || r.PostForm.Get("client_secret") != "s3cret" || pkceChallenge(ts.verifier) != ts.challenge {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
		case "/user":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "login": "octo", "html_url": "https://github.com/octo"})
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func testService(t *testing.T, ts *tokenServer, store *memStore, v ...Verifier) (*Service, *memState) {
	t.Helper()
	p := GitHub(Credentials{ClientID: "id", ClientSecret: "s3cret"})
	p.AuthorizeURL, p.ExchangeURL, p.Profile = ts.URL+"/authorize", ts.URL+"/token", githubProfile(ts.URL)
	state := newMemState()
	return NewService(Options{Providers: []Provider{p}, Verifiers: v, Store: store, State: state,
		CallbackURL: "https://app.example/api/v2", WebURL: "https://app.example"}), state
}

func startQuery(t *testing.T, s *Service, ts *tokenServer, userID int64) url.Values {
	t.Helper()
	link, err := s.Start(context.Background(), userID, "github")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	q := u.Query()
	if q.Get("redirect_uri") != "https://app.example/api/v2/connections/callback/github" || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "id" {
		t.Fatalf("auth url %s", link)
	}
	ts.challenge = q.Get("code_challenge")
	return q
}

func result(t *testing.T, location string) string {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil || !strings.HasPrefix(location, "https://app.example/settings/connections?") || u.Query().Get("provider") != "github" {
		t.Fatalf("location %s", location)
	}
	return u.Query().Get("result")
}

func TestOAuthConnectWithPKCEAndOneTimeState(t *testing.T) {
	ts, store := newTokenServer(t), &memStore{}
	s, _ := testService(t, ts, store)
	q := startQuery(t, s, ts, 7)
	cb := url.Values{"code": {"good"}, "state": {q.Get("state")}}
	if got := result(t, s.Callback(context.Background(), "github", cb)); got != ResultConnected {
		t.Fatalf("result %s", got)
	}
	list, _ := store.List(context.Background(), 7)
	if len(list) != 1 || list[0].ExternalID != "42" || list[0].DisplayName != "octo" || *list[0].ProfileURL != "https://github.com/octo" {
		t.Fatalf("stored %+v", list)
	}
	if got := result(t, s.Callback(context.Background(), "github", cb)); got != ResultFailed {
		t.Fatalf("a state works once: %s", got)
	}
}

func TestOAuthCallbackFailures(t *testing.T) {
	ts, store := newTokenServer(t), &memStore{}
	s, _ := testService(t, ts, store)
	q := startQuery(t, s, ts, 7)
	if got := result(t, s.Callback(context.Background(), "github", url.Values{"error": {"access_denied"}, "state": {q.Get("state")}})); got != ResultDenied {
		t.Fatalf("denied: %s", got)
	}
	q = startQuery(t, s, ts, 7)
	if got := result(t, s.Callback(context.Background(), "github", url.Values{"code": {"bad"}, "state": {q.Get("state")}})); got != ResultFailed {
		t.Fatalf("bad code: %s", got)
	}
	if got := result(t, s.Callback(context.Background(), "github", url.Values{"code": {"good"}, "state": {"forged"}})); got != ResultFailed {
		t.Fatalf("unknown state: %s", got)
	}
	for i := range postgres.MaxConnections {
		_, _ = store.Connect(context.Background(), 8, postgres.ConnectedAccount{Provider: "website", ExternalID: fmt.Sprint(i), DisplayName: "x"})
	}
	if _, err := s.Start(context.Background(), 8, "github"); !errors.Is(err, postgres.ErrConnectionLimit) {
		t.Fatalf("start at the limit: %v", err)
	}
	q = startQuery(t, s, ts, 9)
	for i := range postgres.MaxConnections {
		_, _ = store.Connect(context.Background(), 9, postgres.ConnectedAccount{Provider: "website", ExternalID: fmt.Sprint(i), DisplayName: "x"})
	}
	if got := result(t, s.Callback(context.Background(), "github", url.Values{"code": {"good"}, "state": {q.Get("state")}})); got != ResultLimit {
		t.Fatalf("limit reached meanwhile: %s", got)
	}
}

func TestProvidersAndFlows(t *testing.T) {
	ts := newTokenServer(t)
	v := &fakeVerifier{name: "website", account: Account{ExternalID: "example.org", DisplayName: "example.org"}}
	s, state := testService(t, ts, &memStore{}, v)
	if got := strings.Join(s.Enabled(), ","); got != "github,website" {
		t.Fatalf("enabled %s", got)
	}
	if _, err := s.Start(context.Background(), 1, "website"); !errors.Is(err, ErrWrongFlow) {
		t.Fatalf("start a verifier: %v", err)
	}
	if _, err := s.Start(context.Background(), 1, "spotify"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("disabled provider: %v", err)
	}
	if _, err := s.Claim(context.Background(), 1, "github", "x"); !errors.Is(err, ErrWrongFlow) {
		t.Fatalf("claim an oauth provider: %v", err)
	}
	if _, err := s.Verify(context.Background(), 1, "website"); !errors.Is(err, ErrNoClaim) {
		t.Fatalf("verify without claim: %v", err)
	}
	ch, err := s.Claim(context.Background(), 1, "website", "example.org")
	if err != nil || !strings.HasPrefix(ch.Code, CodePrefix) || len(ch.Code) != len(CodePrefix)+16 {
		t.Fatalf("claim %+v %v", ch, err)
	}
	v.err = ErrNotVerified
	if _, err := s.Verify(context.Background(), 1, "website"); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("not yet published: %v", err)
	}
	v.err = nil
	c, err := s.Verify(context.Background(), 1, "website")
	if err != nil || c.ExternalID != "example.org" || v.checked[1] != "example.org "+ch.Code {
		t.Fatalf("verify %+v %v %v", c, err, v.checked)
	}
	if _, found, _ := state.Get(context.Background(), claimKey(1, "website")); found {
		t.Fatal("the claim is used up")
	}
	noOAuth := NewService(Options{Providers: []Provider{GitHub(Credentials{"a", "b"})}, Store: &memStore{}, State: newMemState()})
	if len(noOAuth.Enabled()) != 0 {
		t.Fatal("oauth providers need the callback and web URLs")
	}
}
