package chats

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/myronsi/messenger-back/internal/moderation"
)

type fakeModeration struct {
	routes   bool
	err      error
	allowErr error
}

func (f fakeModeration) Allow(context.Context, int64, moderation.Action, int64) error {
	return f.allowErr
}

func (f fakeModeration) RoutesToRequests(context.Context, int64) (bool, error) {
	return f.routes, f.err
}

type folder struct{ marked [][2]int64 }

func (f *folder) MarkRequest(_ context.Context, chatID, recipientID int64) error {
	f.marked = append(f.marked, [2]int64{chatID, recipientID})
	return nil
}

func TestLimitedAccountsChatsLandInRequests(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		mod       Moderation
		requester int64
		want      bool
	}{
		{"limited or worse", fakeModeration{routes: true}, 1, true},
		{"good or warned", fakeModeration{}, 1, false},
		{"status unknown", fakeModeration{err: errors.New("down")}, 1, true},
		{"saved messages", fakeModeration{routes: true}, 2, false},
		{"no moderation", nil, 1, false},
	}
	for _, c := range cases {
		f := &folder{}
		s := New(Deps{Moderation: c.mod, Requests: f})
		if got := s.MustBeRequest(ctx, c.requester, 2); got != c.want {
			t.Errorf("%s: must be a request %v, want %v", c.name, got, c.want)
		}
		s.routeRequest(ctx, 10, c.requester, 2)
		if moved := len(f.marked) == 1 && f.marked[0] == [2]int64{10, 2}; moved != c.want {
			t.Errorf("%s: moved to the recipient's requests %v (%v)", c.name, moved, f.marked)
		}
	}
	New(Deps{Moderation: fakeModeration{routes: true}}).routeRequest(ctx, 10, 1, 2)
}

func TestModerationOutageIsUnavailable(t *testing.T) {
	down := fmt.Errorf("%w: redis and postgres", moderation.ErrUnavailable)
	err := New(Deps{Moderation: fakeModeration{allowErr: down}}).allow(context.Background(), 1, moderation.StartDirect, 2)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a moderation outage is %v, want ErrUnavailable", err)
	}
	restricted := &moderation.ErrRestricted{Action: moderation.StartDirect}
	if err := New(Deps{Moderation: fakeModeration{allowErr: restricted}}).allow(context.Background(), 1, moderation.StartDirect, 2); errors.Is(err, ErrUnavailable) {
		t.Fatal("a restriction became unavailable")
	}
}
