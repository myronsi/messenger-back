package scylla

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/gocql/gocql"

	"github.com/myronsi/messenger-back/internal/ids"
)

const importBatchRows = 50

func (r *Messages) InsertImported(ctx context.Context, msgs []Message, at time.Time) error {
	if len(msgs) == 0 {
		return nil
	}
	byBucket := map[int][]Message{}
	for _, m := range msgs {
		if !validType(m.Type) || m.ID <= 0 || m.ChatID <= 0 || m.CreatedAt.IsZero() || !ids.IsSnowflake(m.ID) {
			return errors.New("insert imported messages: invalid message")
		}
		b := BucketOf(ids.Time(m.ID))
		byBucket[b] = append(byBucket[b], m)
	}
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	ts := at.UnixMicro()
	buckets := make([]int, 0, len(byBucket))
	for b := range byBucket {
		buckets = append(buckets, b)
	}
	chatID := msgs[0].ChatID
	err = each(ctx, buckets, func(ctx context.Context, b int) error {
		return sess.Query(`INSERT INTO chat_buckets (chat_id, bucket) VALUES (?, ?) USING TIMESTAMP ?`, chatID, b, ts).
			WithContext(ctx).Idempotent(true).Exec()
	})
	if err != nil {
		return fmt.Errorf("insert imported messages: %w", err)
	}
	err = each(ctx, msgs, func(ctx context.Context, m Message) error {
		return sess.Query(`INSERT INTO message_locations (message_id, chat_id, bucket) VALUES (?, ?, ?) USING TIMESTAMP ?`,
			m.ID, m.ChatID, BucketOf(ids.Time(m.ID)), ts).WithContext(ctx).Idempotent(true).Exec()
	})
	if err != nil {
		return fmt.Errorf("insert imported messages: %w", err)
	}
	var chunks [][]Message
	for _, b := range buckets {
		for c := range slices.Chunk(byBucket[b], importBatchRows) {
			chunks = append(chunks, c)
		}
	}
	err = each(ctx, chunks, func(ctx context.Context, chunk []Message) error {
		batch := sess.Batch(gocql.UnloggedBatch).WithContext(ctx)
		for _, m := range chunk {
			batch.Query(insertMessage+` USING TIMESTAMP ?`, append(insertValues(m, BucketOf(ids.Time(m.ID))), ts)...)
		}
		return batch.Exec()
	})
	if err != nil {
		return fmt.Errorf("insert imported messages: %w", err)
	}
	return nil
}

func (r *Messages) DeleteRange(ctx context.Context, chatID, from, to int64) error {
	if from <= 0 || to < from || !ids.IsSnowflake(from) {
		return nil
	}
	sess, err := r.s.Session(ctx)
	if err != nil {
		return err
	}
	lctx, cancel := context.WithTimeout(ctx, r.timeout)
	users, err := r.hiddenUsers(lctx, sess, chatID)
	cancel()
	if err != nil {
		return err
	}
	for b := BucketOf(ids.Time(from)); b <= BucketOf(ids.Time(to)); b++ {
		for {
			done, err := r.deleteRangeChunk(ctx, sess, chatID, b, from, to)
			if err != nil {
				return fmt.Errorf("delete range: %w", err)
			}
			if done {
				break
			}
		}
	}
	return each(ctx, users, func(ctx context.Context, uid int64) error {
		return sess.Query(`DELETE FROM hidden_messages WHERE user_id = ? AND chat_id = ? AND message_id >= ? AND message_id <= ?`,
			uid, chatID, from, to).WithContext(ctx).Idempotent(true).Exec()
	})
}

func (r *Messages) deleteRangeChunk(ctx context.Context, sess *gocql.Session, chatID int64, b int, from, to int64) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	iter := sess.Query(`SELECT message_id FROM messages WHERE chat_id = ? AND bucket = ? AND message_id >= ? AND message_id <= ? LIMIT ?`,
		chatID, b, from, to, deleteChunk).WithContext(ctx).Iter()
	var found []int64
	var id int64
	for iter.Scan(&id) {
		found = append(found, id)
	}
	if err := iter.Close(); err != nil {
		return false, err
	}
	if len(found) == 0 {
		return true, nil
	}
	err := each(ctx, found, func(ctx context.Context, id int64) error {
		if err := sess.Query(`DELETE FROM message_reactions WHERE chat_id = ? AND message_id = ?`, chatID, id).WithContext(ctx).Idempotent(true).Exec(); err != nil {
			return err
		}
		return sess.Query(`DELETE FROM message_locations WHERE message_id = ?`, id).WithContext(ctx).Idempotent(true).Exec()
	})
	if err != nil {
		return false, err
	}
	err = sess.Query(`DELETE FROM messages WHERE chat_id = ? AND bucket = ? AND message_id >= ? AND message_id <= ?`,
		chatID, b, found[len(found)-1], found[0]).WithContext(ctx).Idempotent(true).Exec()
	return false, err
}
