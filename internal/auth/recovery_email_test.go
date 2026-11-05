package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func recoveryMailToken(t *testing.T, e *env) (string, string) {
	t.Helper()
	ctx := context.Background()
	job, err := e.store.RecoveryEmails().Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var recipient, body string
	err = e.svc.processRecoveryMail(ctx, job, func(_ context.Context, to, subject, text string) error {
		recipient, body = to, text
		if strings.ContainsAny(subject, "\r\n") {
			t.Fatal("unsafe mail subject")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.store.RecoveryEmails().FinishJob(ctx, job.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(body, "?token=")
	if !ok {
		return recipient, ""
	}
	return recipient, strings.Split(after, "\n")[0]
}

func enableEmail(t *testing.T, e *env, owner *TokenSet, code string) string {
	t.Helper()
	ctx := context.Background()
	e.svc.recoveryMailEnabled = true
	e.svc.recoveryWebURL = "http://localhost:4173"
	if err := e.svc.SetRecoveryEmail(ctx, e.client("10.0.0.101"), principal(owner), "owner@example.test", "correct horse", code); err != nil {
		t.Fatal(err)
	}
	address, token := recoveryMailToken(t, e)
	if address != "owner@example.test" || len(token) < 32 {
		t.Fatal("verification message missing")
	}
	if err := e.svc.VerifyRecoveryEmail(ctx, e.client("10.0.0.101"), token); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestRecoveryEmailLifecycleAndPrivacy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.101")
	owner := e.register(t, "email_owner", "correct horse")
	verification := enableEmail(t, e, owner, "")
	if err := e.svc.VerifyRecoveryEmail(ctx, c, verification); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("verification token reused", err)
	}
	if err := e.svc.RequestEmailReset(ctx, c, "OWNER@example.test"); err != nil {
		t.Fatal(err)
	}
	address, token := recoveryMailToken(t, e)
	if address != "owner@example.test" || token == "" {
		t.Fatal("reset email missing")
	}
	var plain int
	if err := e.store.Pool().QueryRow(ctx, `SELECT count(*) FROM email_tokens WHERE token_hash=$1`, token).Scan(&plain); err != nil || plain != 0 {
		t.Fatal("reset token stored plaintext", err)
	}
	next, err := e.svc.ResetWithEmail(ctx, c, token, "brand new password", "")
	if err != nil {
		t.Fatal(err)
	}
	if next.RecoveryKey == "" {
		t.Fatal("replacement key missing")
	}
	if _, err = e.svc.Authenticate(ctx, owner.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("old session survived", err)
	}
	if _, err = e.svc.ResetWithEmail(ctx, c, token, "another password", ""); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("reset token reused", err)
	}
	if err = e.svc.RemoveRecoveryEmail(ctx, c, principal(next), "brand new password", ""); !errors.Is(err, ErrRecoveryLocked) {
		t.Fatal("email removal bypassed delay", err)
	}
}

func TestRecoveryEmailUnknownAccountsAndUnverifiedAddressesDoNotSend(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.102")
	owner := e.register(t, "email_unknown", "correct horse")
	e.svc.recoveryMailEnabled = true
	e.svc.recoveryWebURL = "http://localhost:4173"
	if err := e.svc.RequestEmailReset(ctx, c, "no_such_user"); err != nil {
		t.Fatal(err)
	}
	if address, token := recoveryMailToken(t, e); address != "" || token != "" {
		t.Fatal("unknown account got mail")
	}
	if err := e.svc.SetRecoveryEmail(ctx, c, principal(owner), "unverified@example.test", "correct horse", ""); err != nil {
		t.Fatal(err)
	}
	job, err := e.store.RecoveryEmails().Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(job.Payload, "unverified@example.test") {
		t.Fatal("outbox personal data is not encrypted")
	}
	if err = e.store.RecoveryEmails().FinishJob(ctx, job.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.RequestEmailReset(ctx, c, owner.User.Username); err != nil {
		t.Fatal(err)
	}
	if address, token := recoveryMailToken(t, e); address != "" || token != "" {
		t.Fatal("unverified address got reset mail")
	}
}

func TestRecoveryEmailExpiryAndSecondFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.103")
	owner := e.register(t, "email_factor", "correct horse")
	_, codes := e.enableTwoFactor(t, owner, "correct horse")
	enableEmail(t, e, owner, codes[0])
	if err := e.svc.RequestEmailReset(ctx, c, owner.User.Username); err != nil {
		t.Fatal(err)
	}
	_, token := recoveryMailToken(t, e)
	if _, err := e.svc.ResetWithEmail(ctx, c, token, "brand new password", ""); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("second factor bypassed", err)
	}
	if _, err := e.store.Pool().Exec(ctx, `UPDATE email_tokens SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, e.svc.recoveryHash(token)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ResetWithEmail(ctx, c, token, "brand new password", codes[1]); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("expired token accepted", err)
	}
	if err := e.svc.RequestEmailReset(ctx, c, owner.User.Username); err != nil {
		t.Fatal(err)
	}
	_, token = recoveryMailToken(t, e)
	if _, err := e.svc.ResetWithEmail(ctx, c, token, "brand new password", codes[1]); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryEmailTokensInvalidatedByKeyRecovery(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.104")
	owner := e.register(t, "email_rotate", "correct horse")
	enableEmail(t, e, owner, "")
	if err := e.svc.RequestEmailReset(ctx, c, owner.User.Username); err != nil {
		t.Fatal(err)
	}
	_, token := recoveryMailToken(t, e)
	if _, err := e.svc.RecoverWithKey(ctx, c, owner.User.Username, owner.RecoveryKey, "brand new password", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.RecoveryEmails().Token(ctx, e.svc.recoveryHash(token), "password_reset"); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatal("old email capability survived another recovery", err)
	}
}
