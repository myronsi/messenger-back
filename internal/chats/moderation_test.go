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

func TestLimitedAccountsChatsLandInRequests(t *testing.T) {
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
		if got := New(Deps{Moderation: c.mod}).MustBeRequest(context.Background(), c.requester, 2); got != c.want {
			t.Errorf("%s: must be a request %v, want %v", c.name, got, c.want)
		}
	}
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
