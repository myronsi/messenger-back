package httpapi

import (
	"context"
	"net/http"
	"testing"
)

func TestSearchByKindOverHTTP(t *testing.T) {
	e := newChatEnvWith(t, true)
	alice, bob := e.register(t, "alice"), e.register(t, "bob")
	ab := e.create(t, alice, e.uid(t, bob), "").json()["id"].(string)
	e.send(t, alice, ab, "k-1", "The plan is on https://example.com/plan")
	e.send(t, alice, ab, "k-2", "The plan without a link")
	if err := e.index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	get := func(path string) reply {
		return e.do(t, request{method: http.MethodGet, path: path, token: bob.access})
	}
	if got := items(t, get("/search/messages?kind=link")); len(got) != 1 {
		t.Fatalf("links without words: %v", got)
	}
	if got := items(t, get("/search/messages?kind=link&q=plan")); len(got) != 1 {
		t.Fatalf("links with words: %v", got)
	}
	if r := get("/search/messages"); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("neither words nor kind: %d %s", r.Code, r.Body)
	}
	if r := get("/search/messages?kind=photo"); r.Code != http.StatusBadRequest && r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown kind: %d %s", r.Code, r.Body)
	}
}
