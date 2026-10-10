package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Stream names. Work that does not have to happen inside a request is driven by entries on them.
const (
	StreamMessages = "events:messages"
	StreamChats    = "events:chats"
	StreamUsers    = "events:users"
)

// DefaultStreamMaxLen bounds every stream (approximately, MAXLEN ~). Consumers far enough behind to lose
// entries to trimming fall back to the reconciliation jobs.
const DefaultStreamMaxLen = 1_000_000

// Streams appends entries to the event streams.
type Streams struct {
	rdb    Client
	prefix string
	maxLen int64
}

// NewStreams returns the producer under the prefix ("" in production); maxLen 0 means DefaultStreamMaxLen.
func NewStreams(rdb Client, prefix string, maxLen int64) *Streams {
	if maxLen <= 0 {
		maxLen = DefaultStreamMaxLen
	}
	return &Streams{rdb: rdb, prefix: prefix, maxLen: maxLen}
}

// Add appends an entry (field/value pairs) to the stream and returns its id.
func (s *Streams) Add(ctx context.Context, stream string, fields map[string]any) (string, error) {
	id, err := s.rdb.XAdd(ctx, &goredis.XAddArgs{
		Stream: s.prefix + stream, MaxLen: s.maxLen, Approx: true, Values: fields,
	}).Result()
	if err != nil {
		return "", fmt.Errorf("stream add: %w", err)
	}
	return id, nil
}

// Entry is one stream entry handed to a consumer.
type Entry struct {
	ID     string
	Fields map[string]string
	// Deliveries is 1 for a first attempt and one more for every failure before.
	Deliveries int64
}

// Handler processes an entry. It must be idempotent: an entry is delivered at least once, and again after a
// failure or a crash. Returning a RetryLater error hands the entry back without counting a failure.
type Handler func(ctx context.Context, e Entry) error

// RetryLater asks for the entry again after the delay, for work that has a second step later (a deletion's
// grace pass). It does not count as a failure.
type RetryLater struct{ After time.Duration }

func (r RetryLater) Error() string { return "retry later" }

// ConsumerOptions configures a Consumer.
type ConsumerOptions struct {
	Prefix string
	Stream string
	// Group is the purpose (search-indexer, chat-cleanup, ...): every group sees every entry.
	Group string
	// Name identifies this process within the group (the instance id).
	Name    string
	Handler Handler
	// Batch is how many entries are read at once (default 100).
	Batch int64
	// Block is how long a read waits for new entries (default 5s).
	Block time.Duration
	// RetryAfter is how long a failed or abandoned entry waits before it is tried again; after the n-th
	// failure it waits n times as long (default 10s).
	RetryAfter time.Duration
	// MaxFailures moves an entry whose handler failed this often to the dead-letter stream <stream>:dead
	// (default 5).
	MaxFailures int64
	// OnResult is told about every handled entry (metrics). Optional.
	OnResult func(ok, deadLettered bool)
	Log      *slog.Logger
}

// Consumer reads one stream as a member of a consumer group: at-least-once processing, acknowledged only
// after the handler succeeded, failed entries retried with growing delays and moved to a dead-letter stream
// after MaxFailures, and entries of crashed consumers reclaimed (XAUTOCLAIM) once they are idle.
//
// Retries are recorded in a hash <stream>:retry:<group> (entry id -> "not before ms,failures"), removed
// when the entry is acknowledged. A reclaimed entry is counted as failed before its handler runs, so an
// entry whose handler takes the process down still reaches the dead-letter stream. While a batch is being
// handled, its entries are touched regularly so that other consumers do not reclaim them as abandoned.
type Consumer struct {
	rdb goredis.UniversalClient
	o   ConsumerOptions
	// passes counts reclaim passes; every sweepEvery-th pass also sweeps the retry hash and stale consumers.
	passes int
}

