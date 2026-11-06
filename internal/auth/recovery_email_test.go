package auth

import (
	"context"
	"errors"
	"fmt"
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
	if _, err := e.svc.ResetWithEmail(ctx, c, token, "brand new password", ""); !errors.Is(err, ErrTwoFactorRequired) {
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

func TestRecoveryEmailUniqueOnlyWhenVerified(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.105")
	first := e.register(t, "email_first", "correct horse")
	second := e.register(t, "email_second", "correct horse")
	third := e.register(t, "email_third", "correct horse")
	e.svc.recoveryMailEnabled = true
	e.svc.recoveryWebURL = "http://localhost:4173"
	for _, owner := range []*TokenSet{first, second} {
		if err := e.svc.SetRecoveryEmail(ctx, c, principal(owner), "Shared@example.test", "correct horse", ""); err != nil {
			t.Fatal("adding an address leaks whether it is used", err)
		}
	}
	_, firstToken := recoveryMailToken(t, e)
	_, secondToken := recoveryMailToken(t, e)
	if firstToken == "" || secondToken == "" {
		t.Fatal("verification mail missing")
	}
	if err := e.svc.VerifyRecoveryEmail(ctx, c, firstToken); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.VerifyRecoveryEmail(ctx, c, secondToken); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("second account verified an address already verified elsewhere", err)
	}
	if status, err := e.svc.RecoveryEmailStatus(ctx, principal(second)); err != nil || status.State != "pending" {
		t.Fatal("conflicting address became verified", status, err)
	}
	if err := e.svc.SetRecoveryEmail(ctx, c, principal(third), "shared@example.test", "correct horse", ""); err != nil {
		t.Fatal("adding a verified address leaks that it is used", err)
	}
	if address, token := recoveryMailToken(t, e); address != "" || token != "" {
		t.Fatal("verification mail sent for an address verified by another account")
	}
}

func TestRecoveryEmailResetLimitedPerAccountAndRotatesTokens(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	owner := e.register(t, "email_reset_limit", "correct horse")
	enableEmail(t, e, owner, "")
	var tokens []string
	for i := range RuleEmailResetUser.Limit + 1 {
		c := e.client(fmt.Sprintf("10.0.1.%d", i+1))
		if err := e.svc.RequestEmailReset(ctx, c, owner.User.Username); err != nil {
			t.Fatal(err)
		}
		_, token := recoveryMailToken(t, e)
		tokens = append(tokens, token)
	}
	for i, token := range tokens[:RuleEmailResetUser.Limit] {
		if token == "" {
			t.Fatal("reset mail missing", i)
		}
	}
	if tokens[RuleEmailResetUser.Limit] != "" {
		t.Fatal("per-account reset limit not applied")
	}
	for _, token := range tokens[:RuleEmailResetUser.Limit-1] {
		if _, err := e.store.RecoveryEmails().Token(ctx, e.svc.recoveryHash(token), "password_reset"); !errors.Is(err, postgres.ErrNotFound) {
			t.Fatal("older reset token survived a new one", err)
		}
	}
	latest := tokens[RuleEmailResetUser.Limit-1]
	if _, err := e.svc.ResetWithEmail(ctx, e.client("10.0.0.106"), latest, "brand new password", ""); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryEmailRetryDoesNotSpendResetLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	owner := e.register(t, "email_reset_retry", "correct horse")
	enableEmail(t, e, owner, "")
	if err := e.svc.RequestEmailReset(ctx, e.client("10.0.2.1"), owner.User.Username); err != nil {
		t.Fatal(err)
	}
	for range RuleEmailResetUser.Limit + 1 {
		job, err := e.store.RecoveryEmails().Claim(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = e.svc.processRecoveryMail(ctx, job, func(context.Context, string, string, string) error { return errors.New("smtp down") })
		if err == nil {
			t.Fatal("a failed send was reported as delivered")
		}
		if err = e.store.RecoveryEmails().FinishJob(ctx, job.ID, false, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, token := recoveryMailToken(t, e); token == "" {
		t.Fatal("retries after a mail server failure used up the reset limit")
	}
}

func TestRecoveryEmailChangeRejectsUsedBackupCodeAndLimitsGuesses(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.3.1")
	owner := e.register(t, "email_backup_code", "correct horse")
	_, codes := e.enableTwoFactor(t, owner, "correct horse")
	e.svc.recoveryMailEnabled = true
	e.svc.recoveryWebURL = "http://localhost:4173"
	if err := e.svc.SetRecoveryEmail(ctx, c, principal(owner), "owner@example.test", "correct horse", codes[0]); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemoveRecoveryEmail(ctx, c, principal(owner), "correct horse", codes[0]); !errors.Is(err, ErrRecoveryKey) {
		t.Fatal("a used backup code was not reported as an invalid code", err)
	}
	var limited *RateLimitError
	for i := range RuleTwoFAUser.Limit + 1 {
		err := e.svc.RemoveRecoveryEmail(ctx, c, principal(owner), "correct horse", "000000")
		if errors.As(err, &limited) {
			if i == 0 {
				t.Fatal("limited before any wrong code")
			}
			return
		}
		if !errors.Is(err, ErrRecoveryKey) {
			t.Fatal(err)
		}
	}
	t.Fatal("wrong second-factor codes are not limited")
}
