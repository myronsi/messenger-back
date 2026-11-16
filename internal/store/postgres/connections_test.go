package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestConnections(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u, other := mustUser(t, s, "connector"), mustUser(t, s, "bystander")
	link := "https://github.com/octo"
	gh, err := s.Connections().Connect(ctx, u.ID, ConnectedAccount{Provider: "github", ExternalID: "42", DisplayName: "octo", ProfileURL: &link})
	if err != nil || gh.Position != 0 {
		t.Fatalf("connect %+v %v", gh, err)
	}
	site, err := s.Connections().Connect(ctx, u.ID, ConnectedAccount{Provider: "website", ExternalID: "example.org", DisplayName: "example.org"})
	if err != nil || site.Position != 1 || site.ProfileURL != nil {
		t.Fatalf("second %+v %v", site, err)
	}
	again, err := s.Connections().Connect(ctx, u.ID, ConnectedAccount{Provider: "github", ExternalID: "42", DisplayName: "octocat", ProfileURL: &link})
	if err != nil || again.ID != gh.ID || again.DisplayName != "octocat" || again.Position != 0 {
		t.Fatalf("reconnecting refreshes the row: %+v %v", again, err)
	}
	list, err := s.Connections().Reorder(ctx, u.ID, []int64{site.ID, gh.ID})
	if err != nil || len(list) != 2 || list[0].ID != site.ID {
		t.Fatalf("reorder %+v %v", list, err)
	}
	for _, bad := range [][]int64{{site.ID}, {site.ID, site.ID}, {site.ID, 999999}} {
		if _, err := s.Connections().Reorder(ctx, u.ID, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("reorder %v: %v", bad, err)
		}
	}
	if err := s.Connections().Delete(ctx, other.ID, gh.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's connection: %v", err)
	}
	if err := s.Connections().Delete(ctx, u.ID, gh.ID); err != nil {
		t.Fatal(err)
	}
	for i := range MaxConnections - 1 {
		if _, err := s.Connections().Connect(ctx, u.ID, ConnectedAccount{Provider: "website", ExternalID: fmt.Sprintf("s%d.example", i), DisplayName: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Connections().Connect(ctx, u.ID, ConnectedAccount{Provider: "website", ExternalID: "one-too-many.example", DisplayName: "x"}); !errors.Is(err, ErrConnectionLimit) {
		t.Fatalf("16th: %v", err)
	}
	if _, err := s.Connections().Connect(ctx, u.ID, ConnectedAccount{Provider: "website", ExternalID: "example.org", DisplayName: "renamed"}); err != nil {
		t.Fatalf("refreshing an existing one works at the limit: %v", err)
	}
	bad := "http://insecure.example"
	if _, err := s.Connections().Connect(ctx, other.ID, ConnectedAccount{Provider: "website", ExternalID: "x", DisplayName: "x", ProfileURL: &bad}); err == nil {
		t.Fatal("the schema accepts only https profile links")
	}
}
