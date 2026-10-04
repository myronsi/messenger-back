package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp/totp"
	goredis "github.com/redis/go-redis/v9"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const migrationsDir = "../../migrations/postgres"

// newStore returns a Store on a fresh database with the full schema, or skips the test.
func newStore(t *testing.T) *postgres.Store {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	name := "test_" + hex.EncodeToString(raw)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = conn.Exec(c, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = conn.Close(c)
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name

	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations: %v", err)
	}
	sort.Strings(files)
	mc, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer mc.Close(ctx)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mc.Exec(ctx, string(sql), pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
	}
	s, err := postgres.New(ctx, u.String(), postgres.Options{MaxConns: 8, QueryTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

type env struct {
	svc   *Service
	store *postgres.Store
	clock *atomic.Int64 // unix seconds added to the real time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store := newStore(t)
	rdb, ns := testRedis(t)
	svc, err := NewService(store, rdb, Config{
		JWTSecret:         []byte(strings.Repeat("j", 40)),
		EncryptionKey:     []byte(strings.Repeat("k", 32)),
		RecoveryPepper:    []byte(strings.Repeat("p", 32)),
		SessionCacheTTL:   time.Minute,
		RefreshReuseGrace: 10 * time.Second,
		Namespace:         ns,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{svc: svc, store: store, clock: new(atomic.Int64)}
	svc.now = func() time.Time { return time.Now().Add(time.Duration(e.clock.Load()) * time.Second) }
	return e
}

// skip moves the service clock forward, e.g. to the next TOTP step.
func (e *env) skip(d time.Duration) { e.clock.Add(int64(d / time.Second)) }

func (e *env) client(ip string) Client {
	return Client{IP: netip.MustParseAddr(ip), UserAgent: "test-agent"}
}

func (e *env) register(t *testing.T, name, password string) *TokenSet {
	t.Helper()
	ts, err := e.svc.Register(context.Background(), e.client("10.0.0.1"), name, "Display "+name, password, nil)
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	return ts
}

func principal(ts *TokenSet) Principal { return Principal{UserID: ts.User.ID, SessionID: ts.SessionID} }

func (e *env) enableTwoFactor(t *testing.T, ts *TokenSet, password string) (secret string, codes []string) {
	t.Helper()
	ctx := context.Background()
	setup, err := e.svc.SetupTwoFactor(ctx, principal(ts), password)
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(setup.Secret, e.svc.now())
	if err != nil {
		t.Fatal(err)
	}
	codes, err = e.svc.ConfirmTwoFactor(ctx, e.client("10.0.0.1"), principal(ts), code)
	if err != nil {
		t.Fatal(err)
	}
	return setup.Secret, codes
}

func (e *env) totpCode(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, e.svc.now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestRegisterAndLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ts := e.register(t, "Alice_1", "correct horse")

	if ts.User.Username != "alice_1" {
		t.Fatalf("username is stored in lower case, got %q", ts.User.Username)
	}
	if _, err := e.svc.Authenticate(ctx, ts.AccessToken); err != nil {
		t.Fatalf("fresh access token: %v", err)
	}

	if _, err := e.svc.Register(ctx, e.client("10.0.0.1"), "ALICE_1", "Other", "another pass", nil); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate (case-insensitive) username: got %v", err)
	}
	var ve *ValidationError
	if _, err := e.svc.Register(ctx, e.client("10.0.0.1"), "a b", "x", "correct horse", nil); !errors.As(err, &ve) || ve.Field != "username" {
		t.Fatalf("invalid username: got %v", err)
	}
	if _, err := e.svc.Register(ctx, e.client("10.0.0.1"), "bob", "x", "short", nil); !errors.As(err, &ve) || ve.Field != "password" {
		t.Fatalf("short password: got %v", err)
	}

	res, err := e.svc.Login(ctx, e.client("10.0.0.2"), "  ALICE_1 ", "correct horse")
	if err != nil || res.Tokens == nil {
		t.Fatalf("login: %v %+v", err, res)
	}
	if res.Tokens.SessionID == ts.SessionID {
		t.Fatal("every login creates its own session")
	}
	if _, err := e.svc.Login(ctx, e.client("10.0.0.2"), "alice_1", "wrong password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: got %v", err)
	}
	if _, err := e.svc.Login(ctx, e.client("10.0.0.2"), "nobody", "correct horse"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user must look like a wrong password: got %v", err)
	}
}

func TestLoginUpgradesV1Hash(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, err := e.store.Users().Create(ctx, "legacy", "Legacy", v1Argon2)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.Login(ctx, e.client("10.0.0.2"), "legacy", "correct horse battery"); err != nil {
		t.Fatalf("login with a v1 hash: %v", err)
	}
	creds, err := e.store.Users().CredentialsByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if creds.PasswordHash == v1Argon2 || !strings.HasPrefix(creds.PasswordHash, "$argon2id$") {
		t.Fatalf("hash was not upgraded: %q", creds.PasswordHash)
	}
	if _, err := e.svc.Login(ctx, e.client("10.0.0.2"), "legacy", "correct horse battery"); err != nil {
		t.Fatalf("login after the upgrade: %v", err)
	}

	if _, err := e.store.Users().Create(ctx, "legacy2", "Legacy", v1PBKDF2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Login(ctx, e.client("10.0.0.2"), "legacy2", "legacy-password"); err != nil {
		t.Fatalf("login with a PBKDF2 hash: %v", err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.register(t, "carol", "correct horse")

	for range RuleLoginUser.Limit {
		// Different IPs: only the per-account limit can stop this.
		ip := netip.AddrFrom4([4]byte{10, 1, byte(rand8()), byte(rand8())})
		_, err := e.svc.Login(ctx, Client{IP: ip}, "carol", "wrong password")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("got %v", err)
		}
	}
	var rl *RateLimitError
	_, err := e.svc.Login(ctx, e.client("10.9.9.9"), "carol", "correct horse")
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("the account must be locked after repeated failures, got %v", err)
	}
}

func rand8() int {
	b := make([]byte, 1)
	_, _ = rand.Read(b)
	return int(b[0])
}

func TestRefreshRotationAndReuse(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.3")
	e.svc.grace = 0
	ts := e.register(t, "dave", "correct horse")

	next, err := e.svc.Refresh(ctx, c, ts.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if next.RefreshToken == ts.RefreshToken || next.SessionID != ts.SessionID {
		t.Fatal("refresh rotates the token and keeps the session")
	}
	if _, err := e.svc.Authenticate(ctx, next.AccessToken); err != nil {
		t.Fatal(err)
	}

	// The old token is used again: the session is revoked, including the newest token.
	if _, err := e.svc.Refresh(ctx, c, ts.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("reuse: got %v", err)
	}
	if _, err := e.svc.Refresh(ctx, c, next.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("the session must be dead after reuse, got %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, next.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("access tokens of a revoked session must be refused at once, got %v", err)
	}

	for _, bad := range []string{"", "short", strings.Repeat("x", 64), strings.Repeat("x", 500)} {
		if _, err := e.svc.Refresh(ctx, c, bad); !errors.Is(err, ErrInvalidRefreshToken) {
			t.Fatalf("garbage token %q: got %v", bad, err)
		}
	}
}

func TestRefreshGraceWindowAllowsParallelTabs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.3")
	ts := e.register(t, "erin", "correct horse")

	if _, err := e.svc.Refresh(ctx, c, ts.RefreshToken); err != nil {
		t.Fatal(err)
	}
	// Within the grace window the previous token is refused but does not kill the session.
	if _, err := e.svc.Refresh(ctx, c, ts.RefreshToken); !errors.Is(err, ErrRefreshSuperseded) {
		t.Fatalf("got %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, ts.AccessToken); err != nil {
		t.Fatalf("a refresh race must not sign the user out: %v", err)
	}
}

func TestConfirmationCodeCannotBeReplayedAtLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.7")
	ts := e.register(t, "gina", "correct horse")
	setup, err := e.svc.SetupTwoFactor(ctx, principal(ts), "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	code := e.totpCode(t, setup.Secret)
	if _, err := e.svc.ConfirmTwoFactor(ctx, c, principal(ts), code); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Login(ctx, c, "gina", "correct horse")
	if err != nil || res.Challenge == "" {
		t.Fatalf("login: %v %+v", err, res)
	}
	if _, err := e.svc.LoginTwoFactor(ctx, c, res.Challenge, code); !errors.Is(err, ErrInvalidTwoFactorCode) {
		t.Fatalf("the code that enabled 2FA must not log in: got %v", err)
	}
}

func TestRevocationFailsClosedWhenTheCacheIsDown(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.8")
	ts := e.register(t, "hank", "correct horse")
	other, err := e.svc.Login(ctx, c, "hank", "correct horse")
	if err != nil || other.Tokens == nil {
		t.Fatalf("login: %v", err)
	}

	down := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	defer down.Close()
	e.svc.cache = NewSessionCache(down, "x", time.Minute)
	err = e.svc.RevokeSession(ctx, c, principal(ts), other.Tokens.SessionID)
	if !errors.Is(err, ErrRevocationIncomplete) {
		t.Fatalf("a revocation the cache never saw must be reported, got %v", err)
	}

	// Token reuse revokes the session; a cache that cannot be told is reported there as well.
	e.svc.grace = 0
	if _, err := e.svc.Refresh(ctx, c, ts.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Refresh(ctx, c, ts.RefreshToken); !errors.Is(err, ErrRevocationIncomplete) {
		t.Fatalf("reuse with a broken cache: got %v", err)
	}
}

func TestTwoFactorLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.4")
	ts := e.register(t, "frank", "correct horse")
	secret, codes := e.enableTwoFactor(t, ts, "correct horse")
	if len(codes) != 10 {
		t.Fatalf("got %d recovery codes", len(codes))
	}

	res, err := e.svc.Login(ctx, c, "frank", "correct horse")
	if err != nil || res.Tokens != nil || res.Challenge == "" {
		t.Fatalf("login with 2FA must answer with a challenge: %v %+v", err, res)
	}

	if _, err := e.svc.LoginTwoFactor(ctx, c, res.Challenge, "000000"); !errors.Is(err, ErrInvalidTwoFactorCode) {
		t.Fatalf("wrong code: got %v", err)
	}
	e.skip(time.Minute) // a fresh TOTP step that was not used to confirm
	code := e.totpCode(t, secret)
	got, err := e.svc.LoginTwoFactor(ctx, c, res.Challenge, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, got.AccessToken); err != nil {
		t.Fatal(err)
	}

	// The challenge is single use.
	if _, err := e.svc.LoginTwoFactor(ctx, c, res.Challenge, code); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("challenge reuse: got %v", err)
	}

	// A code cannot be replayed on a new challenge either.
	res2, err := e.svc.Login(ctx, c, "frank", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.LoginTwoFactor(ctx, c, res2.Challenge, code); !errors.Is(err, ErrInvalidTwoFactorCode) {
		t.Fatalf("replayed TOTP code: got %v", err)
	}

	// A recovery code works once.
	if _, err := e.svc.LoginTwoFactor(ctx, c, res2.Challenge, codes[0]); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	res3, err := e.svc.Login(ctx, c, "frank", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.LoginTwoFactor(ctx, c, res3.Challenge, strings.ToLower(codes[0])); !errors.Is(err, ErrInvalidTwoFactorCode) {
		t.Fatalf("a recovery code is single use: got %v", err)
	}
}

func TestTwoFactorChallengeAttemptsAreLimited(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.4")
	ts := e.register(t, "gina", "correct horse")
	secret, _ := e.enableTwoFactor(t, ts, "correct horse")
	res, err := e.svc.Login(ctx, c, "gina", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		_, err = e.svc.LoginTwoFactor(ctx, c, res.Challenge, "000000")
		if !errors.Is(err, ErrInvalidTwoFactorCode) {
			t.Fatalf("got %v", err)
		}
	}
	e.skip(time.Minute)
	if _, err := e.svc.LoginTwoFactor(ctx, c, res.Challenge, e.totpCode(t, secret)); err == nil {
		t.Fatal("a challenge must die after too many wrong codes, even for a right code")
	}
}

func TestTwoFactorSetupRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.5")
	ts := e.register(t, "hank", "correct horse")
	p := principal(ts)

	if _, err := e.svc.SetupTwoFactor(ctx, p, "wrong password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("setup needs the password: %v", err)
	}
	if _, err := e.svc.ConfirmTwoFactor(ctx, c, p, "123456"); !errors.Is(err, ErrNoPendingTwoFactor) {
		t.Fatalf("confirm without setup: %v", err)
	}
	setup, err := e.svc.SetupTwoFactor(ctx, p, "correct horse")
	if err != nil || !strings.HasPrefix(setup.URI, "otpauth://totp/") {
		t.Fatalf("%v %+v", err, setup)
	}
	if strings.Contains(setup.URI, "correct") {
		t.Fatal("the URI must not carry the password")
	}
	if _, err := e.svc.ConfirmTwoFactor(ctx, c, p, "000000"); !errors.Is(err, ErrInvalidTwoFactorCode) {
		t.Fatalf("wrong confirmation code: %v", err)
	}
	if sec, _ := e.svc.Security(ctx, p); sec.TwoFactorEnabled {
		t.Fatal("two-factor must stay off until confirmed")
	}
	code := e.totpCode(t, setup.Secret)
	if _, err := e.svc.ConfirmTwoFactor(ctx, c, p, code); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetupTwoFactor(ctx, p, "correct horse"); !errors.Is(err, ErrTwoFactorEnabled) {
		t.Fatalf("setup while enabled: %v", err)
	}

	// The secret is stored sealed, not in clear.
	st, err := e.store.SecuritySettings().Get(ctx, p.UserID)
	if err != nil || st.TwoFactorSecret == nil || strings.Contains(*st.TwoFactorSecret, setup.Secret) {
		t.Fatalf("secret must be encrypted at rest: %v", err)
	}

	// Disabling needs the password and a code.
	e.skip(time.Minute)
	if err := e.svc.DisableTwoFactor(ctx, c, p, "wrong password", e.totpCode(t, setup.Secret)); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disable with a wrong password: %v", err)
	}
	if err := e.svc.DisableTwoFactor(ctx, c, p, "correct horse", "000000"); !errors.Is(err, ErrInvalidTwoFactorCode) {
		t.Fatalf("disable with a wrong code: %v", err)
	}
	if err := e.svc.DisableTwoFactor(ctx, c, p, "correct horse", e.totpCode(t, setup.Secret)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DisableTwoFactor(ctx, c, p, "correct horse", "123456"); !errors.Is(err, ErrTwoFactorNotEnabled) {
		t.Fatalf("disable twice: %v", err)
	}
	res, err := e.svc.Login(ctx, c, "hank", "correct horse")
	if err != nil || res.Tokens == nil {
		t.Fatalf("after disabling, login needs no code: %v", err)
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.6")
	a := e.register(t, "ivan", "correct horse")

	if err := e.svc.Logout(ctx, c, principal(a)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, a.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("after logout: %v", err)
	}
	if _, err := e.svc.Refresh(ctx, c, a.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("refresh after logout: %v", err)
	}

	b, err := e.svc.Login(ctx, c, "ivan", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.LogoutByRefreshToken(ctx, c, b.Tokens.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Refresh(ctx, c, b.Tokens.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("refresh after cookie logout: %v", err)
	}
	if err := e.svc.LogoutByRefreshToken(ctx, c, strings.Repeat("z", 64)); err != nil {
		t.Fatalf("an unknown cookie must not make logout fail: %v", err)
	}
}

func TestAuthenticateRejectsForgedAndForeignTokens(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ts := e.register(t, "judy", "correct horse")

	forger, err := NewTokens([]byte(strings.Repeat("x", 40)))
	if err != nil {
		t.Fatal(err)
	}
	forged, _, _ := forger.Issue(TypeAccess, Claims{UserID: ts.User.ID, SessionID: ts.SessionID}, time.Minute)
	if _, err := e.svc.Authenticate(ctx, forged); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("forged signature: %v", err)
	}
	media, _, _ := e.svc.tokens.Issue(TypeMedia, Claims{UserID: ts.User.ID, SessionID: ts.SessionID}, time.Minute)
	if _, err := e.svc.Authenticate(ctx, media); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("a token of another type must not authenticate: %v", err)
	}
	wrongUser, _, _ := e.svc.tokens.Issue(TypeAccess, Claims{UserID: ts.User.ID + 1000, SessionID: ts.SessionID}, time.Minute)
	if _, err := e.svc.Authenticate(ctx, wrongUser); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("a session of another user: %v", err)
	}
	unknown, _, _ := e.svc.tokens.Issue(TypeAccess, Claims{UserID: ts.User.ID, SessionID: uuid.New()}, time.Minute)
	if _, err := e.svc.Authenticate(ctx, unknown); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("an unknown session: %v", err)
	}
	expired, _, _ := e.svc.tokens.Issue(TypeAccess, Claims{UserID: ts.User.ID, SessionID: ts.SessionID}, -time.Minute)
	if _, err := e.svc.Authenticate(ctx, expired); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, "garbage"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("garbage: %v", err)
	}
}

func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.7")
	a := e.register(t, "kate", "correct horse")
	r, err := e.svc.Login(ctx, c, "kate", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	b := r.Tokens
	// Warm the cache so the test proves the tombstone, not the database, refuses the token.
	if _, err := e.svc.Authenticate(ctx, b.AccessToken); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.ChangePassword(ctx, c, principal(a), "wrong password", "a new password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong current password: %v", err)
	}
	var ve *ValidationError
	if err := e.svc.ChangePassword(ctx, c, principal(a), "correct horse", "short"); !errors.As(err, &ve) {
		t.Fatalf("weak new password: %v", err)
	}
	if err := e.svc.ChangePassword(ctx, c, principal(a), "correct horse", "a new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, a.AccessToken); err != nil {
		t.Fatalf("the current session stays: %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, b.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("other sessions are revoked: %v", err)
	}
	if _, err := e.svc.Login(ctx, c, "kate", "correct horse"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password: %v", err)
	}
	if _, err := e.svc.Login(ctx, c, "kate", "a new password"); err != nil {
		t.Fatalf("new password: %v", err)
	}
}

func TestSessionManagement(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.8")
	a := e.register(t, "liam", "correct horse")
	r1, _ := e.svc.Login(ctx, c, "liam", "correct horse")
	r2, _ := e.svc.Login(ctx, c, "liam", "correct horse")
	p := principal(a)

	list, err := e.svc.Sessions(ctx, p)
	if err != nil || len(list) != 3 {
		t.Fatalf("%v %d", err, len(list))
	}
	current := 0
	for _, s := range list {
		if s.Current {
			current++
			if s.ID != a.SessionID {
				t.Fatal("the wrong session is marked current")
			}
		}
	}
	if current != 1 {
		t.Fatalf("%d current sessions", current)
	}

	if err := e.svc.RevokeSession(ctx, c, p, r1.Tokens.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, r1.Tokens.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked session: %v", err)
	}
	if err := e.svc.RevokeSession(ctx, c, p, uuid.New()); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unknown session: %v", err)
	}

	// Another user's session is not revocable and looks like it does not exist.
	other := e.register(t, "mia", "correct horse")
	if err := e.svc.RevokeSession(ctx, c, p, other.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("foreign session: %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, other.AccessToken); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.RevokeOtherSessions(ctx, c, p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, r2.Tokens.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("other sessions: %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, a.AccessToken); err != nil {
		t.Fatalf("current session: %v", err)
	}
}

func TestSessionDuration(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.9")
	a := e.register(t, "noah", "correct horse")
	p := principal(a)

	var ve *ValidationError
	if err := e.svc.SetSessionDuration(ctx, c, p, 7); !errors.As(err, &ve) {
		t.Fatalf("unsupported duration: %v", err)
	}
	if err := e.svc.SetSessionDuration(ctx, c, p, 30); err != nil {
		t.Fatal(err)
	}
	if sec, err := e.svc.Security(ctx, p); err != nil || sec.SessionDurationDays != 30 {
		t.Fatalf("%v %+v", err, sec)
	}
	r, err := e.svc.Login(ctx, c, "noah", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(r.Tokens.RefreshExpiresAt); d > 31*24*time.Hour || d < 29*24*time.Hour {
		t.Fatalf("new sessions last 30 days, got %v", d)
	}
}

func TestRecoveryTokenResetsPassword(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.10")
	a := e.register(t, "olga", "correct horse")

	token, err := e.svc.IssueRecoveryToken(ctx, a.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := e.store.Pool().QueryRow(ctx, "SELECT count(*) FROM recovery_tokens WHERE jti_hash = $1", token).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("the recovery token must be stored hashed: %v %d", err, stored)
	}

	if err := e.svc.ResetPassword(ctx, c, "not-a-valid-token-at-all", "brand new pass"); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatalf("unknown token: %v", err)
	}
	var ve *ValidationError
	if err := e.svc.ResetPassword(ctx, c, token, "short"); !errors.As(err, &ve) {
		t.Fatalf("weak password: %v", err)
	}
	if err := e.svc.ResetPassword(ctx, c, token, "brand new pass"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ResetPassword(ctx, c, token, "another new pass"); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatalf("a recovery token is single use: %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, a.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("all sessions end after a reset: %v", err)
	}
	if _, err := e.svc.Login(ctx, c, "olga", "brand new pass"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryTokenExpires(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.register(t, "paul", "correct horse")
	token, err := e.svc.IssueRecoveryToken(ctx, a.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Pool().Exec(ctx, "UPDATE recovery_tokens SET expires_at = now() - interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ResetPassword(ctx, e.client("10.0.0.10"), token, "brand new pass"); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatalf("expired token: %v", err)
	}
}

func TestWebSocketTickets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.register(t, "quinn", "correct horse")
	p := principal(a)

	ticket, ttl, err := e.svc.IssueTicket(ctx, p)
	if err != nil || ttl <= 0 {
		t.Fatal(err)
	}
	got, err := e.svc.RedeemTicket(ctx, ticket)
	if err != nil || got != p {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := e.svc.RedeemTicket(ctx, ticket); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("a ticket is single use: %v", err)
	}

	ticket, _, err = e.svc.IssueTicket(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Logout(ctx, e.client("10.0.0.11"), p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RedeemTicket(ctx, ticket); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("a ticket of a revoked session: %v", err)
	}
}

func TestConcurrentRefreshRotatesOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.register(t, "rita", "correct horse")

	var wg sync.WaitGroup
	var ok atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.svc.Refresh(ctx, e.client("10.0.0.12"), a.RefreshToken); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("exactly one concurrent refresh may win, got %d", ok.Load())
	}
}

func TestSecurityEventsHaveNoSecrets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.13")
	a := e.register(t, "sam", "correct horse")
	_, _ = e.svc.Login(ctx, c, "sam", "wrong password")
	_ = e.svc.ChangePassword(ctx, c, principal(a), "correct horse", "a new password")

	rows, err := e.store.Pool().Query(ctx, "SELECT event_type, coalesce(details::text, '') FROM user_security_events WHERE user_id = $1", a.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var typ, details string
		if err := rows.Scan(&typ, &details); err != nil {
			t.Fatal(err)
		}
		seen[typ] = true
		for _, secret := range []string{"correct horse", "wrong password", "a new password", a.RefreshToken, a.AccessToken} {
			if strings.Contains(details, secret) {
				t.Fatalf("event %s leaks a secret", typ)
			}
		}
	}
	for _, want := range []string{"register", "login_failed", "password_changed"} {
		if !seen[want] {
			t.Errorf("missing security event %q (got %v)", want, seen)
		}
	}
}

func TestNewServiceRejectsWeakConfig(t *testing.T) {
	good := Config{
		JWTSecret:      []byte(strings.Repeat("j", 40)),
		EncryptionKey:  []byte(strings.Repeat("k", 32)),
		RecoveryPepper: []byte(strings.Repeat("p", 32)),
	}
	bad := map[string]func(*Config){
		"short jwt secret": func(c *Config) { c.JWTSecret = []byte("short") },
		"bad key length":   func(c *Config) { c.EncryptionKey = []byte("short") },
		"short pepper":     func(c *Config) { c.RecoveryPepper = []byte("short") },
	}
	for name, mutate := range bad {
		cfg := good
		mutate(&cfg)
		if _, err := NewService(nil, nil, cfg, nil); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
