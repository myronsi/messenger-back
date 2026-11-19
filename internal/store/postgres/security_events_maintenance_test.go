package postgres

import (
	"context"
	"testing"
)

func TestPurgeSecurityEventsKeepsTheLastYearAndDrainsBatches(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	if _, err := s.pool.Exec(ctx, `INSERT INTO user_security_events(user_id,event_type,ip_address,created_at)
		SELECT $1,'login','192.0.2.1',now()-interval '400 days' FROM generate_series(1,1001)`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO user_security_events(user_id,event_type,ip_address,created_at)
		VALUES ($1,'login','192.0.2.2',now()-interval '364 days'),($1,'password_changed',NULL,now())`, u.ID); err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeSecurityEvents(ctx, SecurityEventRetention)
	if err != nil || n != 1001 {
		t.Fatalf("purged %d: %v", n, err)
	}
	var left int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM user_security_events WHERE user_id=$1`, u.ID).Scan(&left); err != nil || left != 2 {
		t.Fatalf("left %d: %v", left, err)
	}
	if n, err := s.PurgeSecurityEvents(ctx, SecurityEventRetention); err != nil || n != 0 {
		t.Fatalf("second purge %d: %v", n, err)
	}
}
