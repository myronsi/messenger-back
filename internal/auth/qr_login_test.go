package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/geoip/geoiptest"
)

func qrStatus(t *testing.T, e *env, c Client, start QRLoginStart) QRLoginPoll {
	t.Helper()
	res, err := e.svc.PollQRLogin(context.Background(), c, start.ID, start.PollToken)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestQRLoginFullFlow(t *testing.T) {
	e := newEnv(t)
	e.svc.geo = geoiptest.Default(t)
	e.svc.recoveryWebURL = "https://messenger.example.com/"
	ctx := context.Background()
	newDevice := Client{IP: e.client("203.0.113.9").IP, UserAgent: "New\x00 Device/1.0"}
	owner := e.register(t, "qr_owner", "correct horse")
	other := e.register(t, "qr_other", "correct horse")
	type notice struct {
		uid  int64
		kind string
		ua   string
	}
	var notices []notice
	e.svc.onSecurityEvent = func(_ context.Context, uid int64, kind string, c Client) {
		notices = append(notices, notice{uid, kind, c.UserAgent})
	}

	start, err := e.svc.StartQRLogin(ctx, newDevice)
	if err != nil {
		t.Fatal(err)
	}
	if len(start.ID) != 43 || len(start.PollToken) != 43 || start.ID == start.PollToken {
		t.Fatalf("tokens: %q %q", start.ID, start.PollToken)
	}
	if start.URL != "https://messenger.example.com/qr/"+start.ID {
		t.Fatalf("url = %s", start.URL)
	}
	if d := time.Until(start.ExpiresAt); d < time.Minute || d > 2*time.Minute+time.Second {
		t.Fatalf("expiry in %s", d)
	}
	var plain int
	if err = e.store.Pool().QueryRow(ctx, `SELECT count(*) FROM qr_login_requests WHERE id_hash=$1 OR poll_hash=$2 OR id_hash=$2 OR poll_hash=$1`, start.ID, start.PollToken).Scan(&plain); err != nil || plain != 0 {
		t.Fatal("capabilities stored in plaintext", plain, err)
	}
	if got := qrStatus(t, e, newDevice, start); got.Status != "pending" || got.Tokens != nil {
		t.Fatalf("before scan: %+v", got)
	}

	info, err := e.svc.ScanQRLogin(ctx, principal(owner), start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != "scanned" || info.Device == nil || *info.Device != "New Device/1.0" || city(info.Location) != "|Berlin|DE|Germany" || info.ID != start.ID {
		t.Fatalf("scan info: %+v %s", info, city(info.Location))
	}
	if _, err = e.svc.ScanQRLogin(ctx, principal(other), start.ID); !errors.Is(err, ErrQRLoginNotFound) {
		t.Fatal("request bound to the first scanner is visible to another account", err)
	}
	if err = e.svc.ApproveQRLogin(ctx, e.client("10.0.0.1"), principal(other), start.ID, ""); !errors.Is(err, ErrQRLoginNotFound) {
		t.Fatal("another account approved", err)
	}
	if got := qrStatus(t, e, newDevice, start); got.Status != "scanned" {
		t.Fatalf("after scan: %+v", got)
	}

	if err = e.svc.ApproveQRLogin(ctx, e.client("10.0.0.1"), principal(owner), start.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.ApproveQRLogin(ctx, e.client("10.0.0.1"), principal(owner), start.ID, ""); !errors.Is(err, ErrQRLoginDecided) {
		t.Fatal("approved twice", err)
	}
	if err = e.svc.DenyQRLogin(ctx, principal(owner), start.ID); !errors.Is(err, ErrQRLoginDecided) {
		t.Fatal("denied after approval", err)
	}

	done := qrStatus(t, e, newDevice, start)
	if done.Status != "approved" || done.Tokens == nil || done.Tokens.RefreshToken == "" || done.Tokens.User.ID != owner.User.ID {
		t.Fatalf("approved poll: %+v", done)
	}
	p, err := e.svc.Authenticate(ctx, done.Tokens.AccessToken)
	if err != nil || p.UserID != owner.User.ID || p.SessionID == owner.SessionID {
		t.Fatal("new session unusable", p, err)
	}
	sess, err := e.store.Sessions().Get(ctx, done.Tokens.SessionID)
	if err != nil || sess.UserAgent == nil || *sess.UserAgent != "New Device/1.0" || city(sess.Location) != "|Berlin|DE|Germany" {
		t.Fatalf("session metadata: %+v %v", sess, err)
	}
	if _, err = e.svc.Refresh(ctx, newDevice, done.Tokens.RefreshToken); err != nil {
		t.Fatal("refresh token of the QR session", err)
	}

	if again := qrStatus(t, e, newDevice, start); again.Status != "expired" || again.Tokens != nil {
		t.Fatalf("second poll after completion: %+v", again)
	}
	if _, err = e.svc.ScanQRLogin(ctx, principal(owner), start.ID); !errors.Is(err, ErrQRLoginNotFound) {
		t.Fatal("used request still visible", err)
	}
	if err = e.svc.ApproveQRLogin(ctx, e.client("10.0.0.1"), principal(owner), start.ID, ""); !errors.Is(err, ErrQRLoginNotFound) {
		t.Fatal("used request approvable", err)
	}

	events, err := e.svc.SecurityEvents(ctx, principal(owner))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, ev := range events {
		switch ev.Type {
		case "qr_login_approved", "qr_login":
			if ev.UserAgent == nil || *ev.UserAgent != "New Device/1.0" || city(ev.Location) != "|Berlin|DE|Germany" {
				t.Fatalf("%s event: %+v", ev.Type, ev)
			}
			found[ev.Type] = true
		}
	}
	if !found["qr_login_approved"] || !found["qr_login"] {
		t.Fatalf("events: %v", found)
	}
	approvedNotice := false
	for _, n := range notices {
		if n.kind == "qr_login_approved" {
			approvedNotice = n.uid == owner.User.ID && n.ua == "New Device/1.0"
		}
	}
	if !approvedNotice {
		t.Fatalf("approval notice: %+v", notices)
	}
}

func TestQRLoginApproveWithoutScanAndDeny(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.130")
	owner := e.register(t, "qr_deny", "correct horse")

	direct, err := e.svc.StartQRLogin(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.ApproveQRLogin(ctx, c, principal(owner), direct.ID, ""); err != nil {
		t.Fatal("approval without a prior scan", err)
	}
	if got := qrStatus(t, e, c, direct); got.Status != "approved" || got.Tokens == nil {
		t.Fatalf("poll: %+v", got)
	}

	start, err := e.svc.StartQRLogin(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.ScanQRLogin(ctx, principal(owner), start.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.DenyQRLogin(ctx, principal(owner), start.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := qrStatus(t, e, c, start); got.Status != "denied" || got.Tokens != nil {
			t.Fatalf("poll after deny: %+v", got)
		}
	}
	if info, err := e.svc.ScanQRLogin(ctx, principal(owner), start.ID); err != nil || info.Status != "denied" {
		t.Fatal("denied request status", info, err)
	}
	if err = e.svc.ApproveQRLogin(ctx, c, principal(owner), start.ID, ""); !errors.Is(err, ErrQRLoginDecided) {
		t.Fatal("approved after deny", err)
	}
	if err = e.svc.DenyQRLogin(ctx, principal(owner), start.ID); !errors.Is(err, ErrQRLoginDecided) {
		t.Fatal("denied twice", err)
	}
	if err = e.svc.DenyQRLogin(ctx, principal(owner), strings.Repeat("x", 43)); !errors.Is(err, ErrQRLoginNotFound) {
		t.Fatal("unknown request denied", err)
	}
	events, err := e.svc.SecurityEvents(ctx, principal(owner))
	if err != nil {
		t.Fatal(err)
	}
	denied := 0
	for _, ev := range events {
		if ev.Type == "qr_login_denied" && ev.UserAgent != nil && *ev.UserAgent == "test-agent" {
			denied++
		}
	}
	if denied != 1 {
		t.Fatalf("qr_login_denied events: %d", denied)
	}
}

func TestQRLoginExpiryAndWrongPollToken(t *testing.T) {
	for _, kind := range []string{"database", "clock"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			c := e.client("10.0.0.131")
			owner := e.register(t, "qr_expiry", "correct horse")
			start, err := e.svc.StartQRLogin(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "database" {
				if _, err = e.store.Pool().Exec(ctx, `UPDATE qr_login_requests SET expires_at=now()-interval '1 second'`); err != nil {
					t.Fatal(err)
				}
				if _, err = e.svc.ScanQRLogin(ctx, principal(owner), start.ID); !errors.Is(err, ErrQRLoginNotFound) {
					t.Fatal("expired request scanned", err)
				}
			} else {
				e.skip(QRLoginTTL + time.Second)
			}
			if got := qrStatus(t, e, c, start); got.Status != "expired" {
				t.Fatalf("expired poll: %+v", got)
			}
			if err = e.svc.ApproveQRLogin(ctx, c, principal(owner), start.ID, ""); !errors.Is(err, ErrQRLoginNotFound) {
				t.Fatal("expired request approved", err)
			}
			if err = e.svc.DenyQRLogin(ctx, principal(owner), start.ID); !errors.Is(err, ErrQRLoginNotFound) {
				t.Fatal("expired request denied", err)
			}
		})
	}

	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.132")
	owner := e.register(t, "qr_poll_token", "correct horse")
	start, err := e.svc.StartQRLogin(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.StartQRLogin(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.ApproveQRLogin(ctx, c, principal(owner), start.ID, ""); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "short", second.PollToken, start.ID, strings.Repeat("A", 43)} {
		got, err := e.svc.PollQRLogin(ctx, c, start.ID, token)
		if err != nil || got.Status != "expired" || got.Tokens != nil {
			t.Fatalf("wrong poll token %q: %+v %v", token, got, err)
		}
	}
	for _, id := range []string{"", strings.Repeat("A", 43), strings.Repeat("A", 200)} {
		got, err := e.svc.PollQRLogin(ctx, c, id, start.PollToken)
		if err != nil || got.Status != "expired" {
			t.Fatalf("unknown id %q: %+v %v", id, got, err)
		}
	}
	if got := qrStatus(t, e, c, start); got.Status != "approved" || got.Tokens == nil {
		t.Fatalf("right poll token after wrong ones: %+v", got)
	}
}

func TestQRLoginApprovalAsksForMissingSecondFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.133")
	owner := e.register(t, "qr_tfa", "correct horse")
	secret, codes := e.enableTwoFactor(t, owner, "correct horse")
	for _, code := range []string{"", " "} {
		start, err := e.svc.StartQRLogin(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if err = e.svc.ApproveQRLogin(ctx, c, principal(owner), start.ID, code); !errors.Is(err, ErrTwoFactorRequired) {
			t.Fatalf("missing code %q: %v", code, err)
		}
		if got := qrStatus(t, e, c, start); got.Status != "pending" {
			t.Fatalf("two_factor_required consumed the request: %+v", got)
		}
		if err = e.svc.ApproveQRLogin(ctx, c, principal(owner), start.ID, "000000"); !errors.Is(err, ErrRecoveryKey) {
			t.Fatal("wrong code", err)
		}
		if code == "" {
			err = e.svc.ApproveQRLogin(ctx, c, principal(owner), start.ID, codes[0])
		} else {
			e.skip(30 * time.Second)
			err = e.svc.ApproveQRLogin(ctx, c, principal(owner), start.ID, e.totpCode(t, secret))
		}
		if err != nil {
			t.Fatal("request not approvable after two_factor_required", err)
		}
		if got := qrStatus(t, e, c, start); got.Status != "approved" || got.Tokens == nil {
			t.Fatalf("poll: %+v", got)
		}
	}
	start, err := e.svc.StartQRLogin(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.ApproveQRLogin(ctx, c, principal(owner), start.ID, codes[0]); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("used recovery code accepted", err)
	}
}

func TestQRLoginStaleApprovingSession(t *testing.T) {
	for _, kind := range []string{"idle", "revoked", "expired", "revoked-before-poll", "password-changed-before-poll"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			c := e.client("10.0.0.134")
			owner := e.register(t, "qr_stale", "correct horse")
			start, err := e.svc.StartQRLogin(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			var stale string
			switch kind {
			case "idle":
				stale = `UPDATE user_sessions SET last_active_at=now()-interval '31 days' WHERE id=$1`
			case "revoked":
				stale = `UPDATE user_sessions SET revoked_at=now() WHERE id=$1`
			case "expired":
				stale = `UPDATE user_sessions SET expires_at=now()-interval '1 second' WHERE id=$1`
			}
			if stale != "" {
				if _, err = e.store.Pool().Exec(ctx, stale, owner.SessionID); err != nil {
					t.Fatal(err)
				}
				if err = e.svc.ApproveQRLogin(ctx, c, principal(owner), start.ID, ""); !errors.Is(err, ErrApprovalSessionStale) {
					t.Fatal("stale session approved", err)
				}
				if got := qrStatus(t, e, c, start); got.Status != "pending" {
					t.Fatalf("refused approval changed the request: %+v", got)
				}
				return
			}
			if err = e.svc.ApproveQRLogin(ctx, c, principal(owner), start.ID, ""); err != nil {
				t.Fatal(err)
			}
			if kind == "revoked-before-poll" {
				err = e.svc.Logout(ctx, c, principal(owner))
			} else {
				err = e.svc.ChangePassword(ctx, c, principal(owner), "correct horse", "another horse")
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := qrStatus(t, e, c, start); got.Status != "expired" || got.Tokens != nil {
				t.Fatalf("approval survived %s: %+v", kind, got)
			}
		})
	}
}

func TestQRLoginRateLimits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.135")
	var start QRLoginStart
	var err error
	for i := range RuleQRLoginIP.Limit {
		if start, err = e.svc.StartQRLogin(ctx, c); err != nil {
			t.Fatal(i, err)
		}
	}
	var limited *RateLimitError
	if _, err = e.svc.StartQRLogin(ctx, c); !errors.As(err, &limited) {
		t.Fatal("create not limited", err)
	}
	if _, err = e.svc.StartQRLogin(ctx, e.client("10.0.0.136")); err != nil {
		t.Fatal("limit is not per address", err)
	}
	for i := range RuleQRLoginPollIP.Limit {
		if _, err = e.svc.PollQRLogin(ctx, c, start.ID, start.PollToken); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err = e.svc.PollQRLogin(ctx, c, start.ID, start.PollToken); !errors.As(err, &limited) {
		t.Fatal("poll not limited", err)
	}
}