// NewConsumer returns a consumer. Call Run.
func NewConsumer(rdb goredis.UniversalClient, o ConsumerOptions) (*Consumer, error) {
	if o.Stream == "" || o.Group == "" || o.Name == "" || o.Handler == nil {
		return nil, errors.New("consumer: stream, group, name and handler are required")
	}
	if o.Batch <= 0 {
		o.Batch = 100
	}
	if o.Block <= 0 {
		o.Block = 5 * time.Second
	}
	if o.RetryAfter <= 0 {
		o.RetryAfter = 10 * time.Second
	}
	if o.MaxFailures <= 0 {
		o.MaxFailures = 5
	}
	if o.OnResult == nil {
		o.OnResult = func(bool, bool) {}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Consumer{rdb: rdb, o: o}, nil
}

func (c *Consumer) stream() string     { return c.o.Prefix + c.o.Stream }
func (c *Consumer) deadLetter() string { return c.stream() + ":dead" }
func (c *Consumer) retryKey() string   { return c.stream() + ":retry:" + c.o.Group }

// ensureGroup creates the stream and the group; a new group starts at the beginning of what the stream
// still holds.
func (c *Consumer) ensureGroup(ctx context.Context) error {
	err := c.rdb.XGroupCreateMkStream(ctx, c.stream(), c.o.Group, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("consumer group: %w", err)
	}
	return nil
}

// Run consumes until ctx is done. Errors of Redis are logged and retried.
func (c *Consumer) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := c.ensureGroup(ctx); err != nil {
			c.o.Log.WarnContext(ctx, "consumer group", "error", err, "group", c.o.Group)
			sleep(ctx, backoff)
			continue
		}
		break
	}
	nextClaim := time.Now()
	for ctx.Err() == nil {
		if !time.Now().Before(nextClaim) {
			if err := c.reclaim(ctx); err != nil && ctx.Err() == nil {
				c.o.Log.WarnContext(ctx, "reclaim entries", "error", err, "group", c.o.Group)
			}
			nextClaim = time.Now().Add(c.o.RetryAfter / 2)
		}
		err := c.readNew(ctx)
		if err != nil && ctx.Err() == nil {
			if strings.HasPrefix(err.Error(), "NOGROUP") {
				_ = c.ensureGroup(ctx) // the stream was deleted or Redis lost it
			}
			c.o.Log.WarnContext(ctx, "read entries", "error", err, "group", c.o.Group)
			sleep(ctx, backoff)
			backoff = min(2*backoff, 30*time.Second)
			continue
		}
		backoff = time.Second
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// work is a claimed entry and the failures recorded for it.
type work struct {
	entry    Entry
	failures int64
}

// readNew reads entries never delivered to the group and handles them.
func (c *Consumer) readNew(ctx context.Context) error {
	// The read blocks on the server, so it gets a deadline of its own (the client's timeout skips it).
	rctx, cancel := context.WithTimeout(ctx, c.o.Block+5*time.Second)
	res, err := c.rdb.XReadGroup(rctx, &goredis.XReadGroupArgs{
		Group: c.o.Group, Consumer: c.o.Name, Streams: []string{c.stream(), ">"}, Count: c.o.Batch, Block: c.o.Block,
	}).Result()
	cancel()
	if errors.Is(err, goredis.Nil) {
		return nil
	}
	if err != nil {
		return err
	}
	var batch []work
	for _, s := range res {
		for _, m := range s.Messages {
			batch = append(batch, work{entry: Entry{ID: m.ID, Fields: stringFields(m.Values), Deliveries: 1}})
		}
	}
	c.handleBatch(ctx, batch, false)
	return nil
}

// maxClaimPages bounds one reclaim pass; the next pass starts from the beginning again.
const maxClaimPages = 20

// sweepEvery is how many reclaim passes go by between sweeps of the retry hash and of stale consumers.
const sweepEvery = 120

// staleConsumer is how long a consumer without pending entries may be idle before a sweep removes it (each
// start of a worker joins the groups under a new name).
const staleConsumer = 24 * time.Hour

// reclaim takes over entries that were delivered but not acknowledged for at least RetryAfter: failures of
// this consumer, entries handed back with RetryLater, and entries of consumers that crashed.
func (c *Consumer) reclaim(ctx context.Context) error {
	if c.passes%sweepEvery == 0 {
		c.sweep(ctx)
	}
	c.passes++
	start := "0-0"
	for range maxClaimPages {
		msgs, next, deleted, err := c.rdb.XAutoClaimWithDeleted(ctx, &goredis.XAutoClaimArgs{
			Stream: c.stream(), Group: c.o.Group, Consumer: c.o.Name, MinIdle: c.o.RetryAfter, Start: start, Count: c.o.Batch,
		}).Result()
		if err != nil {
			return err
		}
		if len(deleted) > 0 {
			// Trimmed from the stream before anyone handled them; Redis dropped them from the pending list.
			c.o.Log.ErrorContext(ctx, "pending events were trimmed before they were handled", "group", c.o.Group, "count", len(deleted))
			_ = c.rdb.HDel(ctx, c.retryKey(), deleted...).Err()
			for range deleted {
				c.o.OnResult(false, true)
			}
		}
		if len(msgs) > 0 {
			ids := make([]string, len(msgs))
			for i, m := range msgs {
				ids[i] = m.ID
			}
			states, err := c.retryStates(ctx, ids)
			if err != nil {
				return err
			}
			batch := make([]work, 0, len(msgs))
			for i, m := range msgs {
				st := states[i]
				// Claimed before it is due: it stays pending (its idle time starts over) and comes back later.
				if time.Now().Before(st.notBefore) {
					continue
				}
				batch = append(batch, work{entry: Entry{ID: m.ID, Fields: stringFields(m.Values), Deliveries: st.failures + 1}, failures: st.failures})
			}
			c.handleBatch(ctx, batch, true)
		}
		// An empty page does not mean the end: XAUTOCLAIM scans a bounded part of the pending list per call.
		if next == "0-0" || next == "" || ctx.Err() != nil {
			return nil
		}
		start = next
	}
	return nil
}

// handleBatch handles the entries one after another while a heartbeat keeps the unhandled ones claimed.
// For reclaimed entries the next failure is recorded before the handler runs.
func (c *Consumer) handleBatch(ctx context.Context, batch []work, reclaimed bool) {
	if len(batch) == 0 {
		return
	}
	var mu sync.Mutex
	open := make(map[string]bool, len(batch))
	for _, w := range batch {
		open[w.entry.ID] = true
	}
	hbCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(c.o.RetryAfter / 3)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				mu.Lock()
				ids := make([]string, 0, len(open))
				for id := range open {
					ids = append(ids, id)
				}
				mu.Unlock()
				if err := c.touch(hbCtx, ids); err != nil && hbCtx.Err() == nil {
					c.o.Log.WarnContext(hbCtx, "keep entries claimed", "error", err, "group", c.o.Group)
				}
			}
		}
	}()
	defer func() {
		stop()
		<-done
	}()
	for _, w := range batch {
		if ctx.Err() != nil {
			return // shutting down: the rest stays pending and is reclaimed later
		}
		switch {
		case !reclaimed:
			c.handle(ctx, w.entry, w.failures)
		case w.failures >= c.o.MaxFailures:
			// Its handler kept stopping before it could report back (the process crashed or was killed).
			c.toDeadLetter(ctx, w.entry, errors.New("the handler did not finish (the process stopped)"))
		case c.setRetry(ctx, w.entry.ID, time.Now().Add(time.Duration(w.failures+1)*c.o.RetryAfter), w.failures+1):
			c.handle(ctx, w.entry, w.failures)
		}
		mu.Lock()
		delete(open, w.entry.ID)
		mu.Unlock()
	}
}

