package auth

import (
	"context"
	"errors"
	"testing"
)

func TestRecoverWithKeyAsksForMissingSecondFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.120")
	owner := e.register(t, "tfr_key", "correct horse")
	_, codes := e.enableTwoFactor(t, owner, "correct horse")
	for _, name := range []string{"tfr_key", "tfr_unknown"} {
		if _, err := e.svc.RecoverWithKey(ctx, c, name, NewRecoveryKey(), "new password", ""); !errors.Is(err, ErrRecoveryKey) {
			t.Fatalf("invalid key revealed the second factor for %s: %v", name, err)
		}
	}
	if _, err := e.svc.RecoverWithKey(ctx, c, "tfr_key", owner.RecoveryKey, "new password", ""); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatalf("missing code: %v", err)
	}
	if _, err := e.svc.RecoverWithKey(ctx, c, "tfr_key", owner.RecoveryKey, "new password", " "); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatalf("blank code: %v", err)
	}
	if _, err := e.svc.RecoverWithKey(ctx, c, "tfr_key", owner.RecoveryKey, "new password", "000000"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatalf("wrong code: %v", err)
	}
	if _, err := e.svc.RecoverWithKey(ctx, c, "tfr_key", owner.RecoveryKey, "new password", codes[0]); err != nil {
		t.Fatal("key not usable after two_factor_required", err)
	}
}

func TestRecoverWithKeyWithoutTwoFactorNeedsNoCode(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	owner := e.register(t, "tfr_key_plain", "correct horse")
	if _, err := e.svc.RecoverWithKey(ctx, e.client("10.0.0.121"), "tfr_key_plain", owner.RecoveryKey, "new password", ""); err != nil {
		t.Fatal(err)
	}
}

func TestResetWithEmailAsksForMissingSecondFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.122")
	owner := e.register(t, "tfr_email", "correct horse")
	_, codes := e.enableTwoFactor(t, owner, "correct horse")
	enableEmail(t, e, owner, codes[0])
	if err := e.svc.RequestEmailReset(ctx, c, owner.User.Username); err != nil {
		t.Fatal(err)
	}
	_, token := recoveryMailToken(t, e)
	if _, err := e.svc.ResetWithEmail(ctx, c, NewRecoveryKey()+NewRecoveryKey(), "brand new password", ""); !errors.Is(err, ErrRecoveryLinkInvalid) {
		t.Fatal("invalid token revealed the second factor", err)
	}
	if _, err := e.svc.ResetWithEmail(ctx, c, token, "brand new password", ""); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatal("missing code", err)
	}
	if _, err := e.svc.ResetWithEmail(ctx, c, token, "brand new password", "000000"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("wrong code", err)
	}
	if _, err := e.svc.ResetWithEmail(ctx, c, token, "brand new password", codes[1]); err != nil {
		t.Fatal("token not usable after two_factor_required", err)
	}
}

func TestDeviceRecoveryApprovalAsksForMissingSecondFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.123")
	owner := e.register(t, "tfr_device", "correct horse")
	other := e.register(t, "tfr_device_other", "correct horse")
	_, codes := e.enableTwoFactor(t, owner, "correct horse")
	e.enableTwoFactor(t, other, "correct horse")
	approval := ""
	e.svc.onRecoveryRequest = func(_ context.Context, _ int64, n RecoveryRequestNotice) { approval = n.ID }
	r, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(other), approval, r.Match, "correct horse", "", false); !errors.Is(err, ErrRecoveryRequestNotFound) {
		t.Fatal("foreign session revealed the second factor", err)
	}
	if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), approval, r.Match, "correct horse", "", false); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatal("missing code", err)
	}
	if status, err := e.svc.DeviceRecoveryStatus(ctx, c, r.ID); err != nil || status != "pending" {
		t.Fatal("two_factor_required changed the request", status, err)
	}
	if _, err = e.svc.DecideDeviceRecovery(ctx, c, principal(owner), approval, r.Match, "correct horse", "000000", false); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("wrong code", err)
	}
	if status, err := e.svc.DecideDeviceRecovery(ctx, c, principal(owner), approval, r.Match, "correct horse", codes[0], false); err != nil || status != "approved" {
		t.Fatal("request not usable after two_factor_required", status, err)
	}
	if _, err = e.svc.CompleteDeviceRecovery(ctx, c, r.ID, "brand new password"); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceRecoveryWrongNumberStillDeniesWithTwoFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.124")
	owner := e.register(t, "tfr_device_wrong", "correct horse")
	e.enableTwoFactor(t, owner, "correct horse")
	approval := ""
	e.svc.onRecoveryRequest = func(_ context.Context, _ int64, n RecoveryRequestNotice) { approval = n.ID }
	r, err := e.svc.StartDeviceRecovery(ctx, c, owner.User.Username)
	if err != nil {
		t.Fatal(err)
	}
	wrong := 10
	if wrong == r.Match {
		wrong = 11
	}
	if status, err := e.svc.DecideDeviceRecovery(ctx, c, principal(owner), approval, wrong, "", "", false); err != nil || status != "denied" {
		t.Fatal("wrong number must deny, not ask for a code", status, err)
	}
}

