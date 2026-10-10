package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/myronsi/messenger-back/internal/store/elastic"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

// Chats walks the chats (postgres chat repository).
type Chats interface {
	IDsAfter(ctx context.Context, afterID int64, since *time.Time, limit int) ([]int64, error)
}

// pageSize is how many messages are read and written at once while rebuilding.
const pageSize = 500

// indexChat writes the chat's messages created since `since` (zero: all of them) into the named index, newest
// first, and removes those that are deleted.
func (in *Indexer) indexChat(ctx context.Context, index string, chatID int64, since time.Time) error {
	hidden, err := in.msgs.DeletedForMe(ctx, chatID)
	if err != nil {
		return err
	}
	q := scylla.PageQuery{ChatID: chatID, Limit: pageSize}
	for {
		page, err := in.msgs.Page(ctx, q)
		if err != nil {
			return err
		}
		var (
			docs    []Doc
			deletes []string
			older   bool
		)
		for i := len(page.Messages) - 1; i >= 0; i-- {
			m := page.Messages[i]
			if !since.IsZero() && m.CreatedAt.Before(since) {
				older = true
				break
			}
			if !indexable(m) {
				deletes = append(deletes, id(m.ID))
				continue
			}
			d := in.doc(ctx, m)
			for _, uid := range hidden[m.ID] {
				d.HiddenFor = append(d.HiddenFor, id(uid))
			}
			docs = append(docs, d)
		}
		if err := in.bulk(ctx, index, docs, deletes); err != nil {
			return err
		}
		if older || !page.HasOlder || len(page.Messages) == 0 {
			return nil
		}
		q.Before = page.Messages[0].ID
	}
}

// walk runs fn for every chat (with since: those with activity since then).
func walk(ctx context.Context, chats Chats, since *time.Time, fn func(chatID int64) error) error {
	after := int64(0)
	for {
		ids, err := chats.IDsAfter(ctx, after, since, pageSize)
		if err != nil {
			return err
		}
		for _, c := range ids {
			if err := fn(c); err != nil {
				return fmt.Errorf("chat %d: %w", c, err)
			}
		}
		if len(ids) < pageSize {
			return nil
		}
		after = ids[len(ids)-1]
	}
}

// Reconcile writes the messages created in the window again, for every chat with activity in it, so messages
// whose created event the stream lost (Redis away for a moment, entries trimmed before they were indexed) are
// found. Edits, deletions and hides of older messages that the stream lost are not repaired here: the store is
// checked for every hit anyway, so they can only make a result page shorter, and a rebuild repairs them.
func (in *Indexer) Reconcile(ctx context.Context, chats Chats, window time.Duration) error {
	since := time.Now().Add(-window)
	return walk(ctx, chats, &since, func(chatID int64) error {
		return in.indexChat(ctx, in.ix.alias, chatID, since)
	})
}

// ErrRebuilding: another rebuild runs (its alias exists).
var ErrRebuilding = errors.New("search: a rebuild is running")

// Rebuild builds a new index from the message store and switches the alias to it without downtime: searches use
// the old index until the new one is complete.
//
// While it runs, the new index also stands behind the rebuild alias, which every indexer writes to as well (they
// notice within rebuildCheck; the copy starts after rebuildSettle), so edits, deletions and hides during the
// rebuild reach the new index too. The copy merges hidden_for instead of replacing it. The rebuild alias is also
// the lock: a second rebuild refuses to start while it exists.
func (in *Indexer) Rebuild(ctx context.Context, chats Chats) (string, error) {
	ix := in.ix
	var existing map[string]json.RawMessage
	if err := ix.es.Do(ctx, "GET", "/_alias/"+ix.rebuildAlias(), nil, &existing); err == nil && len(existing) > 0 {
		return "", ErrRebuilding
	} else if err != nil && !errors.Is(err, elastic.ErrNotFound) {
		return "", err
	}
	old, err := ix.Current(ctx)
	if err != nil {
		return "", err
	}
	name := ix.next(old)
	err = ix.es.Do(ctx, "PUT", "/"+name, map[string]any{"aliases": map[string]any{ix.rebuildAlias(): map[string]any{}}}, nil)
	if err != nil {
		return "", fmt.Errorf("new search index: %w", err)
	}
	ok := false
	defer func() {
		if !ok { // leave nothing behind: the next rebuild starts over
			_ = ix.es.Do(context.WithoutCancel(ctx), "DELETE", "/"+name, nil, nil)
		}
	}()
	in.mu.Lock()
	in.rebuild, in.checkedAt = true, time.Now()
	in.mu.Unlock()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(in.settle):
	}
	if err := walk(ctx, chats, nil, func(chatID int64) error { return in.indexChat(ctx, name, chatID, time.Time{}) }); err != nil {
		return "", err
	}
	actions := []map[string]any{
		{"add": map[string]any{"index": name, "alias": ix.alias, "is_write_index": true}},
		{"remove": map[string]any{"index": name, "alias": ix.rebuildAlias()}},
	}
	if old != "" {
		actions = append([]map[string]any{{"remove": map[string]any{"index": old, "alias": ix.alias}}}, actions...)
	}
	if err := ix.es.Do(ctx, "POST", "/_aliases", map[string]any{"actions": actions}, nil); err != nil {
		return "", fmt.Errorf("switch search alias: %w", err)
	}
	ok = true
	in.mu.Lock()
	in.rebuild, in.checkedAt = false, time.Now()
	in.mu.Unlock()
	if old != "" {
		if err := ix.es.Do(ctx, "DELETE", "/"+old, nil, nil); err != nil {
			in.log.WarnContext(ctx, "delete the previous search index", "index", old, "error", err)
		}
	}
	return name, nil
}

// cursor encodes a search_after position: the creation time in milliseconds and the message id.
func cursor(sort []any) string {
	if len(sort) != 2 {
		return ""
	}
	ms, ok := sort[0].(float64)
	if !ok {
		return ""
	}
	mid, _ := sort[1].(string)
	return strconv.FormatInt(int64(ms), 10) + "_" + mid
}
