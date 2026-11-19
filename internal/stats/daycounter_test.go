package stats_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/stats"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type sink struct {
	mu    sync.Mutex
	fail  bool
	calls int
	saved map[string]postgres.MessageCount
}

func newSink() *sink { return &sink{saved: map[string]postgres.MessageCount{}} }

func (s *sink) Add(_ context.Context, counts []postgres.MessageCount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.fail {
		return errors.New("postgres is away")
	}
	for _, c := range counts {
		k := c.Day.Format(time.DateOnly) + "/" + c.Type
		got := s.saved[k]
		got.Day, got.Type = c.Day, c.Type
		got.Messages += c.Messages
		got.Attachments += c.Attachments
		s.saved[k] = got
	}
	return nil
}

func (s *sink) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.saved))
	for k, c := range s.saved {
		out = append(out, k+"="+strconv.FormatInt(c.Messages, 10)+","+strconv.FormatInt(c.Attachments, 10))
	}
	slices.Sort(out)
	return out
}

func TestDayCounterSumsPerUTCDayAndType(t *testing.T) {
	s := newSink()
	c := stats.NewDayCounter(s, nil)
	berlin := time.FixedZone("CET", 3600)
	c.Message("text", 0, time.Date(2026, 11, 18, 23, 59, 0, 0, time.UTC))
	c.Message("text", 0, time.Date(2026, 11, 19, 0, 30, 0, 0, berlin))
	c.Message("text", 0, time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC))
	c.Message("file", 3, time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC))
	c.Message("file", 1, time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC))
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-11-18/text=2,0", "2026-11-19/file=2,4", "2026-11-19/text=1,0"}
	if got := s.keys(); !slices.Equal(got, want) {
		t.Fatalf("saved %v, want %v", got, want)
	}
	if err := c.Flush(context.Background()); err != nil || s.calls != 1 {
		t.Fatalf("an empty flush writes nothing: calls %d, err %v", s.calls, err)
	}
}

func TestDayCounterKeepsCountsWhenSavingFails(t *testing.T) {
	s := newSink()
	s.fail = true
	c := stats.NewDayCounter(s, nil)
	day := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	c.Message("voice", 1, day)
	if err := c.Flush(context.Background()); err == nil {
		t.Fatal("flush reported success while the sink failed")
	}
	c.Message("voice", 1, day)
	s.fail = false
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.keys(); !slices.Equal(got, []string{"2026-11-19/voice=2,2"}) {
		t.Fatalf("saved %v", got)
	}
}

func TestDayCounterRunFlushesOnStop(t *testing.T) {
	s := newSink()
	c := stats.NewDayCounter(s, nil)
	c.Message("sticker", 0, time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Run(ctx, time.Hour)
		close(done)
	}()
	cancel()
	<-done
	if got := s.keys(); !slices.Equal(got, []string{"2026-11-19/sticker=1,0"}) {
		t.Fatalf("saved %v", got)
	}
}

func TestDayCounterIsSafeForConcurrentSends(t *testing.T) {
	s := newSink()
	c := stats.NewDayCounter(s, nil)
	day := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 250 {
				c.Message("text", 0, day)
			}
			_ = c.Flush(context.Background())
		})
	}
	wg.Wait()
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.keys(); !slices.Equal(got, []string{"2026-11-19/text=2000,0"}) {
		t.Fatalf("saved %v", got)
	}
}
