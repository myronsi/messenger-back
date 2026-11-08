package auth

import (
	"context"
	"errors"
	"strings"
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
	if len(notice.ID) < 32 || notice.ID == r.ID || len(notice.Choices) != 3 || r.Match < 10 || r.Match > 99 {
		t.Fatal("number matching notice missing or reuses the requester capability")
	}
	var plain int
	if err = e.store.Pool().QueryRow(ctx, `SELECT count(*) FROM recovery_requests WHERE id_hash=$1`, r.ID).Scan(&plain); err != nil || plain != 0 {
		t.Fatal("capability stored in plaintext", err)
	}
	if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(other), notice.ID, RecoveryApproval{Match: r.Match}, false); !errors.Is(err, ErrRecoveryRequestNotFound) {
		t.Fatal("foreign user approved", err)
	}
	if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), r.ID, RecoveryApproval{Match: r.Match}, false); !errors.Is(err, ErrRecoveryRequestNotFound) {
		t.Fatal("the requester capability approved its own request", err)
	}
	if _, err = e.svc.CompleteDeviceRecovery(ctx, c, r.ID, "brand new password"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("pending request completed", err)
	}
	if status, err := e.svc.DecideDeviceRecovery(ctx, c, principal(owner), notice.ID, RecoveryApproval{Match: r.Match}, false); err != nil || status != "approved" {
		t.Fatal(status, err)
	}
	if status, err := e.svc.DeviceRecoveryStatus(ctx, c, notice.ID); err != nil || status != "expired" {
		t.Fatal("the approval handle polls the request", status, err)
	}
	if _, err = e.svc.CompleteDeviceRecovery(ctx, c, notice.ID, "attacker password"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("the broadcast approval handle completed the reset", err)
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
			notices := 0
			approval := ""
			e.svc.onRecoveryRequest = func(_ context.Context, _ int64, n RecoveryRequestNotice) {
				notices++
				if approval == "" {
					approval = n.ID
				}
			}
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
				status, err := e.svc.DecideDeviceRecovery(ctx, c, principal(owner), approval, RecoveryApproval{Match: wrong}, kind == "deny")
				if err != nil || status != "denied" {
					t.Fatal(status, err)
				}
				fake, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username)
				if err != nil {
					t.Fatal(err)
				}
				if status, err = e.svc.DeviceRecoveryStatus(ctx, c, fake.ID); err != nil || status != "pending" || notices != 1 {
					t.Fatal("deny did not block new notifications or the blocked request is distinguishable", status, notices, err)
				}
			case "expired":
				_, err = e.store.Pool().Exec(ctx, `UPDATE recovery_requests SET expires_at=now()-interval '1 second' WHERE id_hash=$1`, e.svc.recoveryHash(r.ID))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), approval, RecoveryApproval{Match: r.Match}, false); !errors.Is(err, ErrRecoveryRequestNotFound) {
					t.Fatal("expired request approved", err)
				}
			case "stale-session":
				_, err = e.store.Pool().Exec(ctx, `UPDATE user_sessions SET last_active_at=now()-interval '31 days' WHERE id=$1`, owner.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), approval, RecoveryApproval{Match: r.Match}, false); !errors.Is(err, ErrApprovalSessionStale) {
					t.Fatal("old inactive session approved", err)
				}
			case "revoked-session":
				if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), approval, RecoveryApproval{Match: r.Match}, false); err != nil {
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
	approval := ""
	e.svc.onRecoveryRequest = func(_ context.Context, _ int64, n RecoveryRequestNotice) { approval = n.ID }
	r, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), approval, RecoveryApproval{Match: r.Match}, false); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatal("2FA bypassed", err)
	}
	if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), approval, RecoveryApproval{Match: r.Match, Code: codes[0]}, false); err != nil {
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
	notices := 0
	e.svc.onRecoveryRequest = func(context.Context, int64, RecoveryRequestNotice) { notices++ }
	unknown, err := e.svc.StartDeviceRecovery(ctx, c, "nobody_here")
	if err != nil || len(unknown.ID) < 32 || unknown.Match < 10 || !unknown.ExpiresAt.After(time.Now()) {
		t.Fatal("unknown account shape differs", unknown, err)
	}
	var requests []RecoveryRequestResult
	for range 4 {
		r, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username)
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, r)
	}
	if notices != 3 {
		t.Fatal("more than three active requests notified", notices)
	}
	capped := requests[3]
	for _, id := range []string{unknown.ID, requests[0].ID, capped.ID} {
		if status, err := e.svc.DeviceRecoveryStatus(ctx, c, id); err != nil || status != "pending" {
			t.Fatal("status polling reveals account existence", status, err)
		}
	}
	for _, r := range []RecoveryRequestResult{unknown, capped} {
		if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), r.ID, RecoveryApproval{Match: r.Match}, false); !errors.Is(err, ErrRecoveryRequestNotFound) {
			t.Fatal("decoy request approved", err)
		}
		if _, err = e.svc.CompleteDeviceRecovery(ctx, c, r.ID, "brand new password"); !errors.Is(err, ErrRecoveryKey) {
			t.Fatal("decoy request completed", err)
		}
		if status, err := e.svc.DeviceRecoveryStatus(ctx, c, r.ID); err != nil || status != "pending" {
			t.Fatal("decoy changed state", status, err)
		}
	}
	e.skip(11 * time.Minute)
	for _, id := range []string{unknown.ID, requests[0].ID, capped.ID} {
		if status, err := e.svc.DeviceRecoveryStatus(ctx, c, id); err != nil || status != "expired" {
			t.Fatal("expiry differs between real and decoy requests", status, err)
		}
	}
}

