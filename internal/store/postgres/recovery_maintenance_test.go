package postgres

import (
	"context"
	"testing"
)

func TestPurgeRecoveryPreservesLiveCapabilitiesAndDrainsBatches(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	for _, query := range []string{
		`INSERT INTO recovery_requests(id_hash,user_id,match_number,choices,user_agent,expires_at) SELECT 'request-'||v,$1,42,'[42,43,44]','test',now()+v*interval '1 hour' FROM (VALUES (-1),(1)) n(v)`,
		`INSERT INTO email_tokens(token_hash,user_id,address,purpose,expires_at) SELECT 'email-'||v,$1,'alice@example.test','verify_email',now()+v*interval '1 hour' FROM (VALUES (-1),(1)) n(v)`,
		`INSERT INTO webauthn_challenges(id_hash,user_id,purpose,payload,expires_at) SELECT 'challenge-'||v,$1,'registration','{}',now()-interval '1 hour' FROM generate_series(1,1001) v`,
		`INSERT INTO webauthn_challenges(id_hash,user_id,purpose,payload,expires_at) VALUES('live-challenge',$1,'registration','{}',now()+interval '1 hour')`,
		`INSERT INTO qr_login_requests(id_hash,poll_hash,scanned_by,expires_at) SELECT 'qr-'||v,'poll',$1,now()+v*interval '1 minute' FROM (VALUES (-1),(1)) n(v)`,
	} {
		if _, err := s.pool.Exec(ctx, query, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO recovery_mail_queue(kind,payload_sealed,created_at) VALUES('verify_email','old',now()-interval '2 days'),('verify_email','live',now())`); err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeRecovery(ctx)
	if err != nil || n != 1005 {
		t.Fatalf("purged %d: %v", n, err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM recovery_requests`, `SELECT count(*) FROM webauthn_challenges`,
		`SELECT count(*) FROM email_tokens`, `SELECT count(*) FROM recovery_mail_queue`, `SELECT count(*) FROM qr_login_requests`,
	} {
		var count int
		if err := s.pool.QueryRow(ctx, query).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s: count=%d, error=%v", query, count, err)
		}
	}
	if n, err := s.PurgeRecovery(ctx); err != nil || n != 0 {
		t.Fatalf("second purge %d: %v", n, err)
	}
}