func TestDisableTwoFactorAsksForMissingCode(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.125")
	owner := e.register(t, "tfr_disable", "correct horse")
	_, codes := e.enableTwoFactor(t, owner, "correct horse")
	if err := e.svc.DisableTwoFactor(ctx, c, principal(owner), "wrong password", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("wrong password", err)
	}
	if err := e.svc.DisableTwoFactor(ctx, c, principal(owner), "correct horse", ""); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatal("missing code", err)
	}
	if err := e.svc.DisableTwoFactor(ctx, c, principal(owner), "correct horse", "000000"); !errors.Is(err, ErrInvalidTwoFactorCode) {
		t.Fatal("wrong code", err)
	}
	if st, err := e.svc.Security(ctx, principal(owner)); err != nil || !st.TwoFactorEnabled {
		t.Fatal("2FA turned off without a code", err)
	}
	if err := e.svc.DisableTwoFactor(ctx, c, principal(owner), "correct horse", codes[0]); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DisableTwoFactor(ctx, c, principal(owner), "correct horse", ""); !errors.Is(err, ErrTwoFactorNotEnabled) {
		t.Fatal("disabled 2FA must not ask for a code", err)
	}
}

func TestReplaceRecoveryKeyAsksForMissingSecondFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.126")
	owner := e.register(t, "tfr_replace", "correct horse")
	_, codes := e.enableTwoFactor(t, owner, "correct horse")
	if _, err := e.svc.ReplaceRecoveryKey(ctx, c, principal(owner), "wrong password", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("wrong password", err)
	}
	if _, err := e.svc.ReplaceRecoveryKey(ctx, c, principal(owner), "correct horse", ""); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatal("missing code", err)
	}
	if _, err := e.svc.ReplaceRecoveryKey(ctx, c, principal(owner), "correct horse", "000000"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("wrong code", err)
	}
	if _, err := e.svc.RecoverWithKey(ctx, c, "tfr_replace", owner.RecoveryKey, "new password", ""); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatal("two_factor_required rotated the key", err)
	}
	if _, err := e.svc.ReplaceRecoveryKey(ctx, c, principal(owner), "correct horse", codes[0]); err != nil {
		t.Fatal(err)
	}
}

func TestPasskeyRegistrationAsksForMissingSecondFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.127")
	owner := e.register(t, "tfr_passkey", "correct horse")
	_, codes := e.enableTwoFactor(t, owner, "correct horse")
	var err error
	e.svc.passkeys, err = NewPasskeys("localhost", []string{softOrigin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.BeginPasskeyRegistration(ctx, c, principal(owner), "wrong password", "", "Key"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("wrong password", err)
	}
	if _, err = e.svc.BeginPasskeyRegistration(ctx, c, principal(owner), "correct horse", "", "Key"); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatal("missing code", err)
	}
	if _, err = e.svc.BeginPasskeyRegistration(ctx, c, principal(owner), "correct horse", "000000", "Key"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("wrong code", err)
	}
	device := newSoftAuthenticator(t)
	options, err := e.svc.BeginPasskeyRegistration(ctx, c, principal(owner), "correct horse", codes[0], "Key")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.FinishPasskeyRegistration(ctx, c, principal(owner), options.ChallengeID, device.register(options)); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryEmailChangesAskForMissingSecondFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.128")
	owner := e.register(t, "tfr_mail", "correct horse")
	_, codes := e.enableTwoFactor(t, owner, "correct horse")
	e.svc.recoveryMailEnabled = true
	e.svc.recoveryWebURL = "http://localhost:4173"
	p := principal(owner)
	if err := e.svc.SetRecoveryEmail(ctx, c, p, "tfr@example.test", "wrong password", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("wrong password", err)
	}
	if err := e.svc.SetRecoveryEmail(ctx, c, p, "tfr@example.test", "correct horse", ""); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatal("missing code", err)
	}
	if err := e.svc.SetRecoveryEmail(ctx, c, p, "tfr@example.test", "correct horse", "000000"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("wrong code", err)
	}
	if st, err := e.svc.RecoveryEmailStatus(ctx, p); err != nil || st.State != "not_added" {
		t.Fatal("address stored without a code", st, err)
	}
	if err := e.svc.SetRecoveryEmail(ctx, c, p, "tfr@example.test", "correct horse", codes[0]); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemoveRecoveryEmail(ctx, c, p, "wrong password", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("wrong password", err)
	}
	if err := e.svc.RemoveRecoveryEmail(ctx, c, p, "correct horse", ""); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatal("missing code", err)
	}
	if err := e.svc.RemoveRecoveryEmail(ctx, c, p, "correct horse", "000000"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("wrong code", err)
	}
	if err := e.svc.RemoveRecoveryEmail(ctx, c, p, "correct horse", codes[1]); err != nil {
		t.Fatal(err)
	}
}
