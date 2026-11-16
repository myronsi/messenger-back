package search

import (
	"context"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func TestIndexedTextAndExcerptsUseShortcodes(t *testing.T) {
	content := "hello <:party_cat:42> world"
	m := scylla.Message{ID: 2, ChatID: 1, Type: scylla.TypeText, Content: &content, CreatedAt: time.Now()}
	d := (&Indexer{}).doc(context.Background(), m)
	if d.Content != "hello :party_cat: world" {
		t.Fatalf("indexed %q", d.Content)
	}
	h := map[string][]string{contentHighlights[0]: {"hello " + MarkStart + ":party_cat:" + MarkEnd + " world"}}
	if got := current(h, m); got == nil {
		t.Fatal("an excerpt with the shortcode still matches the stored message")
	}
}
