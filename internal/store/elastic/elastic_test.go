package elastic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPing(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_cluster/health" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()

	s, err := New(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	status = http.StatusUnauthorized
	if err := s.Ping(ctx); err == nil {
		t.Fatal("a 401 must fail the ping")
	}
	srv.Close()
	if err := s.Ping(ctx); err == nil {
		t.Fatal("an unreachable server must fail the ping")
	}
}

func TestNewRejectsInvalidURL(t *testing.T) {
	for _, u := range []string{"not a url", "ftp://host:9200", "http://host?tenant=x", "http://host#frag"} {
		if _, err := New(u); err == nil {
			t.Errorf("New(%q) must fail", u)
		}
	}
}
