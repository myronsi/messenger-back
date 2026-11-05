package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func TestPasskeyOriginConfiguration(t *testing.T) {
	if key, err := NewPasskeys("", nil); err != nil || key != nil {
		t.Fatal("disabled configuration", err)
	}
	for _, test := range []struct {
		rp, origin string
		ok         bool
	}{
		{"example.com", "https://chat.example.com", true}, {"localhost", "http://localhost:4173", true},
		{"example.com", "http://example.com", false}, {"example.com", "https://attacker.test", false},
		{"example.com", "https://example.com/callback", false}, {"example.com", "https://example.com?x=1", false},
	} {
		t.Run(test.origin, func(t *testing.T) {
			_, err := NewPasskeys(test.rp, []string{test.origin})
			if (err == nil) != test.ok {
				t.Fatal("unexpected origin validation", err)
			}
		})
	}
}

func TestPasskeyOptionsDoNotConsumePasswordLoginBudget(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.register(t, "passkey_budget", "correct horse")
	c := e.client("10.0.0.96")
	var err error
	e.svc.passkeys, err = NewPasskeys("localhost", []string{"http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	for range RulePasskeyBegin.Limit {
		if _, err = e.svc.BeginPasskeyLogin(ctx, c, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = e.svc.BeginPasskeyLogin(ctx, c, ""); err == nil {
		t.Fatal("passkey options were not limited")
	}
	if _, err = e.svc.Login(ctx, c, "passkey_budget", "correct horse"); err != nil {
		t.Fatalf("options blocked password login: %v", err)
	}
}

func TestPasskeyChallengesAreSessionBoundSingleUseAndPrivate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.95")
	owner := e.register(t, "passkey_owner", "correct horse")
	var err error
	e.svc.passkeys, err = NewPasskeys("localhost", []string{"http://localhost:4173"})
	if err != nil {
		t.Fatal(err)
	}
	options, err := e.svc.BeginPasskeyRegistration(ctx, c, principal(owner), "correct horse", "", "Test device")
	if err != nil {
		t.Fatal(err)
	}
	if options.ChallengeID == "" || options.PublicKey == nil {
		t.Fatal("missing WebAuthn options")
	}
	var rawCount int
	if err = e.store.Pool().QueryRow(ctx, `SELECT count(*) FROM webauthn_challenges WHERE id_hash=$1`, options.ChallengeID).Scan(&rawCount); err != nil || rawCount != 0 {
		t.Fatal("challenge capability persisted unhashed", err)
	}
	other, err := e.svc.Login(ctx, c, owner.User.Username, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.FinishPasskeyRegistration(ctx, c, principal(other.Tokens), options.ChallengeID, json.RawMessage(`{}`)); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatal("challenge not bound to originating session", err)
	}
	if err = e.svc.FinishPasskeyRegistration(ctx, c, principal(owner), options.ChallengeID, json.RawMessage(`{}`)); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("malformed credential accepted", err)
	}
	if err = e.svc.FinishPasskeyRegistration(ctx, c, principal(owner), options.ChallengeID, json.RawMessage(`{}`)); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatal("challenge replayed", err)
	}
}

func TestPasskeyAuthenticationChallengeExpiryAndUserVerification(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.96")
	var err error
	e.svc.passkeys, err = NewPasskeys("localhost", []string{"http://localhost:4173"})
	if err != nil {
		t.Fatal(err)
	}
	options, err := e.svc.BeginPasskeyLogin(ctx, c, "not_a_real_account")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(options.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"userVerification":"required"`) {
		t.Fatal("passkey login must require verified user presence")
	}
	if _, err = e.store.Pool().Exec(ctx, `UPDATE webauthn_challenges SET expires_at=now()-interval '1 second' WHERE id_hash=$1`, e.svc.recoveryHash(options.ChallengeID)); err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.FinishPasskeyLogin(ctx, c, options.ChallengeID, json.RawMessage(`{}`)); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("expired challenge accepted", err)
	}
}

func TestPasskeyLastRemovalRequiresPasswordAndHonoursDelay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.97")
	owner := e.register(t, "passkey_remove", "correct horse")
	proof, err := e.svc.recoveryChangeProof(ctx, principal(owner), "correct horse", "")
	if err != nil {
		t.Fatal(err)
	}
	id := []byte{1, 2, 3}
	if err = e.store.Passkeys().Add(ctx, postgres.Passkey{ID: id, UserID: owner.User.ID, Credential: "sealed-test-placeholder", Name: "Test"}, owner.SessionID, proof); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.DeletePasskey(ctx, c, principal(owner), "AQID", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("last passkey removed without password", err)
	}
	if _, err = e.store.Pool().Exec(ctx, `UPDATE user_security_settings SET recovery_locked_until=now()+interval '1 hour' WHERE user_id=$1`, owner.User.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.svc.DeletePasskey(ctx, c, principal(owner), "AQID", "correct horse"); !errors.Is(err, ErrRecoveryLocked) {
		t.Fatal("security delay bypassed", err)
	}
}
