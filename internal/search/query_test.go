package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func TestTemplateHasPrefixGermanAndSplitFields(t *testing.T) {
	raw, err := json.Marshal(NewIndex(nil, "", 0).template())
	if err != nil {
		t.Fatal(err)
	}
	var tpl struct {
		Template struct {
			Settings struct {
				Analysis struct {
					CharFilter map[string]map[string]any `json:"char_filter"`
					Filter     map[string]map[string]any `json:"filter"`
					Analyzer   map[string]struct {
						CharFilter []string `json:"char_filter"`
						Filter     []string `json:"filter"`
					} `json:"analyzer"`
				} `json:"analysis"`
			} `json:"settings"`
			Mappings struct {
				Meta       map[string]any `json:"_meta"`
				Properties map[string]struct {
					Analyzer string `json:"analyzer"`
					Fields   map[string]struct {
						Analyzer       string `json:"analyzer"`
						SearchAnalyzer string `json:"search_analyzer"`
					} `json:"fields"`
				} `json:"properties"`
			} `json:"mappings"`
		} `json:"template"`
		Meta map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &tpl); err != nil {
		t.Fatal(err)
	}
	a := tpl.Template.Settings.Analysis
	if g := a.Filter["prefix_grams"]; g["type"] != "edge_ngram" || g["min_gram"] != float64(2) || g["max_gram"] != float64(20) {
		t.Fatalf("prefix grams: %v", g)
	}
	if c := a.CharFilter["split_words"]; c["type"] != "pattern_replace" {
		t.Fatalf("split words: %v", c)
	}
	for name, want := range map[string][]string{
		"folded":        {"lowercase", "asciifolding"},
		"prefix":        {"lowercase", "asciifolding", "prefix_grams"},
		"prefix_search": {"lowercase", "asciifolding", "prefix_cut"},
	} {
		an := a.Analyzer[name]
		if !slices.Equal(an.Filter, want) || !slices.Equal(an.CharFilter, []string{"split_words"}) {
			t.Errorf("analyzer %s: %+v", name, an)
		}
	}
	props := tpl.Template.Mappings.Properties
	if f := props["content"].Fields["de"]; f.Analyzer != "german" {
		t.Errorf("content.de: %+v", f)
	}
	for _, field := range []string{"content", "file_name"} {
		if f := props[field].Fields["prefix"]; f.Analyzer != "prefix" || f.SearchAnalyzer != "prefix_search" {
			t.Errorf("%s.prefix: %+v", field, f)
		}
	}
	if tpl.Template.Mappings.Meta["mapping_version"] != float64(MappingVersion) || tpl.Meta["mapping_version"] != float64(MappingVersion) {
		t.Errorf("mapping version: %v %v", tpl.Template.Mappings.Meta, tpl.Meta)
	}
}

