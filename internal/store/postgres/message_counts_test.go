package postgres

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestMessageCountsAddUp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	repo := s.MessageCounts()
	day := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 11, 19, 23, 59, 0, 0, time.FixedZone("EST", -5*3600))
	if err := repo.Add(ctx, []MessageCount{
		{Day: day, Type: "text", Messages: 3},
		{Day: day, Type: "file", Messages: 1, Attachments: 4},
		{Day: day, Type: "text", Messages: 2},
		{Day: late, Type: "text", Messages: 7},
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if err := repo.Add(ctx, []MessageCount{{Day: day, Type: "voice", Messages: 1, Attachments: 1}, {Day: day, Type: "text", Messages: 1}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := repo.On(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	want := []MessageCount{
		{Day: day, Type: "file", Messages: 1, Attachments: 4},
		{Day: day, Type: "text", Messages: 11},
		{Day: day, Type: "voice", Messages: 6, Attachments: 6},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if !got[i].Day.Equal(want[i].Day) || got[i].Type != want[i].Type || got[i].Messages != want[i].Messages || got[i].Attachments != want[i].Attachments {
			t.Fatalf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	next, err := repo.On(ctx, day.AddDate(0, 0, 1))
	if err != nil || len(next) != 1 || next[0].Messages != 7 {
		t.Fatalf("the next UTC day: %+v %v", next, err)
	}
	if err := repo.Add(ctx, []MessageCount{{Day: day, Type: "system", Messages: 1}}); err == nil {
		t.Fatal("system messages are not counted")
	}
}
