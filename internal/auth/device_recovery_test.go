package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeviceRecoveryLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.91")
	owner := e.register(t, "device_owner", "correct horse")
	other := e.register(t, "device_other", "correct horse")
	var notice RecoveryRequestNotice
	e.svc.onRecoveryRequest = func(_ context.Context, uid int64, n RecoveryRequestNotice) {
		if uid != owner.User.ID {
			t.Fatal("wrong recipient")
		}
		notice = n
	}
	r, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username)
	if err != nil {
		t.Fatal(err)
	}
	if notice.ID != r.ID || len(notice.Choices) != 3 || r.Match < 10 || r.Match > 99 {
		t.Fatal("number matching notice missing")
	}
	var plain int
	if err = e.store.Pool().QueryRow(ctx, `SELECT count(*) FROM recovery_requests WHERE id_hash=$1`, r.ID).Scan(&plain); err != nil || plain != 0 {
		t.Fatal("capability stored in plaintext", err)
	}
	if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(other), r.ID, r.Match, "", false); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("foreign user approved", err)
	}
	if _, err = e.svc.CompleteDeviceRecovery(ctx, c, r.ID, "brand new password"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("pending request completed", err)
	}
	if status, err := e.svc.DecideDeviceRecovery(ctx, c, principal(owner), r.ID, r.Match, "", false); err != nil || status != "approved" {
		t.Fatal(status, err)
	}
	next, err := e.svc.CompleteDeviceRecovery(ctx, c, r.ID, "brand new password")
	if err != nil {
		t.Fatal(err)
	}
	if next.RecoveryKey == "" {
		t.Fatal("replacement key missing")
	}
	if _, err = e.svc.Authenticate(ctx, owner.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("old session survived", err)
	}
	if _, err = e.svc.CompleteDeviceRecovery(ctx, c, r.ID, "other password"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("request reused", err)
	}
	if _, err = e.svc.ReplaceRecoveryKey(ctx, c, principal(next), "brand new password", ""); !errors.Is(err, ErrRecoveryLocked) {
		t.Fatal("security delay missing", err)
	}
}

func TestDeviceRecoveryWrongNumberDenyExpiryAndSpam(t *testing.T) {
	for _, kind := range []string{"wrong", "deny", "expired", "stale-session", "revoked-session"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			c := e.client("10.0.0.92")
			owner := e.register(t, "device_cases", "correct horse")
			r, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "wrong", "deny":
				wrong := 10
				if wrong == r.Match {
					wrong = 11
				}
				status, err := e.svc.DecideDeviceRecovery(ctx, c, principal(owner), r.ID, wrong, "", kind == "deny")
				if err != nil || status != "denied" {
					t.Fatal(status, err)
				}
				fake, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username)
				if err != nil {
					t.Fatal(err)
				}
				if status, err = e.svc.DeviceRecoveryStatus(ctx, c, fake.ID); err != nil || status != "expired" {
					t.Fatal("deny did not block new notifications", status, err)
				}
			case "expired":
				_, err = e.store.Pool().Exec(ctx, `UPDATE recovery_requests SET expires_at=now()-interval '1 second' WHERE id_hash=$1`, e.svc.recoveryHash(r.ID))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), r.ID, r.Match, "", false); !errors.Is(err, ErrRecoveryKey) {
					t.Fatal("expired request approved", err)
				}
			case "stale-session":
				_, err = e.store.Pool().Exec(ctx, `UPDATE user_sessions SET last_active_at=now()-interval '31 days' WHERE id=$1`, owner.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), r.ID, r.Match, "", false); !errors.Is(err, ErrRecoveryKey) {
					t.Fatal("old inactive session approved", err)
				}
			case "revoked-session":
				if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), r.ID, r.Match, "", false); err != nil {
					t.Fatal(err)
				}
				if err = e.svc.Logout(ctx, c, principal(owner)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = e.svc.CompleteDeviceRecovery(ctx, c, r.ID, "brand new password"); !errors.Is(err, ErrRecoveryKey) {
				t.Fatal("invalid request completed", err)
			}
		})
	}
}

func TestDeviceRecoveryRequiresSecondFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.93")
	owner := e.register(t, "device_factor", "correct horse")
	_, codes := e.enableTwoFactor(t, owner, "correct horse")
	r, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), r.ID, r.Match, "", false); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("2FA bypassed", err)
	}
	if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), r.ID, r.Match, codes[0], false); err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.CompleteDeviceRecovery(ctx, c, r.ID, "brand new password"); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceRecoveryUnknownAndPendingCap(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.94")
	owner := e.register(t, "device_cap", "correct horse")
	unknown, err := e.svc.StartDeviceRecovery(ctx, c, "nobody_here")
	if err != nil || len(unknown.ID) < 32 || unknown.Match < 10 || !unknown.ExpiresAt.After(time.Now()) {
		t.Fatal("unknown account shape differs", unknown, err)
	}
	for i := 0; i < 4; i++ {
		r, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username)
		if err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			status, err := e.svc.DeviceRecoveryStatus(ctx, c, r.ID)
			if err != nil || status != "expired" {
				t.Fatal("more than three active requests", status, err)
			}
		}
	}
}