func TestTextQueryMatchesEveryWordWholeOrByPrefix(t *testing.T) {
	q := textQuery("lore a ipsum")
	must := q["bool"].(map[string]any)["must"].([]any)
	if len(must) != 3 {
		t.Fatalf("clauses: %v", must)
	}
	fields := func(c any) []string {
		return c.(map[string]any)["multi_match"].(map[string]any)["fields"].([]string)
	}
	for _, i := range []int{0, 2} {
		should := must[i].(map[string]any)["bool"].(map[string]any)["should"].([]any)
		if len(should) != 2 || !slices.Contains(fields(should[0]), "content.de") || !slices.Equal(fields(should[1]), []string{"content.prefix", "file_name.prefix"}) {
			t.Fatalf("word %d: %v", i, should)
		}
		if mm := should[1].(map[string]any)["multi_match"].(map[string]any); mm["operator"] != "and" {
			t.Fatalf("prefix operator: %v", mm)
		}
	}
	if f := fields(must[1]); slices.Contains(f, "content.prefix") {
		t.Fatalf("one-letter word matched by prefix: %v", f)
	}
	long := strings.Repeat("w ", 50)
	if got := textQuery(long)["bool"].(map[string]any)["must"].([]any); len(got) != maxTerms {
		t.Fatalf("long query has %d clauses", len(got))
	}
	raw, _ := json.Marshal(request(7, "lore", []string{"1"}, Query{}))
	for _, want := range []string{`"content.prefix":{`, `"file_name.prefix":{`, `"content.de":{`, `"hidden_for":"7"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("request lacks %s: %s", want, raw)
		}
	}
}

func TestExcerptWithTheMostMatchesWins(t *testing.T) {
	content := "Lorem ipsum dolor"
	m := scylla.Message{Content: &content}
	got := current(map[string][]string{
		"content":        {MarkStart + "ipsum" + MarkEnd + " dolor"},
		"content.prefix": {MarkStart + "Lorem" + MarkEnd + " " + MarkStart + "ipsum" + MarkEnd + " dolor"},
	}, m)
	if got == nil || strings.Count(*got, MarkStart) != 2 {
		t.Fatalf("excerpt: %v", got)
	}
}

type fakeES struct {
	mu      sync.Mutex
	hits    []esHit
	queries int
}

func (f *fakeES) Do(_ context.Context, _, path string, body, out any) error {
	if !strings.HasSuffix(path, "/_search") {
		return errors.New("unexpected " + path)
	}
	f.mu.Lock()
	f.queries++
	f.mu.Unlock()
	b := body.(map[string]any)
	size := b["size"].(int)
	start := 0
	if after, ok := b["search_after"].([]any); ok {
		ms, mid := toInt(after[0]), fmt.Sprint(after[1])
		start = len(f.hits)
		for i, h := range f.hits {
			hms, hid := toInt(h.Sort[0]), h.Sort[1].(string)
			if hms < ms || (hms == ms && hid < mid) {
				start = i
				break
			}
		}
	}
	page := f.hits[start:min(start+size, len(f.hits))]
	raw, _ := json.Marshal(map[string]any{"hits": map[string]any{"hits": page}})
	return json.Unmarshal(raw, out)
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

type fakeMessages struct {
	Messages
	mu      sync.Mutex
	stored  map[int64]scylla.Message
	hidden  map[int64]bool
	getMany int
	hidOf   int
}

func (f *fakeMessages) GetMany(_ context.Context, chatID int64, ids []int64) (map[int64]scylla.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getMany++
	out := map[int64]scylla.Message{}
	for _, id := range ids {
		if m, ok := f.stored[id]; ok && m.ChatID == chatID {
			out[id] = m
		}
	}
	return out, nil
}

func (f *fakeMessages) HiddenOf(_ context.Context, _, _ int64, ids []int64) (map[int64]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hidOf++
	out := map[int64]bool{}
	for _, id := range ids {
		if f.hidden[id] {
			out[id] = true
		}
	}
	return out, nil
}

type member struct{}

func (member) ChatIDs(context.Context, int64) ([]int64, error)      { return []int64{1, 2}, nil }
func (member) IsMember(context.Context, int64, int64) (bool, error) { return true, nil }

func pageFixture(n int, drop func(i int) string) (*Searcher, *fakeES, *fakeMessages, []int64) {
	es := &fakeES{}
	msgs := &fakeMessages{stored: map[int64]scylla.Message{}, hidden: map[int64]bool{}}
	var visible []int64
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		mid := int64(1000 + n - i)
		chat := int64(1 + i%2)
		at := base.Add(time.Duration(n-i) * time.Minute)
		text := "lorem " + strconv.Itoa(i)
		es.hits = append(es.hits, esHit{ID: id(mid), Sort: []any{float64(at.UnixMilli()), id(mid)}})
		es.hits[i].Source.ChatID = id(chat)
		switch drop(i) {
		case "hidden":
			msgs.hidden[mid] = true
			msgs.stored[mid] = scylla.Message{ID: mid, ChatID: chat, Content: &text}
		case "deleted":
			msgs.stored[mid] = scylla.Message{ID: mid, ChatID: chat, Deleted: true}
		case "gone":
		default:
			msgs.stored[mid] = scylla.Message{ID: mid, ChatID: chat, Content: &text}
			visible = append(visible, mid)
		}
	}
	return NewSearcher(NewIndex(es, "", 0), member{}, msgs), es, msgs, visible
}

func TestPagesAreFilledDespiteDroppedHits(t *testing.T) {
	s, es, msgs, visible := pageFixture(60, func(i int) string {
		switch {
		case i < 20 && i%2 == 0:
			return "hidden"
		case i%7 == 3:
			return "deleted"
		case i%11 == 5:
			return "gone"
		}
		return ""
	})
	var got []int64
	after := ""
	pages := 0
	for {
		hits, next, err := s.Search(context.Background(), 9, Query{Text: "lorem", Limit: 10, After: after})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if next != "" && len(hits) != 10 {
			t.Fatalf("page %d has %d hits before the end", pages, len(hits))
		}
		for _, h := range hits {
			got = append(got, h.Message.ID)
		}
		if next == "" {
			break
		}
		after = next
	}
	if !slices.Equal(got, visible) {
		t.Fatalf("got %v\nwant %v", got, visible)
	}
	if pages != (len(visible)+9)/10 {
		t.Fatalf("%d pages for %d hits", pages, len(visible))
	}
	if msgs.getMany != msgs.hidOf || msgs.getMany > 2*es.queries {
		t.Fatalf("store reads: %d GetMany, %d HiddenOf for %d queries", msgs.getMany, msgs.hidOf, es.queries)
	}
}

func TestPageFillingStopsAfterMaxRounds(t *testing.T) {
	s, es, _, visible := pageFixture(400, func(i int) string {
		if i < 390 {
			return "hidden"
		}
		return ""
	})
	hits, next, err := s.Search(context.Background(), 9, Query{Text: "lorem", Limit: 5})
	if err != nil || len(hits) != 0 || next == "" || es.queries != maxRounds {
		t.Fatalf("hits %d, next %q, queries %d, err %v", len(hits), next, es.queries, err)
	}
	var got []int64
	for next != "" {
		hits, next, err = s.Search(context.Background(), 9, Query{Text: "lorem", Limit: 5, After: next})
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hits {
			got = append(got, h.Message.ID)
		}
	}
	if !slices.Equal(got, visible) {
		t.Fatalf("got %v, want %v", got, visible)
	}
}

func TestFullPageAtTheEndHasNoCursor(t *testing.T) {
	s, _, _, _ := pageFixture(10, func(int) string { return "" })
	hits, next, err := s.Search(context.Background(), 9, Query{Text: "lorem", Limit: 10})
	if err != nil || len(hits) != 10 || next != "" {
		t.Fatalf("hits %d, next %q, err %v", len(hits), next, err)
	}
	hits, next, _ = s.Search(context.Background(), 9, Query{Text: "lorem", Limit: 9})
	if len(hits) != 9 || next == "" {
		t.Fatalf("hits %d, next %q", len(hits), next)
	}
	if _, _, err := s.Search(context.Background(), 9, Query{Text: strings.Repeat("a", MaxQuery)}); err != nil {
		t.Fatalf("query of %d characters: %v", MaxQuery, err)
	}
	if _, _, err := s.Search(context.Background(), 9, Query{Text: strings.Repeat("a", MaxQuery+1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("query of %d characters: %v", MaxQuery+1, err)
	}
}
