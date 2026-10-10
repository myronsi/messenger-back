package scylla

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/gocql/gocql"
)

// clockMargin is how far message IDs may be ahead of this process's clock.
const clockMargin = time.Minute

// bucketsPerQuery is how many bucket numbers one read of chat_buckets returns.
const bucketsPerQuery = 16

// minFetch is the smallest number of rows read from a partition at once, so a page with many hidden
// messages does not crawl one row at a time.
const minFetch = 10

// scan walks a chat from a starting point in one direction and collects up to need visible messages.
type scan struct {
	chatID int64
	// viewer's hidden messages are left out; 0 shows everything.
	viewer int64
	desc   bool
	// bound excludes it and everything on its side (0: no bound).
	bound int64
	// start is the first bucket to read; nil starts at the newest (desc) or oldest (asc) bucket.
	start *int
	need  int
	// keep filters further (nil keeps every visible message).
	keep func(Message) bool
}

// run returns the collected messages in scan order and whether more matching messages may follow.
func (r *Messages) run(ctx context.Context, sess *gocql.Session, sc scan) ([]Message, bool, error) {
	var out []Message
	var buckets []int
	var last *int // the last bucket returned by chat_buckets, to continue after it
	exhausted := false
	if sc.start != nil {
		buckets = []int{*sc.start}
		last = sc.start
	}
	for {
		if len(buckets) == 0 {
			if exhausted {
				return out, false, nil
			}
			var err error
			buckets, err = r.bucketsAfter(ctx, sess, sc.chatID, last, sc.desc)
			if err != nil {
				return nil, false, err
			}
			if len(buckets) < bucketsPerQuery {
				exhausted = true
			}
			if len(buckets) == 0 {
				return out, false, nil
			}
			last = &buckets[len(buckets)-1]
		}
		b := buckets[0]
		buckets = buckets[1:]
		done, err := r.readBucket(ctx, sess, sc, b, &out)
		if err != nil {
			return nil, false, err
		}
		if done {
			return out, true, nil
		}
	}
}

// readBucket appends the visible messages of one bucket to out until it has sc.need of them; done reports
// that it has.
func (r *Messages) readBucket(ctx context.Context, sess *gocql.Session, sc scan, b int, out *[]Message) (bool, error) {
	cursor := sc.bound
	for {
		want := max(sc.need-len(*out), minFetch)
		stmt := `SELECT ` + messageColumns + ` FROM messages WHERE chat_id = ? AND bucket = ?`
		args := []any{sc.chatID, b}
		if cursor != 0 {
			if sc.desc {
				stmt += ` AND message_id < ?`
			} else {
				stmt += ` AND message_id > ?`
			}
			args = append(args, cursor)
		}
		if !sc.desc {
			stmt += ` ORDER BY message_id ASC`
		}
		stmt += ` LIMIT ?`
		args = append(args, want)

		iter := sess.Query(stmt, args...).WithContext(ctx).Iter()
		var rows []Message
		for {
			m, ok := scanMessage(sc.chatID, iter.Scan)
			if !ok {
				break
			}
			rows = append(rows, m)
		}
		if err := iter.Close(); err != nil {
			return false, fmt.Errorf("read messages: %w", err)
		}
		if len(rows) == 0 {
			return false, nil
		}
		hidden, err := r.hiddenIn(ctx, sess, sc.viewer, sc.chatID, rows[0].ID, rows[len(rows)-1].ID)
		if err != nil {
			return false, err
		}
		for _, m := range rows {
			if m.Type == "" || hidden[m.ID] || (sc.keep != nil && !sc.keep(m)) {
				continue
			}
			*out = append(*out, m)
			if len(*out) >= sc.need {
				return true, nil
			}
		}
		if len(rows) < want {
			return false, nil
		}
		cursor = rows[len(rows)-1].ID
	}
}

// bucketsAfter returns the next buckets of the chat after the given one (nil: from the newest or oldest).
func (r *Messages) bucketsAfter(ctx context.Context, sess *gocql.Session, chatID int64, after *int, desc bool) ([]int, error) {
	stmt := `SELECT bucket FROM chat_buckets WHERE chat_id = ?`
	args := []any{chatID}
	if after != nil {
		if desc {
			stmt += ` AND bucket < ?`
		} else {
			stmt += ` AND bucket > ?`
		}
		args = append(args, *after)
	}
	if !desc {
		stmt += ` ORDER BY bucket ASC`
	}
	stmt += ` LIMIT ?`
	args = append(args, bucketsPerQuery)
	iter := sess.Query(stmt, args...).WithContext(ctx).Iter()
	var out []int
	var b int
	for iter.Scan(&b) {
		out = append(out, b)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("chat buckets: %w", err)
	}
	return out, nil
}

