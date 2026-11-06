package auth

import (
	"context"
	"testing"

	"github.com/myronsi/messenger-back/internal/geoip"
	"github.com/myronsi/messenger-back/internal/geoip/geoiptest"
)

func city(l *geoip.Location) string {
	if l == nil {
		return "<nil>"
	}
	out := ""
	for _, p := range []*string{l.City, l.CountryCode, l.Country} {
		if p == nil {
			out += "|-"
		} else {
			out += "|" + *p
		}
	}
	return out
}

func TestLocationSnapshots(t *testing.T) {
	e := newEnv(t)
	e.svc.geo = geoiptest.Default(t)
	ctx := context.Background()
	berlin, paris, private := e.client("203.0.113.9"), e.client("198.51.100.20"), e.client("10.0.0.5")

	ts, err := e.svc.Register(ctx, berlin, "geo_owner", "Geo Owner", "correct horse", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Login(ctx, private, "geo_owner", "correct horse"); err != nil {
		t.Fatal(err)
	}
	sessions, err := e.svc.Sessions(ctx, principal(ts))
	if err != nil || len(sessions) != 2 {
		t.Fatal(err, len(sessions))
	}
	for _, s := range sessions {
		want := "<nil>"
		if s.ID == ts.SessionID {
			want = "|Berlin|DE|Germany"
		}
		if got := city(s.Location); got != want {
			t.Fatalf("session %s location = %s, want %s", s.ID, got, want)
		}
	}

	next, err := e.svc.Refresh(ctx, paris, ts.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.store.Sessions().Get(ctx, next.SessionID)
	if err != nil || city(got.Location) != "|Paris|FR|France" {
		t.Fatalf("refreshed location = %s %v", city(got.Location), err)
	}
	if next, err = e.svc.Refresh(ctx, private, next.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if got, err = e.store.Sessions().Get(ctx, next.SessionID); err != nil || got.Location != nil {
		t.Fatalf("a private address must clear the location: %s %v", city(got.Location), err)
	}

	events, err := e.svc.SecurityEvents(ctx, principal(ts))
	if err != nil || len(events) == 0 {
		t.Fatal(err, len(events))
	}
	byType := map[string]string{}
	for _, ev := range events {
		byType[ev.Type] = city(ev.Location)
	}
	if byType["register"] != "|Berlin|DE|Germany" || byType["login"] != "<nil>" {
		t.Fatalf("event locations = %v", byType)
	}

	var notice RecoveryRequestNotice
	e.svc.onRecoveryRequest = func(_ context.Context, _ int64, n RecoveryRequestNotice) { notice = n }
	r, err := e.svc.StartDeviceRecovery(ctx, e.client("2001:db8::7"), "geo_owner")
	if err != nil {
		t.Fatal(err)
	}
	if city(notice.Location) != "|-|NL|Netherlands" {
		t.Fatalf("notice location = %s", city(notice.Location))
	}
	stored, err := e.store.RecoveryRequests().Get(ctx, e.svc.recoveryHash(r.ID))
	if err != nil || city(stored.Location) != "|-|NL|Netherlands" {
		t.Fatalf("stored recovery location = %s %v", city(stored.Location), err)
	}
}

func TestNoLocationWithoutDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ts, err := e.svc.Register(ctx, e.client("203.0.113.9"), "geo_none", "Geo None", "correct horse", nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := e.svc.Sessions(ctx, principal(ts))
	if err != nil || len(sessions) != 1 || sessions[0].Location != nil {
		t.Fatalf("sessions = %+v %v", sessions, err)
	}
	var notice RecoveryRequestNotice
	e.svc.onRecoveryRequest = func(_ context.Context, _ int64, n RecoveryRequestNotice) { notice = n }
	if _, err := e.svc.StartDeviceRecovery(ctx, e.client("203.0.113.9"), "geo_none"); err != nil {
		t.Fatal(err)
	}
	if notice.ID == "" || notice.Location != nil {
		t.Fatalf("notice = %+v", notice)
	}
}