// The scripts act on an entry only while this consumer still owns it: another consumer may have reclaimed it,
// or acknowledged it, in the meantime.
var (
	// touchScript resets the idle time of the given entries the consumer (ARGV[2]) still owns.
	touchScript = goredis.NewScript(`
for i = 3, #ARGV do
  if #redis.call('XPENDING', KEYS[1], ARGV[1], ARGV[i], ARGV[i], 1, ARGV[2]) > 0 then
    redis.call('XCLAIM', KEYS[1], ARGV[1], ARGV[2], 0, ARGV[i], 'JUSTID')
  end
end
return 0`)
	// setRetryScript records the retry state (ARGV[4]) of an entry the consumer owns; 0 when it does not.
	setRetryScript = goredis.NewScript(`
if #redis.call('XPENDING', KEYS[1], ARGV[1], ARGV[3], ARGV[3], 1, ARGV[2]) == 0 then return 0 end
redis.call('HSET', KEYS[2], ARGV[3], ARGV[4])
return 1`)
	// sweepScript removes the retry records of entries that are no longer pending in the group.
	sweepScript = goredis.NewScript(`
for i = 2, #ARGV do
  if #redis.call('XPENDING', KEYS[1], ARGV[1], ARGV[i], ARGV[i], 1) == 0 then
    redis.call('HDEL', KEYS[2], ARGV[i])
  end
end
return 0`)
)

