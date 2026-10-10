package search

import (
	"context"
	"fmt"
	"strconv"
	"time"

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

// Reconcile rewrites the messages of every chat with activity in the window, so what the stream missed
// (Redis was briefly away, entries trimmed before they were indexed) is repaired.
func (in *Indexer) Reconcile(ctx context.Context, chats Chats, window time.Duration) error {
	since := time.Now().Add(-window)
	return walk(ctx, chats, &since, func(chatID int64) error {
		return in.indexChat(ctx, in.ix.alias, chatID, since)
	})
}

// Rebuild builds a new index from the message store and switches the alias to it without downtime: searches
// use the old index until the new one is complete. Writes that went to the old index meanwhile are caught up
// from the message store before the switch, the rest by the next reconciliation.
func (in *Indexer) Rebuild(ctx context.Context, chats Chats) (string, error) {
	ix := in.ix
	old, err := ix.Current(ctx)
	if err != nil {
		return "", err
	}
	name := ix.next(old)
	if err := ix.es.Do(ctx, "PUT", "/"+name, map[string]any{}, nil); err != nil {
		return "", fmt.Errorf("new search index: %w", err)
	}
	started := time.Now()
	if err := walk(ctx, chats, nil, func(chatID int64) error { return in.indexChat(ctx, name, chatID, time.Time{}) }); err != nil {
		return "", err
	}
	// Catch up with the messages of the rebuild's own run, then switch.
	since := started.Add(-time.Minute)
	if err := walk(ctx, chats, &since, func(chatID int64) error { return in.indexChat(ctx, name, chatID, since) }); err != nil {
		return "", err
	}
	actions := []map[string]any{{"add": map[string]any{"index": name, "alias": ix.alias, "is_write_index": true}}}
	if old != "" {
		actions = append([]map[string]any{{"remove": map[string]any{"index": old, "alias": ix.alias}}}, actions...)
	}
	if err := ix.es.Do(ctx, "POST", "/_aliases", map[string]any{"actions": actions}, nil); err != nil {
		return "", fmt.Errorf("switch search alias: %w", err)
	}
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
