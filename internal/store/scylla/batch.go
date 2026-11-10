package scylla

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/myronsi/messenger-back/internal/ids"
)

const maxInList = 100

type bucketChunk struct {
	bucket int
	ids    []int64
}

func chunked(values []int64) [][]int64 {
	var out [][]int64
	for c := range slices.Chunk(values, maxInList) {
		out = append(out, c)
	}
	return out
}

func uniqueIDs(values []int64) []int64 {
	out := slices.Clone(values)
	slices.Sort(out)
	return slices.Compact(out)
}

func (r *Messages) GetMany(ctx context.Context, chatID int64, messageIDs []int64) (map[int64]Message, error) {
	out := make(map[int64]Message, len(messageIDs))
	if len(messageIDs) == 0 {
		return out, nil
	}
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var (
		mu       sync.Mutex
		byBucket = map[int][]int64{}
		legacy   []int64
	)
	for _, id := range uniqueIDs(messageIDs) {
		if ids.IsSnowflake(id) {
			b := BucketOf(ids.Time(id))
			byBucket[b] = append(byBucket[b], id)
		} else {
			legacy = append(legacy, id)
		}
	}
	err = each(ctx, legacy, func(ctx context.Context, id int64) error {
		b, err := r.bucketFor(ctx, sess, chatID, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		mu.Lock()
		byBucket[b] = append(byBucket[b], id)
		mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	var chunks []bucketChunk
	for b, list := range byBucket {
		for _, c := range chunked(list) {
			chunks = append(chunks, bucketChunk{bucket: b, ids: c})
		}
	}
	err = each(ctx, chunks, func(ctx context.Context, c bucketChunk) error {
		iter := sess.Query(`SELECT `+messageColumns+` FROM messages WHERE chat_id = ? AND bucket = ? AND message_id IN ?`,
			chatID, c.bucket, c.ids).WithContext(ctx).Iter()
		var rows []Message
		for {
			m, ok := scanMessage(chatID, iter.Scan)
			if !ok {
				break
			}
			if m.Type != "" {
				rows = append(rows, m)
			}
		}
		if err := iter.Close(); err != nil {
			return err
		}
		mu.Lock()
		for _, m := range rows {
			out[m.ID] = m
		}
		mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	return out, nil
}

func (r *Messages) HiddenOf(ctx context.Context, viewer, chatID int64, messageIDs []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if viewer == 0 || len(messageIDs) == 0 {
		return out, nil
	}
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var mu sync.Mutex
	err = each(ctx, chunked(uniqueIDs(messageIDs)), func(ctx context.Context, c []int64) error {
		iter := sess.Query(`SELECT message_id FROM hidden_messages WHERE user_id = ? AND chat_id = ? AND message_id IN ?`,
			viewer, chatID, c).WithContext(ctx).Iter()
		var found []int64
		var id int64
		for iter.Scan(&id) {
			found = append(found, id)
		}
		if err := iter.Close(); err != nil {
			return err
		}
		mu.Lock()
		for _, id := range found {
			out[id] = true
		}
		mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hidden messages: %w", err)
	}
	return out, nil
}
