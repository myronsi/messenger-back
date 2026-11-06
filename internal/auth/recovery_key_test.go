package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecoveryKeyEncoding(t *testing.T) {
	seen := map[string]bool{}
	svc := &Service{pepper: []byte(strings.Repeat("p", 32))}
	for range 100 {
		key := NewRecoveryKey()
		normalized, ok := normalizeRecoveryKey(strings.ToLower(key))
		if !ok || len(key) != 39 || seen[key] {
			t.Fatal("invalid or duplicate random key")
		}
		seen[key] = true
		if svc.recoveryKeyHash(normalized) == normalized || len(svc.recoveryKeyHash(normalized)) != 64 {
			t.Fatal("key was not hashed")
		}
	}
	for _, key := range []string{"", strings.Repeat("0", 32), strings.Repeat("A", 129)} {
		if _, ok := normalizeRecoveryKey(key); ok {
			t.Fatal("accepted malformed key")
		}
	}
}

func TestRecoveryKeyResetAndCooldown(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.81")
	old := e.register(t, "recover_alice", "correct horse")
	normalized, _ := normalizeRecoveryKey(old.RecoveryKey)
	row, err := e.store.RecoveryKeys().Get(ctx, old.User.ID)
	if err != nil || row.Hash == nil || *row.Hash != e.svc.recoveryKeyHash(normalized) {
		t.Fatal("registration must persist only the keyed hash", err)
	}
	for _, name := range []string{"unknown_account", "recover_alice"} {
		if _, err := e.svc.RecoverWithKey(ctx, c, name, "invalid", "new password", ""); !errors.Is(err, ErrRecoveryKey) {
			t.Fatalf("possession failure differs: %v", err)
		}
	}
	next, err := e.svc.RecoverWithKey(ctx, c, "recover_alice", old.RecoveryKey, "new password", "")
	if err != nil {
		t.Fatal(err)
	}
	if next.RecoveryKey == "" || next.RecoveryKey == old.RecoveryKey {
		t.Fatal("replacement key missing")
	}
	if _, err = e.svc.Authenticate(ctx, old.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old access session survives: %v", err)
	}
	if _, err = e.svc.RecoverWithKey(ctx, c, "recover_alice", old.RecoveryKey, "other password", ""); !errors.Is(err, ErrRecoveryKey) {
		t.Fatalf("old key reused: %v", err)
	}
	if _, err = e.svc.Login(ctx, c, "recover_alice", "correct horse"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password survives: %v", err)
	}
	if _, err = e.svc.Login(ctx, c, "recover_alice", "new password"); err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.ReplaceRecoveryKey(ctx, c, principal(next), "new password", ""); !errors.Is(err, ErrRecoveryLocked) {
		t.Fatalf("key rotation bypassed delay: %v", err)
	}
	if _, err = e.svc.SetupTwoFactor(ctx, principal(next), "new password"); !errors.Is(err, ErrRecoveryLocked) {
		t.Fatalf("2FA setup bypassed delay: %v", err)
	}
	events, err := e.svc.SecurityEvents(ctx, principal(next))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type == "password_reset" {
			found = true
		}
	}
	if !found {
		t.Fatal("reset audit event missing")
	}
	if _, err = e.store.Pool().Exec(ctx, "UPDATE user_security_settings SET recovery_locked_until=now()-interval '1 second' WHERE user_id=$1", old.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.ReplaceRecoveryKey(ctx, c, principal(next), "new password", ""); err != nil {
		t.Fatal("rotation should work after delay", err)
	}
}

func TestRecoveryKeyRequiresSecondFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.82")
	old := e.register(t, "recover_bob", "correct horse")
	secret, codes := e.enableTwoFactor(t, old, "correct horse")
	login, err := e.svc.Login(ctx, c, "recover_bob", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RecoverWithKey(ctx, c, "recover_bob", old.RecoveryKey, "new password", ""); !errors.Is(err, ErrTwoFactorRequired) {
		t.Fatalf("2FA bypass: %v", err)
	}
	if _, err := e.svc.RecoverWithKey(ctx, c, "recover_bob", old.RecoveryKey, "new password", "000000"); !errors.Is(err, ErrRecoveryKey) {
		t.Fatalf("2FA bypass: %v", err)
	}
	next, err := e.svc.RecoverWithKey(ctx, c, "recover_bob", old.RecoveryKey, "new password", codes[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.RecoverWithKey(ctx, c, "recover_bob", next.RecoveryKey, "another password", codes[0]); !errors.Is(err, ErrRecoveryKey) {
		t.Fatalf("backup code reused: %v", err)
	}
	st, err := e.svc.Security(ctx, principal(next))
	if err != nil || !st.TwoFactorEnabled {
		t.Fatal("recovery disabled 2FA", err)
	}
	e.skip(time.Minute)
	if _, err = e.svc.LoginTwoFactor(ctx, c, login.Challenge, codes[1]); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("pre-reset login challenge survived: %v", err)
	}
	if err = e.svc.DisableTwoFactor(ctx, c, principal(next), "new password", e.totpCode(t, secret)); !errors.Is(err, ErrRecoveryLocked) {
		t.Fatalf("2FA removal bypassed delay: %v", err)
	}
}

func TestExistingUserRecoveryKeyEnrollment(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.84")
	hash, err := e.svc.hasher.Hash(ctx, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	user, err := e.store.Users().Create(ctx, "recover_legacy", "Legacy", hash)
	if err != nil {
		t.Fatal(err)
	}
	p := Principal{UserID: user.ID}
	status, err := e.svc.RecoveryKeyStatus(ctx, p)
	if err != nil || status.Exists {
		t.Fatal("legacy user should need enrollment", err)
	}
	if _, err = e.svc.ReplaceRecoveryKey(ctx, c, p, "wrong password", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("enrollment must verify password", err)
	}
	key, err := e.svc.ReplaceRecoveryKey(ctx, c, p, "correct horse", "")
	if err != nil || key == "" {
		t.Fatal("legacy enrollment failed", err)
	}
	status, err = e.svc.RecoveryKeyStatus(ctx, p)
	if err != nil || !status.Exists {
		t.Fatal("enrollment status missing", err)
	}
}

func TestRecoveryKeyConcurrentReset(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	old := e.register(t, "recover_race", "correct horse")
	results := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.svc.RecoverWithKey(ctx, e.client("10.0.0.83"), "recover_race", old.RecoveryKey, "new password", "")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrRecoveryKey) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("single-use key had %d winners", wins)
	}
}
