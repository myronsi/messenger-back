// Package search keeps the Elasticsearch index of messages and answers searches with it. The index is fed
// by the event streams (Indexer), can be rebuilt from ScyllaDB (Rebuild) and repaired for recent changes
// (Reconcile). Elasticsearch never decides access: every query is limited to the caller's chats, and every hit
// is checked against ScyllaDB again before it is shown.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/store/elastic"
)

// DefaultAlias is the alias searches and writes go through; the indices behind it are <alias>-v<n>.
const DefaultAlias = "messages"

// ES is the part of the Elasticsearch store the package uses.
type ES interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Index manages the message index: the template of its mapping, the versioned indices and the alias.
type Index struct {
	es    ES
	alias string
	// replicas of new indices (0 for a single node).
	replicas int
}

// NewIndex returns the index behind alias ("" for DefaultAlias).
func NewIndex(es ES, alias string, replicas int) *Index {
	if alias == "" {
		alias = DefaultAlias
	}
	return &Index{es: es, alias: alias, replicas: replicas}
}

// Alias is the name searches use.
func (ix *Index) Alias() string { return ix.alias }

// Markers wrap the highlighted words of a search hit: private-use characters, never HTML.
const (
	MarkStart = ""
	MarkEnd   = ""
)

// mapping of every index behind the alias. Content is analysed three ways: folded (lower case, accents removed,
// any language) and with the English and Russian stemmers, the languages of the users so far.
func (ix *Index) template() map[string]any {
	return map[string]any{
		"index_patterns": []string{ix.alias + "-v*"},
		"priority":       200,
		"template": map[string]any{
			"settings": map[string]any{
				"number_of_shards":   1,
				"number_of_replicas": ix.replicas,
				"analysis": map[string]any{
					"analyzer": map[string]any{
						"folded": map[string]any{"type": "custom", "tokenizer": "standard", "filter": []string{"lowercase", "asciifolding"}},
					},
				},
			},
			"mappings": map[string]any{
				"dynamic": "strict",
				"properties": map[string]any{
					"message_id": map[string]any{"type": "keyword"},
					"chat_id":    map[string]any{"type": "keyword"},
					"sender_id":  map[string]any{"type": "keyword"},
					"type":       map[string]any{"type": "keyword"},
					"content": map[string]any{"type": "text", "analyzer": "folded", "fields": map[string]any{
						"en": map[string]any{"type": "text", "analyzer": "english"},
						"ru": map[string]any{"type": "text", "analyzer": "russian"},
					}},
					"file_name":  map[string]any{"type": "text", "analyzer": "folded", "fields": map[string]any{"raw": map[string]any{"type": "keyword", "ignore_above": 256}}},
					"hidden_for": map[string]any{"type": "keyword"},
					"created_at": map[string]any{"type": "date"},
				},
			},
		},
		"_meta": map[string]any{"owner": "messenger-back internal/search"},
	}
}

// Ensure installs the template and, when the alias does not exist yet, the first index behind it. It is
// idempotent; the worker calls it on start.
func (ix *Index) Ensure(ctx context.Context) error {
	if err := ix.es.Do(ctx, "PUT", "/_index_template/"+ix.alias, ix.template(), nil); err != nil {
		return fmt.Errorf("search template: %w", err)
	}
	current, err := ix.Current(ctx)
	if err != nil {
		return err
	}
	if current != "" {
		return nil
	}
	err = ix.es.Do(ctx, "PUT", "/"+ix.alias+"-v1", map[string]any{"aliases": map[string]any{ix.alias: map[string]any{"is_write_index": true}}}, nil)
	var se *elastic.StatusError
	if errors.As(err, &se) && se.Type == "resource_already_exists_exception" {
		return nil // another worker was first
	}
	if err != nil {
		return fmt.Errorf("first search index: %w", err)
	}
	return nil
}

// Current returns the index behind the alias ("" when there is none).
func (ix *Index) Current(ctx context.Context) (string, error) {
	var out map[string]json.RawMessage
	err := ix.es.Do(ctx, "GET", "/_alias/"+ix.alias, nil, &out)
	if errors.Is(err, elastic.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("search alias: %w", err)
	}
	names := make([]string, 0, len(out))
	for name := range out {
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return "", nil
	}
	return names[len(names)-1], nil
}

// next is the name of the index after current: <alias>-v<n+1>.
func (ix *Index) next(current string) string {
	n := 0
	if i := strings.LastIndex(current, "-v"); i >= 0 {
		num, _, _ := strings.Cut(current[i+2:], "-")
		n, _ = strconv.Atoi(num)
	}
	return ix.alias + "-v" + strconv.Itoa(n+1) + "-" + strconv.FormatInt(time.Now().Unix(), 10)
}

// Refresh makes recent writes searchable at once (tests; production relies on the 1 s refresh).
func (ix *Index) Refresh(ctx context.Context) error {
	return ix.es.Do(ctx, "POST", "/"+ix.alias+"/_refresh", nil, nil)
}

// Drop deletes the alias, its indices and the template (tests).
func (ix *Index) Drop(ctx context.Context) error {
	if err := ix.es.Do(ctx, "DELETE", "/"+ix.alias+"-v*", nil, nil); err != nil && !errors.Is(err, elastic.ErrNotFound) {
		return err
	}
	if err := ix.es.Do(ctx, "DELETE", "/_index_template/"+ix.alias, nil, nil); err != nil && !errors.Is(err, elastic.ErrNotFound) {
		return err
	}
	return nil
}
