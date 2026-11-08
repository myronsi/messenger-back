package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type approvalFixture struct {
	e        *env
	ctx      context.Context
	c        Client
	owner    *TokenSet
	request  RecoveryRequestResult
	approval string
}

func newApprovalFixture(t *testing.T, name, ip string) *approvalFixture {
	t.Helper()
	f := &approvalFixture{e: newEnv(t), ctx: context.Background()}
	f.c = f.e.client(ip)
	f.owner = f.e.register(t, name, "correct horse")
	f.e.svc.onRecoveryRequest = func(_ context.Context, _ int64, n RecoveryRequestNotice) { f.approval = n.ID }
	f.restart(t)
	return f
}

func (f *approvalFixture) restart(t *testing.T) {
	t.Helper()
	r, err := f.e.svc.StartDeviceRecovery(f.ctx, f.c, f.owner.User.Username)
	if err != nil {
		t.Fatal(err)
	}
	f.request = r
}

func (f *approvalFixture) ageSession(t *testing.T, ts *TokenSet, age time.Duration) {
	t.Helper()
	if _, err := f.e.store.Pool().Exec(f.ctx, `UPDATE user_sessions SET created_at=now()-($2*interval '1 second') WHERE id=$1`, ts.SessionID, age.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func (f *approvalFixture) login(t *testing.T) *TokenSet {
	t.Helper()
	result, err := f.e.svc.Login(f.ctx, f.c, f.owner.User.Username, "correct horse")
	if err != nil || result.Tokens == nil {
		t.Fatal("second sign-in failed", err)
	}
	return result.Tokens
}

func (f *approvalFixture) status(t *testing.T) string {
	t.Helper()
	status, err := f.e.svc.DeviceRecoveryStatus(f.ctx, f.c, f.request.ID)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func (f *approvalFixture) withPasskey(t *testing.T) (*softAuthenticator, []byte) {
	t.Helper()
	var err error
	f.e.svc.passkeys, err = NewPasskeys("localhost", []string{softOrigin})
	if err != nil {
		t.Fatal(err)
	}
	device := newSoftAuthenticator(t)
	options, err := f.e.svc.BeginPasskeyRegistration(f.ctx, f.c, principal(f.owner), "correct horse", "", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.e.svc.FinishPasskeyRegistration(f.ctx, f.c, principal(f.owner), options.ChallengeID, device.register(options)); err != nil {
		t.Fatal(err)
	}
	var handle []byte
	if err = f.e.store.Pool().QueryRow(f.ctx, `SELECT handle FROM user_webauthn_handles WHERE user_id=$1`, f.owner.User.ID).Scan(&handle); err != nil {
		t.Fatal(err)
	}
	return device, handle
}

func (f *approvalFixture) passkeyOptions(t *testing.T, ts *TokenSet) PasskeyOptions {
	t.Helper()
	options, err := f.e.svc.DeviceRecoveryApprovalOptions(f.ctx, f.c, principal(ts), f.approval)
	if err != nil {
		t.Fatal(err)
	}
	if options.Method != "passkey" || options.ChallengeID == "" || options.ApprovableFrom != nil {
		t.Fatal("passkey verification not offered", options)
	}
	return PasskeyOptions{ChallengeID: options.ChallengeID, PublicKey: options.PublicKey}
}

func TestDeviceRecoveryApprovesWithoutCurrentPassword(t *testing.T) {
	f := newApprovalFixture(t, "forgot_password", "10.0.1.1")
	options, err := f.e.svc.DeviceRecoveryApprovalOptions(f.ctx, f.c, principal(f.owner), f.approval)
	if err != nil || options.Method != "session" || options.ApprovableFrom != nil || options.ChallengeID != "" {
		t.Fatal("session verification not offered", options, err)
	}
	if status, err := f.e.svc.DecideDeviceRecovery(f.ctx, f.c, principal(f.owner), f.approval, RecoveryApproval{Match: f.request.Match}, false); err != nil || status != "approved" {
		t.Fatal("approval needed more than the trusted session", status, err)
	}
	next, err := f.e.svc.CompleteDeviceRecovery(f.ctx, f.c, f.request.ID, "brand new password")
	if err != nil {
		t.Fatal(err)
	}
	if next.RecoveryKey == "" {
		t.Fatal("recovery key not rotated")
	}
	if _, err = f.e.svc.Authenticate(f.ctx, f.owner.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("approving session survived the reset", err)
	}
	if result, err := f.e.svc.Login(f.ctx, f.c, f.owner.User.Username, "brand new password"); err != nil || result.Tokens == nil {
		t.Fatal("new password does not sign in", err)
	}
	var verification string
	if err = f.e.store.Pool().QueryRow(f.ctx, `SELECT details->>'verification' FROM user_security_events WHERE user_id=$1 AND event_type='recovery_request_approved'`, f.owner.User.ID).Scan(&verification); err != nil || verification != "session" {
		t.Fatal("approval method not recorded", verification, err)
	}
}

func TestDeviceRecoveryApprovalStillPinsThePassword(t *testing.T) {
	f := newApprovalFixture(t, "pinned_password", "10.0.1.2")
	if status, err := f.e.svc.DecideDeviceRecovery(f.ctx, f.c, principal(f.owner), f.approval, RecoveryApproval{Match: f.request.Match}, false); err != nil || status != "approved" {
		t.Fatal(status, err)
	}
	if _, err := f.e.store.Pool().Exec(f.ctx, `UPDATE users SET password_hash=password_hash||'x' WHERE id=$1`, f.owner.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.svc.CompleteDeviceRecovery(f.ctx, f.c, f.request.ID, "brand new password"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("approval survived a password change", err)
	}
}

func TestDeviceRecoveryNewSessionMustWaitWithoutPasskey(t *testing.T) {
	f := newApprovalFixture(t, "new_session", "10.0.1.3")
	f.e.svc.recoveryApprovalMinSessionAge = 72 * time.Hour
	options, err := f.e.svc.DeviceRecoveryApprovalOptions(f.ctx, f.c, principal(f.owner), f.approval)
	if err != nil || options.Method != "session" || options.ApprovableFrom == nil {
		t.Fatal("waiting time not announced", options, err)
	}
	if wait := time.Until(*options.ApprovableFrom); wait < 71*time.Hour || wait > 72*time.Hour {
		t.Fatal("approvable_from is not 72 hours after sign-in", wait)
	}
	if _, err = f.e.svc.DecideDeviceRecovery(f.ctx, f.c, principal(f.owner), f.approval, RecoveryApproval{Match: f.request.Match}, false); !errors.Is(err, ErrApprovalSessionTooNew) {
		t.Fatal("new session approved", err)
	}
	if got := f.status(t); got != "pending" {
		t.Fatal("rejected approval changed the request", got)
	}
	hash := f.e.svc.recoveryHash(f.request.ID)
	decision := postgres.RecoveryDecision{SessionID: f.owner.SessionID, Match: f.request.Match, MaxAge: time.Hour, MinSessionAge: 72 * time.Hour}
	if _, err = f.e.store.RecoveryRequests().Decide(f.ctx, hash, f.owner.User.ID, decision); !errors.Is(err, postgres.ErrSessionTooNew) {
		t.Fatal("the store accepted a new session", err)
	}
	f.ageSession(t, f.owner, 73*time.Hour)
	if options, err = f.e.svc.DeviceRecoveryApprovalOptions(f.ctx, f.c, principal(f.owner), f.approval); err != nil || options.ApprovableFrom != nil {
		t.Fatal("settled session still waits", options, err)
	}
	if status, err := f.e.svc.DecideDeviceRecovery(f.ctx, f.c, principal(f.owner), f.approval, RecoveryApproval{Match: f.request.Match}, false); err != nil || status != "approved" {
		t.Fatal(status, err)
	}
}

func TestDeviceRecoveryRevokedSessionCannotApprove(t *testing.T) {
	f := newApprovalFixture(t, "revoked_approver", "10.0.1.4")
	second := f.login(t)
	if err := f.e.store.Sessions().Revoke(f.ctx, second.User.ID, second.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.svc.DeviceRecoveryApprovalOptions(f.ctx, f.c, principal(second), f.approval); !errors.Is(err, ErrApprovalSessionStale) {
		t.Fatal("revoked session got approval options", err)
	}
	if _, err := f.e.svc.DecideDeviceRecovery(f.ctx, f.c, principal(second), f.approval, RecoveryApproval{Match: f.request.Match}, false); !errors.Is(err, ErrApprovalSessionStale) {
		t.Fatal("revoked session approved", err)
	}
	if got := f.status(t); got != "pending" {
		t.Fatal("revoked session changed the request", got)
	}
}

func TestDeviceRecoveryApprovalOptionsNeedTheOwnersPendingRequest(t *testing.T) {
	f := newApprovalFixture(t, "options_owner", "10.0.1.5")
	other := f.e.register(t, "options_other", "correct horse")
	if _, err := f.e.svc.DeviceRecoveryApprovalOptions(f.ctx, f.c, principal(other), f.approval); !errors.Is(err, ErrRecoveryRequestNotFound) {
		t.Fatal("another account read the options", err)
	}
	if _, err := f.e.svc.DeviceRecoveryApprovalOptions(f.ctx, f.c, principal(f.owner), f.request.ID); !errors.Is(err, ErrRecoveryRequestNotFound) {
		t.Fatal("the requester capability read the options", err)
	}
	if _, err := f.e.svc.DecideDeviceRecovery(f.ctx, f.c, principal(f.owner), f.approval, RecoveryApproval{}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.svc.DeviceRecoveryApprovalOptions(f.ctx, f.c, principal(f.owner), f.approval); !errors.Is(err, ErrRecoveryRequestNotFound) {
		t.Fatal("a denied request still offers options", err)
	}
}

func TestDeviceRecoveryWithPasskeyNeedsUserVerification(t *testing.T) {
	f := newApprovalFixture(t, "passkey_approver", "10.0.1.6")
	device, handle := f.withPasskey(t)
	f.e.svc.recoveryApprovalMinSessionAge = 72 * time.Hour
	approve := func(ts *TokenSet, options PasskeyOptions, credential []byte) (string, error) {
		return f.e.svc.DecideDeviceRecovery(f.ctx, f.c, principal(ts), f.approval, RecoveryApproval{Match: f.request.Match, ChallengeID: options.ChallengeID, Credential: credential}, false)
	}
	if _, err := f.e.svc.DecideDeviceRecovery(f.ctx, f.c, principal(f.owner), f.approval, RecoveryApproval{Match: f.request.Match}, false); !errors.Is(err, ErrUserVerificationRequired) {
		t.Fatal("approved without the passkey", err)
	}
	options := f.passkeyOptions(t, f.owner)
	if _, err := approve(f.owner, options, device.assertWithFlags(options, handle, 0x01)); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("approved without user verification", err)
	}
	if _, err := approve(f.owner, options, device.assert(options, handle)); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatal("challenge reused", err)
	}
	second := f.login(t)
	options = f.passkeyOptions(t, f.owner)
	if _, err := approve(second, options, device.assert(options, handle)); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatal("challenge of another session accepted", err)
	}
	if got := f.status(t); got != "pending" {
		t.Fatal("failed verification changed the request", got)
	}
	hash := f.e.svc.recoveryHash(f.request.ID)
	decision := postgres.RecoveryDecision{SessionID: f.owner.SessionID, Match: f.request.Match, MaxAge: time.Hour, PasskeysEnabled: true}
	if _, err := f.e.store.RecoveryRequests().Decide(f.ctx, hash, f.owner.User.ID, decision); !errors.Is(err, postgres.ErrVerificationRequired) {
		t.Fatal("the store skipped the passkey", err)
	}
	device.count = 3
	options = f.passkeyOptions(t, f.owner)
	if status, err := approve(f.owner, options, device.assert(options, handle)); err != nil || status != "approved" {
		t.Fatal("verified passkey did not approve a new session", status, err)
	}
	if got := storedSignCount(t, f.e, f.owner.User.ID, device.id); got != 3 {
		t.Fatal("sign count not stored", got)
	}
	var verification string
	if err := f.e.store.Pool().QueryRow(f.ctx, `SELECT details->>'verification' FROM user_security_events WHERE user_id=$1 AND event_type='recovery_request_approved'`, f.owner.User.ID).Scan(&verification); err != nil || verification != "passkey" {
		t.Fatal("approval method not recorded", verification, err)
	}
	if _, err := f.e.svc.CompleteDeviceRecovery(f.ctx, f.c, f.request.ID, "brand new password"); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceRecoveryPasskeyChallengeIsBoundToItsRequest(t *testing.T) {
	f := newApprovalFixture(t, "passkey_binding", "10.0.1.7")
	device, handle := f.withPasskey(t)
	first := f.approval
	options := f.passkeyOptions(t, f.owner)
	f.restart(t)
	if _, err := f.e.svc.DecideDeviceRecovery(f.ctx, f.c, principal(f.owner), f.approval, RecoveryApproval{Match: f.request.Match, ChallengeID: options.ChallengeID, Credential: device.assert(options, handle)}, false); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatal("challenge of another request accepted", err)
	}
	if first == f.approval {
		t.Fatal("restart did not create a new request")
	}
}

func TestDeviceRecoveryPasskeyThenSecondFactor(t *testing.T) {
	f := newApprovalFixture(t, "passkey_2fa", "10.0.1.8")
	_, codes := f.e.enableTwoFactor(t, f.owner, "correct horse")
	var err error
	f.e.svc.passkeys, err = NewPasskeys("localhost", []string{softOrigin})
	if err != nil {
		t.Fatal(err)
	}
	device := newSoftAuthenticator(t)
	registration, err := f.e.svc.BeginPasskeyRegistration(f.ctx, f.c, principal(f.owner), "correct horse", codes[0], "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.e.svc.FinishPasskeyRegistration(f.ctx, f.c, principal(f.owner), registration.ChallengeID, device.register(registration)); err != nil {
		t.Fatal(err)
	}
	var handle []byte
	if err = f.e.store.Pool().QueryRow(f.ctx, `SELECT handle FROM user_webauthn_handles WHERE user_id=$1`, f.owner.User.ID).Scan(&handle); err != nil {
		t.Fatal(err)
	}
	options := f.passkeyOptions(t, f.owner)
	if _, err = f.e.svc.DecideDeviceRecovery(f.ctx, f.c, principal(f.owner), f.approval, RecoveryApproval{Match: f.request.Match, ChallengeID: options.ChallengeID, Credential: device.assert(options, handle)}, false); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatal("second factor skipped after the passkey", err)
	}
	options = f.passkeyOptions(t, f.owner)
	if status, err := f.e.svc.DecideDeviceRecovery(f.ctx, f.c, principal(f.owner), f.approval, RecoveryApproval{Match: f.request.Match, Code: codes[1], ChallengeID: options.ChallengeID, Credential: device.assert(options, handle)}, false); err != nil || status != "approved" {
		t.Fatal(status, err)
	}
}