func TestDeviceRecoveryCancelFreesSlotAndStoresNoSecrets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.95")
	owner := e.register(t, "device_cancel", "correct horse")
	e.enableTwoFactor(t, owner, "correct horse")
	notices := 0
	approval := ""
	e.svc.onRecoveryRequest = func(_ context.Context, _ int64, n RecoveryRequestNotice) { notices++; approval = n.ID }
	var requests []RecoveryRequestResult
	for range 3 {
		r, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username)
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, r)
	}
	if err := e.svc.CancelDeviceRecovery(ctx, c, requests[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CancelDeviceRecovery(ctx, c, NewRecoveryKey()+NewRecoveryKey()); err != nil {
		t.Fatal("cancelling an unknown request is distinguishable", err)
	}
	if status, err := e.svc.DeviceRecoveryStatus(ctx, c, requests[0].ID); err != nil || status != "denied" {
		t.Fatal("cancelled request still pending", status, err)
	}
	if _, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username); err != nil || notices != 4 {
		t.Fatal("a cancelled request still counted towards the cap", notices, err)
	}
	var blocked bool
	if err := e.store.Pool().QueryRow(ctx, `SELECT COALESCE(recovery_requests_blocked_until>now(),false) FROM user_security_settings WHERE user_id=$1`, owner.User.ID).Scan(&blocked); err != nil || blocked {
		t.Fatal("cancelling blocked new requests", blocked, err)
	}
	wrong := 10
	if wrong == requests[1].Match {
		wrong = 11
	}
	if _, err := e.svc.DecideDeviceRecovery(ctx, c, principal(owner), approval, RecoveryApproval{Match: wrong}, true); err != nil {
		t.Fatal(err)
	}
	var proof string
	if err := e.store.Pool().QueryRow(ctx, `SELECT approved_proof::text FROM recovery_requests WHERE approval_hash=$1`, e.svc.recoveryHash(approval)).Scan(&proof); err != nil {
		t.Fatal(err)
	}
	credentials, err := e.store.Users().CredentialsByID(ctx, owner.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := e.store.SecuritySettings().Get(ctx, owner.User.ID)
	if err != nil || settings.TwoFactorSecret == nil {
		t.Fatal(err)
	}
	if strings.Contains(proof, credentials.PasswordHash) || strings.Contains(proof, *settings.TwoFactorSecret) {
		t.Fatal("the stored proof contains the password hash or the second-factor secret")
	}
}
