package linkpreview

import (
	"slices"
	"testing"
)

func TestFindURLs(t *testing.T) {
	got := FindURLs("see (https://Example.com/a?b=1), www.go.dev. and http://x.org/#frag twice https://example.com/a?b=1 ftp://no mailto:a@b.c", 0)
	want := []string{"https://example.com/a?b=1", "https://www.go.dev", "http://x.org/"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := FindURLs("https://a.com https://b.com https://c.com", 2); len(got) != 2 {
		t.Fatalf("limit: %q", got)
	}
	if got := FindURLs("no links here, just text.", 0); len(got) != 0 {
		t.Fatalf("plain text: %q", got)
	}
}