// hiddenIn returns the messages between a and b (inclusive, either order) that are hidden for the viewer.
func (r *Messages) hiddenIn(ctx context.Context, sess *gocql.Session, viewer, chatID, a, b int64) (map[int64]bool, error) {
	if viewer == 0 {
		return nil, nil
	}
	lo, hi := min(a, b), max(a, b)
	iter := sess.Query(`SELECT message_id FROM hidden_messages WHERE user_id = ? AND chat_id = ? AND message_id >= ? AND message_id <= ?`,
		viewer, chatID, lo, hi).WithContext(ctx).Iter()
	var out map[int64]bool
	var id int64
	for iter.Scan(&id) {
		if out == nil {
			out = make(map[int64]bool)
		}
		out[id] = true
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("hidden messages: %w", err)
	}
	return out, nil
}

// startAt returns the bucket to start a scan bounded by the message id. A v1 id that has no location
// cannot be placed; the scan then starts at the end and relies on the bound alone.
func (r *Messages) startAt(ctx context.Context, sess *gocql.Session, chatID, messageID int64) (*int, error) {
	b, err := r.bucketFor(ctx, sess, chatID, messageID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > MaxPage:
		return MaxPage
	}
	return n
}

func (r *Messages) Page(ctx context.Context, q PageQuery) (Page, error) {
	set := 0
	for _, v := range []int64{q.Before, q.After, q.Around} {
		if v != 0 {
			set++
		}
	}
	if set > 1 {
		return Page{}, errors.New("page: at most one of before, after and around")
	}
	limit := clampLimit(q.Limit)
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return Page{}, err
	}
	defer cancel()

	older := func(bound int64, n int) ([]Message, bool, error) {
		// The newest page starts at the bucket of now plus a margin without asking chat_buckets, so an active
		// chat's page is a single-partition read. The margin covers IDs that run ahead of this clock (another
		// node's clock, a generator borrowing future milliseconds): within it of a bucket boundary the scan
		// starts at the next bucket, which is usually empty, and continues with the older ones.
		now := BucketOf(time.Now().Add(clockMargin))
		start := &now
		if bound != 0 {
			var err error
			if start, err = r.startAt(ctx, sess, q.ChatID, bound); err != nil {
				return nil, false, err
			}
		}
		msgs, _, err := r.run(ctx, sess, scan{chatID: q.ChatID, viewer: q.Viewer, desc: true, bound: bound, start: start, need: n + 1})
		if err != nil {
			return nil, false, err
		}
		more := len(msgs) > n
		msgs = msgs[:min(n, len(msgs))]
		slices.Reverse(msgs)
		return msgs, more, nil
	}
	newer := func(bound int64, n int) ([]Message, bool, error) {
		start, err := r.startAt(ctx, sess, q.ChatID, bound)
		if err != nil {
			return nil, false, err
		}
		msgs, _, err := r.run(ctx, sess, scan{chatID: q.ChatID, viewer: q.Viewer, bound: bound, start: start, need: n + 1})
		if err != nil {
			return nil, false, err
		}
		more := len(msgs) > n
		return msgs[:min(n, len(msgs))], more, nil
	}

	switch {
	case q.After != 0:
		msgs, more, err := newer(q.After, limit)
		return Page{Messages: msgs, HasOlder: true, HasNewer: more}, err
	case q.Around != 0:
		target, _, err := r.get(ctx, sess, q.ChatID, q.Around)
		if err != nil {
			return Page{}, err
		}
		hidden, err := r.hiddenIn(ctx, sess, q.Viewer, q.ChatID, q.Around, q.Around)
		if err != nil {
			return Page{}, err
		}
		room := limit
		if !hidden[q.Around] {
			room--
		}
		// Read up to a full page on both sides, then centre the window: room one side cannot use goes
		// to the other.
		old, oldMore, err := older(q.Around, room)
		if err != nil {
			return Page{}, err
		}
		nw, newMore, err := newer(q.Around, room)
		if err != nil {
			return Page{}, err
		}
		before := room / 2
		after := room - before
		if len(old) < before {
			after += before - len(old)
			before = len(old)
		}
		if len(nw) < after {
			before = min(len(old), before+after-len(nw))
			after = len(nw)
		}
		hasOlder := oldMore || len(old) > before
		hasNewer := newMore || len(nw) > after
		msgs := slices.Clone(old[len(old)-before:])
		if !hidden[q.Around] {
			msgs = append(msgs, target)
		}
		return Page{Messages: append(msgs, nw[:after]...), HasOlder: hasOlder, HasNewer: hasNewer}, nil
	default:
		msgs, more, err := older(q.Before, limit)
		return Page{Messages: msgs, HasOlder: more, HasNewer: q.Before != 0}, err
	}
}

func (r *Messages) CountAfter(ctx context.Context, chatID, viewer, afterID int64, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return 0, err
	}
	defer cancel()
	var start *int
	if afterID != 0 {
		if start, err = r.startAt(ctx, sess, chatID, afterID); err != nil {
			return 0, err
		}
	}
	keep := func(m Message) bool { return !m.Deleted && (m.SenderID == nil || *m.SenderID != viewer) }
	msgs, _, err := r.run(ctx, sess, scan{chatID: chatID, viewer: viewer, bound: afterID, start: start, need: limit, keep: keep})
	if err != nil {
		return 0, err
	}
	return len(msgs), nil
}
