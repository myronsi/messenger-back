package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newSession(t *testing.T, s *Store, userID int64, hash string) Session {
	t.Helper()
	sess, err := s.Sessions().Create(context.Background(), NewSession{
		UserID: userID, RefreshTokenHash: hash, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func rotateReq(presented, next string, grace time.Duration) RotateRequest {
	return RotateRequest{
		PresentedHash: presented,
		NewHash:       next,
		NewExpiresAt:  func(days int) time.Time { return time.Now().Add(time.Duration(days) * 24 * time.Hour) },
		ReuseGrace:    grace,
	}
}

func TestSessionRotation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	sess := newSession(t, s, u.ID, "h1")

	res, err := s.Sessions().Rotate(ctx, rotateReq("h1", "h2", 0))
	if err != nil || res.Outcome != RotateOK || res.Session.ID != sess.ID {
		t.Fatalf("rotate: %+v %v", res, err)
	}
	// The default duration applies when the account never set one.
	if d := time.Until(res.Session.ExpiresAt); d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Fatalf("expiry %v is not about 90 days away", d)
	}

	// The new token works, and rotating keeps working in a chain.
	if res, err = s.Sessions().Rotate(ctx, rotateReq("h2", "h3", 0)); err != nil || res.Outcome != RotateOK {
		t.Fatalf("second rotate: %+v %v", res, err)
	}

	// An unknown token changes nothing.
	if res, err = s.Sessions().Rotate(ctx, rotateReq("nope", "x", 0)); err != nil || res.Outcome != RotateInvalid {
		t.Fatalf("unknown: %+v %v", res, err)
	}
	if got, _ := s.Sessions().Get(ctx, sess.ID); got.RevokedAt != nil {
		t.Fatal("an unknown token revoked the session")
	}

	// Presenting a token that was already rotated out is reuse: the session is revoked, so even the
	// current token stops working.
	if res, err = s.Sessions().Rotate(ctx, rotateReq("h2", "h4", 0)); err != nil || res.Outcome != RotateReuse || res.Session.ID != sess.ID {
		t.Fatalf("reuse: %+v %v", res, err)
	}
	if got, _ := s.Sessions().Get(ctx, sess.ID); got.RevokedAt == nil {
		t.Fatal("reuse did not revoke the session")
	}
	if res, err = s.Sessions().Rotate(ctx, rotateReq("h3", "h5", 0)); err != nil || res.Outcome != RotateInvalid {
		t.Fatalf("current token of a revoked session: %+v %v", res, err)
	}
	// A second reuse of the old token is just invalid: the event is reported once.
	if res, err = s.Sessions().Rotate(ctx, rotateReq("h2", "h6", 0)); err != nil || res.Outcome != RotateInvalid {
		t.Fatalf("repeated reuse: %+v %v", res, err)
	}
}

func TestSessionRotationGraceAndExpiry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	newSession(t, s, u.ID, "h1")

	if res, err := s.Sessions().Rotate(ctx, rotateReq("h1", "h2", time.Minute)); err != nil || res.Outcome != RotateOK {
		t.Fatalf("rotate: %+v %v", res, err)
	}
	// Within the grace window the stale token is refused without touching the session.
	res, err := s.Sessions().Rotate(ctx, rotateReq("h1", "h3", time.Minute))
	if err != nil || res.Outcome != RotateInvalid {
		t.Fatalf("stale token in grace: %+v %v", res, err)
	}
	if res, err = s.Sessions().Rotate(ctx, rotateReq("h2", "h3", time.Minute)); err != nil || res.Outcome != RotateOK {
		t.Fatalf("current token after a graceful stale one: %+v %v", res, err)
	}

	// An expired session cannot be refreshed.
	if _, err := s.Pool().Exec(ctx, `UPDATE user_sessions SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if res, err = s.Sessions().Rotate(ctx, rotateReq("h3", "h4", 0)); err != nil || res.Outcome != RotateInvalid {
		t.Fatalf("expired: %+v %v", res, err)
	}
}

func TestSessionRotationUsesAccountDuration(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	newSession(t, s, u.ID, "h1")
	if err := s.SecuritySettings().SetSessionDuration(ctx, u.ID, 30); err != nil {
		t.Fatal(err)
	}
	res, err := s.Sessions().Rotate(ctx, rotateReq("h1", "h2", 0))
	if err != nil || res.Outcome != RotateOK {
		t.Fatal(res, err)
	}
	if d := time.Until(res.Session.ExpiresAt); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Fatalf("expiry %v is not about 30 days away", d)
	}
}

func TestConcurrentRotationRotatesOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	newSession(t, s, u.ID, "h1")

	const n = 6
	outcomes := make([]RotateOutcome, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.Sessions().Rotate(ctx, rotateReq("h1", "next-"+string(rune('a'+i)), time.Minute))
			if err != nil {
				t.Error(err)
			}
			outcomes[i] = res.Outcome
		}()
	}
	wg.Wait()
	ok := 0
	for _, o := range outcomes {
		switch o {
		case RotateOK:
			ok++
		case RotateReuse:
			t.Fatal("a racing refresh was treated as theft")
		}
	}
	if ok != 1 {
		t.Fatalf("%d rotations succeeded, want exactly one", ok)
	}
}

func TestSessionListAndRevoke(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, b := mustUser(t, s, "alice"), mustUser(t, s, "bob")
	s1, s2, s3 := newSession(t, s, a.ID, "a1"), newSession(t, s, a.ID, "a2"), newSession(t, s, a.ID, "a3")
	sb := newSession(t, s, b.ID, "b1")

	list, err := s.Sessions().ListActive(ctx, a.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	if err := s.Sessions().Revoke(ctx, a.ID, sb.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking another user's session: %v", err)
	}
	if err := s.Sessions().Revoke(ctx, a.ID, s2.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Sessions().Revoke(ctx, a.ID, s2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking twice: %v", err)
	}
	ids, err := s.Sessions().RevokeOthers(ctx, a.ID, s1.ID)
	if err != nil || len(ids) != 1 || ids[0] != s3.ID {
		t.Fatalf("revoke others: %v %v", ids, err)
	}
	list, _ = s.Sessions().ListActive(ctx, a.ID)
	if len(list) != 1 || list[0].ID != s1.ID {
		t.Fatalf("remaining: %+v", list)
	}
	if list, _ = s.Sessions().ListActive(ctx, b.ID); len(list) != 1 {
		t.Fatal("another user's sessions were touched")
	}
	if ids, err = s.Sessions().RevokeAll(ctx, a.ID); err != nil || len(ids) != 1 {
		t.Fatalf("revoke all: %v %v", ids, err)
	}
	if err := s.Sessions().Touch(ctx, s1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("touching a revoked session: %v", err)
	}
	if err := s.Sessions().Touch(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sessions().Get(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
}

func TestSessionForUnknownUser(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Sessions().Create(context.Background(), NewSession{UserID: 999, RefreshTokenHash: "x", ExpiresAt: time.Now().Add(time.Hour)})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestSecuritySettingsAndTwoFactor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	repo := s.SecuritySettings()

	// A user without a row gets defaults.
	st, err := repo.Get(ctx, u.ID)
	if err != nil || st.SessionDurationDays != DefaultSessionDays || st.TwoFactorEnabled {
		t.Fatalf("defaults: %+v %v", st, err)
	}
	if err := repo.SetSessionDuration(ctx, u.ID, 365); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSessionDuration(ctx, u.ID, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero days: %v", err)
	}

	if err := repo.ConfirmTwoFactor(ctx, u.ID, "sealed", []string{"c1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("confirm without setup: %v", err)
	}
	if err := repo.BeginTwoFactor(ctx, u.ID, "sealed-1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.BeginTwoFactor(ctx, u.ID, "sealed-2"); err != nil { // a second setup replaces the first
		t.Fatal(err)
	}
	if err := repo.ConfirmTwoFactor(ctx, u.ID, "sealed-1", []string{"c1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("confirming a replaced secret: %v", err)
	}
	if err := repo.ConfirmTwoFactor(ctx, u.ID, "sealed-2", []string{"c1", "c2", "c3"}); err != nil {
		t.Fatal(err)
	}
	st, _ = repo.Get(ctx, u.ID)
	if !st.TwoFactorEnabled || st.TwoFactorSecret == nil || *st.TwoFactorSecret != "sealed-2" || st.PendingTwoFactorSecret != nil || st.SessionDurationDays != 365 {
		t.Fatalf("after confirm: %+v", st)
	}
	if err := repo.BeginTwoFactor(ctx, u.ID, "sealed-3"); !errors.Is(err, ErrConflict) {
		t.Fatalf("setup while enabled: %v", err)
	}
	if n, _ := repo.CountRecoveryCodes(ctx, u.ID); n != 3 {
		t.Fatalf("codes: %d", n)
	}

	if ok, err := repo.UseRecoveryCode(ctx, u.ID, "c2"); err != nil || !ok {
		t.Fatalf("use: %v %v", ok, err)
	}
	if ok, _ := repo.UseRecoveryCode(ctx, u.ID, "c2"); ok {
		t.Fatal("a recovery code worked twice")
	}
	if ok, _ := repo.UseRecoveryCode(ctx, u.ID, "unknown"); ok {
		t.Fatal("unknown code accepted")
	}
	if n, _ := repo.CountRecoveryCodes(ctx, u.ID); n != 2 {
		t.Fatalf("codes after use: %d", n)
	}

	if err := repo.DisableTwoFactor(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	st, _ = repo.Get(ctx, u.ID)
	if st.TwoFactorEnabled || st.TwoFactorSecret != nil {
		t.Fatalf("after disable: %+v", st)
	}
	if n, _ := repo.CountRecoveryCodes(ctx, u.ID); n != 0 {
		t.Fatal("recovery codes survived disabling")
	}
}

func TestRecoveryCodeUsedOnceUnderConcurrency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	repo := s.SecuritySettings()
	if err := repo.BeginTwoFactor(ctx, u.ID, "p"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConfirmTwoFactor(ctx, u.ID, "p", []string{"code"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := repo.UseRecoveryCode(ctx, u.ID, "code"); err == nil && ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d uses succeeded", wins)
	}
}

func TestRecoveryTokens(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, "alice")
	newSession(t, s, u.ID, "a1")
	newSession(t, s, u.ID, "a2")
	repo := s.RecoveryTokens()

	if err := repo.Issue(ctx, u.ID, "tok", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Issue(ctx, u.ID, "old", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ResetPassword(ctx, "old", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token: %v", err)
	}
	if _, err := repo.ResetPassword(ctx, "unknown", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
	res, err := repo.ResetPassword(ctx, "tok", "new-hash")
	if err != nil || res.UserID != u.ID || len(res.RevokedSessions) != 2 {
		t.Fatalf("reset: %+v %v", res, err)
	}
	if c, _ := s.Users().CredentialsByID(ctx, u.ID); c.PasswordHash != "new-hash" {
		t.Fatal("password was not set")
	}
	if list, _ := s.Sessions().ListActive(ctx, u.ID); len(list) != 0 {
		t.Fatal("sessions survived a password reset")
	}
	if _, err := repo.ResetPassword(ctx, "tok", "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token used twice: %v", err)
	}
	if c, _ := s.Users().CredentialsByID(ctx, u.ID); c.PasswordHash != "new-hash" {
		t.Fatal("a used token changed the password")
	}
	if n, err := repo.DeleteStale(ctx); err != nil || n != 0 {
		t.Fatalf("stale: %d %v", n, err)
	}
}

func TestRegisterAndPasswordChange(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u, err := s.Users().Register(ctx, "alice", "Alice", "h0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Users().Register(ctx, "ALICE", "Other", "h"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM user_security_settings WHERE user_id = $1`, u.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("settings row: %d %v", n, err)
	}

	keep := newSession(t, s, u.ID, "k")
	newSession(t, s, u.ID, "o")

	if ok, err := s.Users().RehashPassword(ctx, u.ID, "wrong", "h1"); err != nil || ok {
		t.Fatalf("rehash with a stale hash: %v %v", ok, err)
	}
	if ok, err := s.Users().RehashPassword(ctx, u.ID, "h0", "h1"); err != nil || !ok {
		t.Fatalf("rehash: %v %v", ok, err)
	}
	if _, err := s.Users().ChangePassword(ctx, u.ID, "h0", "h2", keep.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("change against a stale hash: %v", err)
	}
	revoked, err := s.Users().ChangePassword(ctx, u.ID, "h1", "h2", keep.ID)
	if err != nil || len(revoked) != 1 {
		t.Fatalf("change: %v %v", revoked, err)
	}
	list, _ := s.Sessions().ListActive(ctx, u.ID)
	if len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("sessions after the change: %+v", list)
	}
	if c, _ := s.Users().CredentialsByID(ctx, u.ID); c.PasswordHash != "h2" {
		t.Fatal("password not changed")
	}
}

func TestDeleteAccountWithSessionsAndTwoFactor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u, _ := s.Users().Register(ctx, "alice", "Alice", "h")
	newSession(t, s, u.ID, "a1")
	if err := s.SecuritySettings().BeginTwoFactor(ctx, u.ID, "p"); err != nil {
		t.Fatal(err)
	}
	if err := s.SecuritySettings().ConfirmTwoFactor(ctx, u.ID, "p", []string{"c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoveryTokens().Issue(ctx, u.ID, "t", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Users().DeleteAccount(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
}
