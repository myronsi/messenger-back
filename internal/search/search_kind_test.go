package search_test

import (
	"testing"

	"github.com/myronsi/messenger-back/internal/search"
)

func TestSearchByKindWithAndWithoutWords(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	ab := e.chat(alice, bob)
	e.send(ab, alice, "harbour photos at https://example.com/harbour")
	e.send(ab, bob, "harbour trip without a link")
	e.send(ab, bob, "the plan is on https://example.org/plan")

	if got := contents(e.find(bob, search.Query{Kind: search.KindLink})); len(got) != 2 {
		t.Fatalf("every link, newest first: %v", got)
	}
	if got := contents(e.find(bob, search.Query{Kind: search.KindLink, Text: "harbour"})); len(got) != 1 || got[0] != "harbour photos at https://example.com/harbour" {
		t.Fatalf("links about the harbour: %v", got)
	}
	if got := e.find(bob, search.Query{Kind: "file"}); len(got) != 0 {
		t.Fatalf("no files were sent: %v", contents(got))
	}
	if got := e.find(alice, search.Query{Text: "harbour"}); len(got) != 2 {
		t.Fatalf("words without a kind: %v", contents(got))
	}
}