func (c *Consumer) touch(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return touchScript.Run(ctx, c.rdb, []string{c.stream()}, append([]any{c.o.Group, c.o.Name}, toAny(ids)...)...).Err()
}

// sweep removes retry records that were left behind (entries trimmed, or acknowledged while a record was
// written) and consumers that have been gone for a day without pending entries.
func (c *Consumer) sweep(ctx context.Context) {
	var cursor uint64
	for {
		kv, next, err := c.rdb.HScan(ctx, c.retryKey(), cursor, "", 200).Result()
		if err != nil {
			c.o.Log.WarnContext(ctx, "sweep retry records", "error", err, "group", c.o.Group)
			break
		}
		ids := make([]any, 0, len(kv)/2)
		for i := 0; i < len(kv); i += 2 {
			ids = append(ids, kv[i])
		}
		if len(ids) > 0 {
			if err := sweepScript.Run(ctx, c.rdb, []string{c.stream(), c.retryKey()}, append([]any{c.o.Group}, ids...)...).Err(); err != nil {
				c.o.Log.WarnContext(ctx, "sweep retry records", "error", err, "group", c.o.Group)
				break
			}
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	consumers, err := c.rdb.XInfoConsumers(ctx, c.stream(), c.o.Group).Result()
	if err != nil {
		c.o.Log.WarnContext(ctx, "list consumers", "error", err, "group", c.o.Group)
		return
	}
	for _, x := range consumers {
		if x.Name != c.o.Name && x.Pending == 0 && x.Idle > staleConsumer {
			_ = c.rdb.XGroupDelConsumer(ctx, c.stream(), c.o.Group, x.Name).Err()
		}
	}
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

type retryState struct {
	notBefore time.Time
	failures  int64
}

func (c *Consumer) retryStates(ctx context.Context, ids []string) ([]retryState, error) {
	vals, err := c.rdb.HMGet(ctx, c.retryKey(), ids...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]retryState, len(ids))
	for i, v := range vals {
		s, ok := v.(string)
		if !ok {
			continue // no record: an entry of a crashed consumer, due now
		}
		at, fails, _ := strings.Cut(s, ",")
		ms, err1 := strconv.ParseInt(at, 10, 64)
		n, err2 := strconv.ParseInt(fails, 10, 64)
		if err1 == nil && err2 == nil {
			out[i] = retryState{notBefore: time.UnixMilli(ms), failures: n}
		}
	}
	return out, nil
}

// settle detaches bookkeeping from shutdown, so work that finished is still recorded while the consumer stops.
func settle(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// setRetry records the retry state; false when the entry is no longer this consumer's (or Redis failed).
func (c *Consumer) setRetry(ctx context.Context, id string, notBefore time.Time, failures int64) bool {
	ctx, cancel := settle(ctx)
	defer cancel()
	v := strconv.FormatInt(notBefore.UnixMilli(), 10) + "," + strconv.FormatInt(failures, 10)
	n, err := setRetryScript.Run(ctx, c.rdb, []string{c.stream(), c.retryKey()}, c.o.Group, c.o.Name, id, v).Int()
	if err != nil {
		c.o.Log.WarnContext(ctx, "record retry", "error", err, "group", c.o.Group)
		return false
	}
	return n == 1
}

// run calls the handler and turns a panic into an error, so one bad entry cannot take the worker down.
func (c *Consumer) run(ctx context.Context, e Entry) (err error) {
	defer func() {
		if r := recover(); r != nil {
			c.o.Log.ErrorContext(ctx, "event handler panicked", "group", c.o.Group, "entry", e.ID, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("handler panicked: %v", r)
		}
	}()
	return c.o.Handler(ctx, e)
}

// handle runs the handler on an entry that failed `failures` times before.
func (c *Consumer) handle(ctx context.Context, e Entry, failures int64) {
	err := c.run(ctx, e)
	if err == nil {
		c.ack(ctx, e.ID)
		c.o.OnResult(true, false)
		return
	}
	if ctx.Err() != nil {
		return // shutting down: the entry stays pending and is reclaimed later
	}
	var later RetryLater
	if errors.As(err, &later) {
		c.setRetry(ctx, e.ID, time.Now().Add(later.After), failures)
		return
	}
	failures++
	if failures >= c.o.MaxFailures {
		c.toDeadLetter(ctx, e, err)
		return
	}
	c.o.Log.WarnContext(ctx, "event handler failed", "group", c.o.Group, "entry", e.ID, "failures", failures, "error", err)
	c.setRetry(ctx, e.ID, time.Now().Add(time.Duration(failures)*c.o.RetryAfter), failures)
	c.o.OnResult(false, false)
}

func (c *Consumer) ack(ctx context.Context, id string) {
	ctx, cancel := settle(ctx)
	defer cancel()
	pipe := c.rdb.TxPipeline()
	pipe.XAck(ctx, c.stream(), c.o.Group, id)
	pipe.HDel(ctx, c.retryKey(), id)
	if _, err := pipe.Exec(ctx); err != nil {
		// Not acknowledged: the entry comes back and the idempotent handler runs again.
		c.o.Log.WarnContext(ctx, "acknowledge entry", "error", err, "group", c.o.Group)
	}
}

// toDeadLetter parks an entry that keeps failing in <stream>:dead with the group and the last error, then
// acknowledges it, so it no longer blocks or burns retries.
func (c *Consumer) toDeadLetter(ctx context.Context, e Entry, cause error) {
	sctx, cancel := settle(ctx)
	defer cancel()
	fields := make(map[string]any, len(e.Fields)+3)
	for k, v := range e.Fields {
		fields[k] = v
	}
	fields["dead_group"], fields["dead_entry"], fields["dead_error"] = c.o.Group, e.ID, truncate(cause.Error(), 500)
	if err := c.rdb.XAdd(sctx, &goredis.XAddArgs{Stream: c.deadLetter(), MaxLen: 100_000, Approx: true, Values: fields}).Err(); err != nil {
		c.o.Log.ErrorContext(ctx, "dead-letter entry", "error", err, "group", c.o.Group)
		return
	}
	c.ack(ctx, e.ID)
	c.o.Log.ErrorContext(ctx, "event moved to the dead-letter stream", "group", c.o.Group, "entry", e.ID, "failures", e.Deliveries, "error", cause)
	c.o.OnResult(false, true)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func stringFields(v map[string]any) map[string]string {
	out := make(map[string]string, len(v))
	for k, x := range v {
		if s, ok := x.(string); ok {
			out[k] = s
		} else {
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}
