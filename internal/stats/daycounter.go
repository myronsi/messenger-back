package stats

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	FlushEvery   = 10 * time.Second
	flushTimeout = 5 * time.Second
)

type Sink interface {
	Add(ctx context.Context, counts []postgres.MessageCount) error
}

type key struct {
	day string
	typ string
}

type tally struct {
	messages    int64
	attachments int64
}

type DayCounter struct {
	sink    Sink
	log     *slog.Logger
	mu      sync.Mutex
	pending map[key]tally
}

func NewDayCounter(sink Sink, log *slog.Logger) *DayCounter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &DayCounter{sink: sink, log: log, pending: map[key]tally{}}
}

func (c *DayCounter) Message(typ string, attachments int, at time.Time) {
	k := key{day: at.UTC().Format(time.DateOnly), typ: typ}
	c.mu.Lock()
	t := c.pending[k]
	t.messages++
	t.attachments += int64(attachments)
	c.pending[k] = t
	c.mu.Unlock()
}

func (c *DayCounter) Flush(ctx context.Context) error {
	c.mu.Lock()
	taken := c.pending
	c.pending = map[key]tally{}
	c.mu.Unlock()
	if len(taken) == 0 {
		return nil
	}
	counts := make([]postgres.MessageCount, 0, len(taken))
	for k, t := range taken {
		day, _ := time.Parse(time.DateOnly, k.day)
		counts = append(counts, postgres.MessageCount{Day: day, Type: k.typ, Messages: t.messages, Attachments: t.attachments})
	}
	if err := c.sink.Add(ctx, counts); err != nil {
		c.mu.Lock()
		for k, t := range taken {
			p := c.pending[k]
			p.messages += t.messages
			p.attachments += t.attachments
			c.pending[k] = p
		}
		c.mu.Unlock()
		return err
	}
	return nil
}

func (c *DayCounter) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
			if err := c.Flush(final); err != nil {
				c.log.ErrorContext(final, "message day counts lost at shutdown", "err", err)
			}
			cancel()
			return
		case <-t.C:
			if err := c.Flush(ctx); err != nil && ctx.Err() == nil {
				c.log.WarnContext(ctx, "message day counts not saved, retrying", "err", err)
			}
		}
	}
}
