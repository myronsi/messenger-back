package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

// Store is the part of the PostgreSQL store the service uses; *postgres.Store implements it.
type Store interface {
	Users() postgres.UserRepository
	Sessions() postgres.SessionRepository
	SecuritySettings() postgres.SecuritySettingsRepository
	RecoveryTokens() postgres.RecoveryTokenRepository
	SecurityEvents() postgres.SecurityEventRepository
}

// Config configures the service. Secrets come from the environment; none of them is ever logged.
type Config struct {
	JWTSecret     []byte
	EncryptionKey []byte
	// RecoveryPepper keys the hash of recovery tokens, so a database leak does not yield usable tokens.
	RecoveryPepper []byte
	// SessionCacheTTL is how long "this session is active" is cached in Redis (default 30s). A revoked
	// session is refused immediately; the TTL only bounds how stale an expiry change can be.
	SessionCacheTTL time.Duration
	// RefreshReuseGrace is how long the previous refresh token is tolerated after a rotation, for tabs
	// that refresh at the same moment. 0 turns the tolerance off.
	RefreshReuseGrace time.Duration
	// HashConcurrency bounds simultaneous password hashes (default 4).
	HashConcurrency int
	// Namespace prefixes the Redis keys (default "auth").
	Namespace string
	// OnRevoked is told about every revoked session, so open WebSockets of those sessions can be closed.
	// Optional; it must not block.
	OnRevoked func(ctx context.Context, sessionIDs []uuid.UUID)
}

// Client describes who is calling, for rate limits and the security log.
type Client struct {
	IP        netip.Addr
	UserAgent string
}

func (c Client) ipString() string {
	if !c.IP.IsValid() {
		return "unknown"
	}
	return c.IP.String()
}

func (c Client) ipPtr() *netip.Addr {
	if !c.IP.IsValid() {
		return nil
	}
	ip := c.IP
	return &ip
}

func (c Client) agentPtr() *string {
	ua := cleanUserAgent(c.UserAgent)
	if ua == "" {
		return nil
	}
	return &ua
}

const maxUserAgentLen = 200

// cleanUserAgent drops control characters and caps the length: the value is shown to the user in the
// list of their sessions and goes into the security log.
func cleanUserAgent(ua string) string {
	ua = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, ua)
	ua = strings.TrimSpace(ua)
	if utf8.RuneCountInString(ua) > maxUserAgentLen {
		ua = string([]rune(ua)[:maxUserAgentLen])
	}
	return ua
}

// Errors of the service. Match with errors.Is; RateLimitError and ValidationError with errors.As.
var (
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrInvalidTwoFactorCode = errors.New("invalid two-factor code")
	ErrInvalidChallenge     = errors.New("invalid or expired login challenge")
	ErrInvalidRefreshToken  = errors.New("invalid refresh token")
	// ErrRevocationIncomplete means sessions were revoked in PostgreSQL but the cache could not be told, so
	// access tokens may still be accepted for up to SESSION_CACHE_TTL.
	ErrRevocationIncomplete = errors.New("session revocation not fully applied")
	// ErrRefreshSuperseded means the token was replaced a moment ago by a parallel refresh. The client holds
	// (or is about to receive) the new token, so it must keep its cookie.
	ErrRefreshSuperseded   = errors.New("refresh token superseded by a concurrent refresh")
	ErrUnauthenticated     = errors.New("unauthenticated")
	ErrUsernameTaken       = errors.New("username taken")
	ErrSessionNotFound     = errors.New("session not found")
	ErrTwoFactorEnabled    = errors.New("two-factor authentication is already enabled")
	ErrNoPendingTwoFactor  = errors.New("no two-factor setup in progress")
	ErrTwoFactorNotEnabled = errors.New("two-factor authentication is not enabled")
	ErrInvalidRecovery     = errors.New("invalid recovery token")
	ErrConflict            = errors.New("the account changed concurrently")
)

// RateLimitError says when the caller may try again.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return "rate limit exceeded" }

// ValidationError names the invalid field.
type ValidationError struct{ Field, Message string }

func (e *ValidationError) Error() string { return "invalid " + e.Field + ": " + e.Message }

// Principal is the authenticated caller of a request.
type Principal struct {
	UserID    int64
	SessionID uuid.UUID
}

// Tokens is what a successful sign-in hands to the client.
type TokenSet struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	SessionID        uuid.UUID
	User             postgres.User
}

// LoginResult is either a TokenSet or, with two-factor on, a challenge to answer.
type LoginResult struct {
	Tokens    *TokenSet
	Challenge string
}

// SessionInfo is a session as shown to its owner.
type SessionInfo struct {
	postgres.Session
	Current bool
}

// SecurityOptions are the user-visible security settings.
type SecurityOptions struct {
	SessionDurationDays int
	TwoFactorEnabled    bool
}

// TwoFactorSetup is what the authenticator app needs.
type TwoFactorSetup struct {
	Secret string
	URI    string
}

// SessionDurations are the allowed values of the session duration setting, in days.
var SessionDurations = []int{30, 90, 180, 365}

// RecoveryTokenTTL is how long a recovery token can be used.
const RecoveryTokenTTL = 15 * time.Minute

const maxBioLen = 500

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)

// Service implements the authentication use cases.
type Service struct {
	store   Store
	log     *slog.Logger
	hasher  *Hasher
	tokens  *Tokens
	sealer  *Sealer
	pepper  []byte
	limiter *Limiter
	chal    *Challenges
	replay  *TOTPReplay
	cache   *SessionCache
	tickets *Tickets
	grace   time.Duration
	now     func() time.Time
	// onRevoked is Config.OnRevoked.
	onRevoked func(ctx context.Context, sessionIDs []uuid.UUID)
	dummy     func() (string, error)
}

// NewService wires the service.
func NewService(store Store, rdb Redis, cfg Config, log *slog.Logger) (*Service, error) {
	tokens, err := NewTokens(cfg.JWTSecret)
	if err != nil {
		return nil, err
	}
	sealer, err := NewSealer(cfg.EncryptionKey)
	if err != nil {
		return nil, err
	}
	if len(cfg.RecoveryPepper) < 16 {
		return nil, errors.New("recovery pepper is too short")
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "auth"
	}
	if cfg.SessionCacheTTL <= 0 {
		cfg.SessionCacheTTL = 30 * time.Second
	}
	if cfg.RefreshReuseGrace < 0 {
		cfg.RefreshReuseGrace = 0
	}
	if cfg.HashConcurrency <= 0 {
		cfg.HashConcurrency = 4
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		store:   store,
		log:     log,
		hasher:  NewHasher(cfg.HashConcurrency),
		tokens:  tokens,
		sealer:  sealer,
		pepper:  cfg.RecoveryPepper,
		limiter: NewLimiter(rdb, cfg.Namespace),
		chal:    NewChallenges(rdb, cfg.Namespace),
		replay:  NewTOTPReplay(rdb, cfg.Namespace),
		cache:   NewSessionCache(rdb, cfg.Namespace, cfg.SessionCacheTTL),
		tickets: NewTickets(rdb, cfg.Namespace),
		grace:   cfg.RefreshReuseGrace,
		now:     time.Now,

		onRevoked: cfg.OnRevoked,
	}
	s.dummy = sync.OnceValues(func() (string, error) { return s.hasher.dummyHash(context.Background()) })
	return s, nil
}

// ---------------------------------------------------------------- helpers

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (s *Service) recoveryHash(token string) string {
	m := hmac.New(sha256.New, s.pepper)
	m.Write([]byte(token))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Service) event(ctx context.Context, uid int64, typ string, c Client, details map[string]any) {
	var raw json.RawMessage
	if len(details) > 0 {
		raw, _ = json.Marshal(details)
	}
	// The response must not fail, or be cancelled with the request, because the log entry failed.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if _, err := s.store.SecurityEvents().Record(ctx, postgres.NewSecurityEvent{
		UserID: uid, Type: typ, IP: c.ipPtr(), UserAgent: c.agentPtr(), Details: raw,
	}); err != nil {
		s.log.WarnContext(ctx, "security event not recorded", "event", typ, "user_id", uid, "err", err)
	}
}

// allow fails closed: a limiter that cannot be reached denies the request.
// reservation is a set of rate-limit events counted before a secret is verified. They stay counted when the
// secret is wrong; release gives them back when it is right.
type reservation struct {
	s     *Service
	items []reservedEvent
}

type reservedEvent struct {
	limitCheck
	member string
}

// allow counts one attempt for every check, atomically per check, and refuses when one is over its limit.
// An attempt that is refused is not counted.
func (s *Service) allow(ctx context.Context, checks ...limitCheck) (*reservation, error) {
	res := &reservation{s: s}
	for _, c := range checks {
		member, ok, retry, err := s.limiter.Reserve(ctx, c.rule, c.subject)
		if err == nil && !ok {
			err = &RateLimitError{RetryAfter: retry}
		}
		if err != nil {
			res.release(ctx)
			return nil, err
		}
		res.items = append(res.items, reservedEvent{c, member})
	}
	return res, nil
}

func (r *reservation) release(ctx context.Context) {
	for _, e := range r.items {
		if err := r.s.limiter.Release(ctx, e.rule, e.subject, e.member); err != nil {
			r.s.log.WarnContext(ctx, "rate limiter unavailable", "rule", e.rule.Name, "err", err)
		}
	}
	r.items = nil
}

type limitCheck struct {
	rule    Rule
	subject string
}

func (s *Service) take(ctx context.Context, r Rule, subject string) error {
	ok, retry, err := s.limiter.Take(ctx, r, subject)
	if err != nil {
		return err
	}
	if !ok {
		return &RateLimitError{RetryAfter: retry}
	}
	return nil
}

func (s *Service) clear(ctx context.Context, r Rule, subject string) {
	if err := s.limiter.Reset(ctx, r, subject); err != nil {
		s.log.WarnContext(ctx, "rate limiter unavailable", "rule", r.Name, "err", err)
	}
}

// revokeInCache writes the revocation tombstones, retrying briefly. If it cannot, a cached "active"
// verdict could keep an access token alive until it expires, so the failure is returned and the request
// fails instead of reporting an immediate sign-out that did not fully happen.
func (s *Service) revokeInCache(ctx context.Context, ids ...uuid.UUID) error {
	if s.onRevoked != nil && len(ids) > 0 {
		s.onRevoked(ctx, ids)
	}
	var err error
	for attempt := range 3 {
		if err = s.cache.Revoke(ctx, ids...); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	s.log.ErrorContext(ctx, "session revoked in the database but not in the cache", "sessions", len(ids), "err", err)
	return fmt.Errorf("%w: %w", ErrRevocationIncomplete, err)
}

func normalizeUsername(u string) string { return strings.ToLower(strings.TrimSpace(u)) }

// ---------------------------------------------------------------- validation

func validateUsername(u string) error {
	if !usernameRE.MatchString(u) {
		return &ValidationError{"username", "must be 3 to 32 letters, digits or underscores"}
	}
	return nil
}

func validatePassword(p string) error {
	if n := utf8.RuneCountInString(p); n < 8 || n > 128 {
		return &ValidationError{"password", "must be 8 to 128 characters"}
	}
	return nil
}

// normalizeDisplayName trims and collapses whitespace.
func normalizeDisplayName(n string) (string, error) {
	n = strings.Join(strings.Fields(n), " ")
	if c := utf8.RuneCountInString(n); c < 1 || c > 64 {
		return "", &ValidationError{"display_name", "must be 1 to 64 characters"}
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", &ValidationError{"display_name", "must not contain control characters"}
		}
	}
	return n, nil
}

// ---------------------------------------------------------------- sessions and tokens

func newRefreshToken() (token, hash string, err error) {
	token, err = randomToken(48)
	if err != nil {
		return "", "", err
	}
	return token, sha256Hex(token), nil
}

func (s *Service) accessToken(sessionID uuid.UUID, userID int64) (string, time.Time, error) {
	return s.tokens.Issue(TypeAccess, Claims{UserID: userID, SessionID: sessionID}, AccessTokenTTL)
}

// startSession creates a session and the first token pair.
func (s *Service) startSession(ctx context.Context, u postgres.User, c Client) (*TokenSet, error) {
	settings, err := s.store.SecuritySettings().Get(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	refresh, hash, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	sess, err := s.store.Sessions().Create(ctx, postgres.NewSession{
		UserID: u.ID, RefreshTokenHash: hash, UserAgent: c.agentPtr(), IP: c.ipPtr(),
		ExpiresAt: s.now().Add(time.Duration(settings.SessionDurationDays) * 24 * time.Hour),
	})
	if err != nil {
		return nil, err
	}
	access, accessExp, err := s.accessToken(sess.ID, u.ID)
	if err != nil {
		return nil, err
	}
	return &TokenSet{
		AccessToken: access, AccessExpiresAt: accessExp,
		RefreshToken: refresh, RefreshExpiresAt: sess.ExpiresAt,
		SessionID: sess.ID, User: u,
	}, nil
}

// ---------------------------------------------------------------- register and login

// Register creates an account and signs it in.
func (s *Service) Register(ctx context.Context, c Client, username, displayName, password string, bio *string) (*TokenSet, error) {
	username = strings.TrimSpace(username)
	if err := validateUsername(username); err != nil {
		return nil, err
	}
	displayName, err := normalizeDisplayName(displayName)
	if err != nil {
		return nil, err
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	if bio != nil {
		b := strings.TrimSpace(*bio)
		if utf8.RuneCountInString(b) > maxBioLen {
			return nil, &ValidationError{"bio", "must be at most 500 characters"}
		}
		bio = nil
		if b != "" {
			bio = &b
		}
	}
	if err := s.take(ctx, RuleRegisterIP, c.ipString()); err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return nil, err
	}
	u, err := s.store.Users().Register(ctx, strings.ToLower(username), displayName, hash)
	if errors.Is(err, postgres.ErrUsernameTaken) {
		return nil, ErrUsernameTaken
	}
	if err != nil {
		return nil, err
	}
	if bio != nil {
		// The account exists either way; a failed bio must not turn a created account into an error.
		if updated, err := s.store.Users().UpdateProfile(ctx, u.ID, postgres.Profile{DisplayName: u.DisplayName, Bio: bio}); err != nil {
			s.log.WarnContext(ctx, "bio not saved at registration", "user_id", u.ID, "err", err)
		} else {
			u = updated
		}
	}
	ts, err := s.startSession(ctx, u, c)
	if err != nil {
		return nil, err
	}
	s.event(ctx, u.ID, "register", c, nil)
	return ts, nil
}

// Login checks the password. With two-factor enabled it returns a challenge instead of tokens.
func (s *Service) Login(ctx context.Context, c Client, username, password string) (*LoginResult, error) {
	username = normalizeUsername(username)
	if username == "" || len(username) > 64 || password == "" || len(password) > 1024 {
		return nil, ErrInvalidCredentials
	}
	ipRule, userRule := limitCheck{RuleLoginIP, c.ipString()}, limitCheck{RuleLoginUser, username}
	attempt, err := s.allow(ctx, ipRule, userRule)
	if err != nil {
		return nil, err
	}

	creds, err := s.store.Users().Credentials(ctx, username)
	known := err == nil
	if err != nil && !errors.Is(err, postgres.ErrNotFound) {
		return nil, err
	}
	stored := creds.PasswordHash
	if !known {
		// Same work as for a real account, so the response time does not reveal which usernames exist.
		if stored, err = s.dummy(); err != nil {
			return nil, err
		}
	}
	ok, rehash, verr := s.hasher.Verify(ctx, password, stored)
	if verr != nil && !errors.Is(verr, ErrHashFormat) {
		return nil, verr
	}
	if verr != nil {
		s.log.ErrorContext(ctx, "stored password hash has an unsupported format", "user_id", creds.UserID)
	}
	if !known || !ok {
		if known {
			s.event(ctx, creds.UserID, "login_failed", c, nil)
		}
		return nil, ErrInvalidCredentials
	}
	attempt.release(ctx)
	s.clear(ctx, RuleLoginUser, username)

	if rehash {
		s.upgradeHash(ctx, creds.UserID, stored, password)
	}

	settings, err := s.store.SecuritySettings().Get(ctx, creds.UserID)
	if err != nil {
		return nil, err
	}
	if settings.TwoFactorEnabled {
		challenge, err := s.chal.Create(ctx, creds.UserID)
		if err != nil {
			return nil, err
		}
		return &LoginResult{Challenge: challenge}, nil
	}
	u, err := s.store.Users().Get(ctx, creds.UserID)
	if err != nil {
		return nil, err
	}
	ts, err := s.startSession(ctx, u, c)
	if err != nil {
		return nil, err
	}
	s.event(ctx, u.ID, "login", c, nil)
	return &LoginResult{Tokens: ts}, nil
}

// upgradeHash stores the password in the current format. It never fails the login, and it only replaces
// the hash that was just verified, so a concurrent password change wins.
func (s *Service) upgradeHash(ctx context.Context, uid int64, old, password string) {
	hash, err := s.hasher.Hash(ctx, password)
	if err == nil {
		_, err = s.store.Users().RehashPassword(ctx, uid, old, hash)
	}
	if err != nil {
		s.log.WarnContext(ctx, "password hash not upgraded", "user_id", uid, "err", err)
	}
}

// checkSecondFactor verifies a TOTP or recovery code of the user.
func (s *Service) checkSecondFactor(ctx context.Context, uid int64, settings postgres.SecuritySettings, code string) (ok, recovery bool, err error) {
	code = strings.TrimSpace(code)
	if IsRecoveryCode(code) {
		ok, err = s.store.SecuritySettings().UseRecoveryCode(ctx, uid, HashRecoveryCode(code))
		return ok, true, err
	}
	if settings.TwoFactorSecret == nil {
		return false, false, nil
	}
	secret, err := s.sealer.Open(*settings.TwoFactorSecret, totpAAD(uid))
	if err != nil {
		s.log.ErrorContext(ctx, "two-factor secret cannot be opened", "user_id", uid)
		return false, false, nil
	}
	step, valid := VerifyTOTP(secret, code, s.now())
	if !valid {
		return false, false, nil
	}
	fresh, err := s.replay.Claim(ctx, uid, step)
	if err != nil {
		return false, false, err
	}
	return fresh, false, nil
}

func totpAAD(uid int64) string { return fmt.Sprintf("totp:%d", uid) }

// LoginTwoFactor finishes a login that asked for a second factor.
func (s *Service) LoginTwoFactor(ctx context.Context, c Client, challenge, code string) (*TokenSet, error) {
	if challenge == "" || len(challenge) > 256 {
		return nil, ErrInvalidChallenge
	}
	ipRule := limitCheck{RuleTwoFAIP, c.ipString()}
	ipAttempt, err := s.allow(ctx, ipRule)
	if err != nil {
		return nil, err
	}
	uid, err := s.chal.Attempt(ctx, challenge)
	if errors.Is(err, ErrChallengeInvalid) {
		return nil, ErrInvalidChallenge
	}
	if err != nil {
		return nil, err
	}
	userRule := limitCheck{RuleTwoFAUser, fmt.Sprint(uid)}
	userAttempt, err := s.allow(ctx, userRule)
	if err != nil {
		ipAttempt.release(ctx)
		return nil, err
	}
	settings, err := s.store.SecuritySettings().Get(ctx, uid)
	if err != nil {
		return nil, err
	}
	if !settings.TwoFactorEnabled {
		return nil, ErrInvalidChallenge
	}
	ok, recovery, err := s.checkSecondFactor(ctx, uid, settings, code)
	if err != nil {
		return nil, err
	}
	if !ok {
		s.event(ctx, uid, "login_2fa_failed", c, nil)
		return nil, ErrInvalidTwoFactorCode
	}
	// Only the request that ends the challenge gets a session.
	if err := s.chal.Consume(ctx, challenge); err != nil {
		if errors.Is(err, ErrChallengeInvalid) {
			return nil, ErrInvalidChallenge
		}
		return nil, err
	}
	ipAttempt.release(ctx)
	userAttempt.release(ctx)
	s.clear(ctx, RuleTwoFAUser, userRule.subject)
	u, err := s.store.Users().Get(ctx, uid)
	if err != nil {
		return nil, err
	}
	ts, err := s.startSession(ctx, u, c)
	if err != nil {
		return nil, err
	}
	s.event(ctx, uid, "login_2fa", c, nil)
	if recovery {
		s.event(ctx, uid, "recovery_code_used", c, nil)
	}
	return ts, nil
}

// ---------------------------------------------------------------- refresh and logout

// Refresh exchanges a refresh token for a new pair. The old refresh token stops working; presenting it
// again later revokes the whole session.
func (s *Service) Refresh(ctx context.Context, c Client, refreshToken string) (*TokenSet, error) {
	if err := s.take(ctx, RuleRefreshIP, c.ipString()); err != nil {
		return nil, err
	}
	if n := len(refreshToken); n < 32 || n > 256 {
		return nil, ErrInvalidRefreshToken
	}
	next, nextHash, err := newRefreshToken()
	if err != nil {
		return nil, err
	}
	res, err := s.store.Sessions().Rotate(ctx, postgres.RotateRequest{
		PresentedHash: sha256Hex(refreshToken),
		NewHash:       nextHash,
		NewExpiresAt:  func(days int) time.Time { return s.now().Add(time.Duration(days) * 24 * time.Hour) },
		UserAgent:     c.agentPtr(),
		IP:            c.ipPtr(),
		ReuseGrace:    s.grace,
	})
	if err != nil {
		return nil, err
	}
	switch res.Outcome {
	case postgres.RotateOK:
	case postgres.RotateReuse:
		cacheErr := s.revokeInCache(ctx, res.Session.ID)
		s.log.WarnContext(ctx, "refresh token reuse detected, session revoked", "user_id", res.Session.UserID, "session_id", res.Session.ID)
		s.event(ctx, res.Session.UserID, "refresh_reuse_detected", c, map[string]any{"session_id": res.Session.ID})
		if cacheErr != nil {
			return nil, cacheErr
		}
		return nil, ErrInvalidRefreshToken
	case postgres.RotateStale:
		return nil, ErrRefreshSuperseded
	default:
		return nil, ErrInvalidRefreshToken
	}
	u, err := s.store.Users().Get(ctx, res.Session.UserID)
	if err != nil {
		return nil, err
	}
	access, accessExp, err := s.accessToken(res.Session.ID, u.ID)
	if err != nil {
		return nil, err
	}
	return &TokenSet{
		AccessToken: access, AccessExpiresAt: accessExp,
		RefreshToken: next, RefreshExpiresAt: res.Session.ExpiresAt,
		SessionID: res.Session.ID, User: u,
	}, nil
}

// Logout ends the session of the principal.
func (s *Service) Logout(ctx context.Context, c Client, p Principal) error {
	revoked, err := s.store.Sessions().RevokeByID(ctx, p.SessionID)
	if err != nil {
		return err
	}
	cacheErr := s.revokeInCache(ctx, p.SessionID)
	if revoked {
		s.event(ctx, p.UserID, "logout", c, nil)
	}
	return cacheErr
}

// LogoutByRefreshToken ends the session a refresh token belongs to. It succeeds for unknown tokens, so
// signing out never fails on a stale cookie.
func (s *Service) LogoutByRefreshToken(ctx context.Context, c Client, refreshToken string) error {
	if n := len(refreshToken); n < 32 || n > 256 {
		return nil
	}
	sess, err := s.store.Sessions().ByRefreshHash(ctx, sha256Hex(refreshToken))
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.Logout(ctx, c, Principal{UserID: sess.UserID, SessionID: sess.ID})
}

// ---------------------------------------------------------------- authenticating requests

// Authenticate verifies an access token and that its session is still active.
func (s *Service) Authenticate(ctx context.Context, accessToken string) (Principal, error) {
	claims, err := s.tokens.Parse(accessToken, TypeAccess)
	if err != nil {
		return Principal{}, err
	}
	p := Principal(claims)

	hit, found, cerr := s.cache.Get(ctx, p.SessionID)
	if cerr != nil {
		s.log.WarnContext(ctx, "session cache unavailable", "err", cerr)
	}
	if found {
		if hit.Revoked || hit.UserID != p.UserID || !hit.Expires.After(s.now()) {
			return Principal{}, ErrUnauthenticated
		}
		s.touch(ctx, p.SessionID)
		return p, nil
	}

	sess, err := s.store.Sessions().Get(ctx, p.SessionID)
	if errors.Is(err, postgres.ErrNotFound) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	if sess.UserID != p.UserID || !sess.Active(s.now()) {
		return Principal{}, ErrUnauthenticated
	}
	if cerr == nil {
		if err := s.cache.Put(ctx, p.SessionID, p.UserID, sess.ExpiresAt); err != nil {
			s.log.WarnContext(ctx, "session cache unavailable", "err", err)
		}
	}
	s.touch(ctx, p.SessionID)
	return p, nil
}

func (s *Service) touch(ctx context.Context, id uuid.UUID) {
	if !s.cache.ShouldTouch(ctx, id) {
		return
	}
	if err := s.store.Sessions().Touch(ctx, id); err != nil && !errors.Is(err, postgres.ErrNotFound) {
		s.log.WarnContext(ctx, "session activity not recorded", "err", err)
	}
}

// Me returns the account of the principal.
func (s *Service) Me(ctx context.Context, p Principal) (postgres.User, error) {
	u, err := s.store.Users().Get(ctx, p.UserID)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.User{}, ErrUnauthenticated
	}
	return u, err
}

// ---------------------------------------------------------------- password

// verifyPassword checks the current password of the account, counting failures.
func (s *Service) verifyPassword(ctx context.Context, uid int64, password string) (hash string, err error) {
	rule := limitCheck{RulePasswordU, fmt.Sprint(uid)}
	attempt, err := s.allow(ctx, rule)
	if err != nil {
		return "", err
	}
	if password == "" || len(password) > 1024 {
		return "", ErrInvalidCredentials
	}
	creds, err := s.store.Users().CredentialsByID(ctx, uid)
	if err != nil {
		return "", err
	}
	ok, _, err := s.hasher.Verify(ctx, password, creds.PasswordHash)
	if err != nil && !errors.Is(err, ErrHashFormat) {
		return "", err
	}
	if !ok {
		return "", ErrInvalidCredentials
	}
	attempt.release(ctx)
	return creds.PasswordHash, nil
}

// ChangePassword sets a new password and revokes every other session.
func (s *Service) ChangePassword(ctx context.Context, c Client, p Principal, current, next string) error {
	if err := validatePassword(next); err != nil {
		return err
	}
	old, err := s.verifyPassword(ctx, p.UserID, current)
	if err != nil {
		return err
	}
	hash, err := s.hasher.Hash(ctx, next)
	if err != nil {
		return err
	}
	revoked, err := s.store.Users().ChangePassword(ctx, p.UserID, old, hash, p.SessionID)
	if errors.Is(err, postgres.ErrConflict) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	cacheErr := s.revokeInCache(ctx, revoked...)
	s.event(ctx, p.UserID, "password_changed", c, map[string]any{"sessions_revoked": len(revoked)})
	return cacheErr
}

// ---------------------------------------------------------------- sessions

// Sessions lists the active sessions of the user.
func (s *Service) Sessions(ctx context.Context, p Principal) ([]SessionInfo, error) {
	list, err := s.store.Sessions().ListActive(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, len(list))
	for i, sess := range list {
		out[i] = SessionInfo{Session: sess, Current: sess.ID == p.SessionID}
	}
	return out, nil
}

// RevokeSession signs one device out.
func (s *Service) RevokeSession(ctx context.Context, c Client, p Principal, id uuid.UUID) error {
	err := s.store.Sessions().Revoke(ctx, p.UserID, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrSessionNotFound
	}
	if err != nil {
		return err
	}
	cacheErr := s.revokeInCache(ctx, id)
	s.event(ctx, p.UserID, "session_revoked", c, map[string]any{"session_id": id})
	return cacheErr
}

// RevokeOtherSessions signs out every device but the current one.
func (s *Service) RevokeOtherSessions(ctx context.Context, c Client, p Principal) error {
	ids, err := s.store.Sessions().RevokeOthers(ctx, p.UserID, p.SessionID)
	if err != nil {
		return err
	}
	cacheErr := s.revokeInCache(ctx, ids...)
	if len(ids) > 0 {
		s.event(ctx, p.UserID, "other_sessions_revoked", c, map[string]any{"count": len(ids)})
	}
	return cacheErr
}

// ---------------------------------------------------------------- security settings and two-factor

// Security returns the settings of the user.
func (s *Service) Security(ctx context.Context, p Principal) (SecurityOptions, error) {
	st, err := s.store.SecuritySettings().Get(ctx, p.UserID)
	if err != nil {
		return SecurityOptions{}, err
	}
	return SecurityOptions{SessionDurationDays: st.SessionDurationDays, TwoFactorEnabled: st.TwoFactorEnabled}, nil
}

// SetSessionDuration changes how long sessions last. It applies from the next refresh on.
func (s *Service) SetSessionDuration(ctx context.Context, c Client, p Principal, days int) error {
	valid := false
	for _, d := range SessionDurations {
		valid = valid || d == days
	}
	if !valid {
		return &ValidationError{"session_duration_days", "must be one of 30, 90, 180 or 365"}
	}
	if err := s.store.SecuritySettings().SetSessionDuration(ctx, p.UserID, days); err != nil {
		return err
	}
	s.event(ctx, p.UserID, "session_duration_changed", c, map[string]any{"days": days})
	return nil
}

// SetupTwoFactor creates a new secret. Two-factor stays off until ConfirmTwoFactor.
func (s *Service) SetupTwoFactor(ctx context.Context, p Principal, password string) (*TwoFactorSetup, error) {
	if _, err := s.verifyPassword(ctx, p.UserID, password); err != nil {
		return nil, err
	}
	st, err := s.store.SecuritySettings().Get(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if st.TwoFactorEnabled {
		return nil, ErrTwoFactorEnabled
	}
	u, err := s.store.Users().Get(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return nil, err
	}
	sealed, err := s.sealer.Seal(secret, totpAAD(p.UserID))
	if err != nil {
		return nil, err
	}
	if err := s.store.SecuritySettings().BeginTwoFactor(ctx, p.UserID, sealed); err != nil {
		if errors.Is(err, postgres.ErrConflict) {
			return nil, ErrTwoFactorEnabled
		}
		return nil, err
	}
	return &TwoFactorSetup{Secret: secret, URI: TOTPURI(u.Username, secret)}, nil
}

// ConfirmTwoFactor turns two-factor on after the user proved the app works, and returns the one-time
// recovery codes. They are shown once; only their hashes are stored.
func (s *Service) ConfirmTwoFactor(ctx context.Context, c Client, p Principal, code string) ([]string, error) {
	rule := limitCheck{RuleTwoFAUser, fmt.Sprint(p.UserID)}
	attempt, err := s.allow(ctx, rule)
	if err != nil {
		return nil, err
	}
	st, err := s.store.SecuritySettings().Get(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if st.TwoFactorEnabled {
		return nil, ErrTwoFactorEnabled
	}
	if st.PendingTwoFactorSecret == nil {
		return nil, ErrNoPendingTwoFactor
	}
	secret, err := s.sealer.Open(*st.PendingTwoFactorSecret, totpAAD(p.UserID))
	if err != nil {
		return nil, ErrNoPendingTwoFactor
	}
	step, ok := VerifyTOTP(secret, strings.TrimSpace(code), s.now())
	if !ok {
		return nil, ErrInvalidTwoFactorCode
	}
	// Claim the step so the code that enabled 2FA cannot be replayed to answer the first login challenge.
	fresh, err := s.replay.Claim(ctx, p.UserID, step)
	if err != nil {
		return nil, err
	}
	if !fresh {
		return nil, ErrInvalidTwoFactorCode
	}
	attempt.release(ctx)
	codes, err := NewRecoveryCodes()
	if err != nil {
		return nil, err
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = HashRecoveryCode(c)
	}
	if err := s.store.SecuritySettings().ConfirmTwoFactor(ctx, p.UserID, *st.PendingTwoFactorSecret, hashes); err != nil {
		if errors.Is(err, postgres.ErrConflict) {
			return nil, ErrNoPendingTwoFactor
		}
		return nil, err
	}
	s.clear(ctx, RuleTwoFAUser, rule.subject)
	s.event(ctx, p.UserID, "2fa_enabled", c, nil)
	return codes, nil
}

// DisableTwoFactor turns two-factor off; it needs the password and a code.
func (s *Service) DisableTwoFactor(ctx context.Context, c Client, p Principal, password, code string) error {
	if _, err := s.verifyPassword(ctx, p.UserID, password); err != nil {
		return err
	}
	rule := limitCheck{RuleTwoFAUser, fmt.Sprint(p.UserID)}
	attempt, err := s.allow(ctx, rule)
	if err != nil {
		return err
	}
	st, err := s.store.SecuritySettings().Get(ctx, p.UserID)
	if err != nil {
		return err
	}
	if !st.TwoFactorEnabled {
		return ErrTwoFactorNotEnabled
	}
	ok, _, err := s.checkSecondFactor(ctx, p.UserID, st, code)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidTwoFactorCode
	}
	attempt.release(ctx)
	if err := s.store.SecuritySettings().DisableTwoFactor(ctx, p.UserID); err != nil {
		return err
	}
	s.clear(ctx, RuleTwoFAUser, rule.subject)
	s.event(ctx, p.UserID, "2fa_disabled", c, nil)
	return nil
}

// ---------------------------------------------------------------- recovery (temporary)

// IssueRecoveryToken creates a single-use token that lets the user set a new password. The flow that
// proves who the user is (MSGC-69) calls this; it is not reachable from the API yet.
func (s *Service) IssueRecoveryToken(ctx context.Context, userID int64) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	if err := s.store.RecoveryTokens().Issue(ctx, userID, s.recoveryHash(token), s.now().Add(RecoveryTokenTTL)); err != nil {
		return "", err
	}
	return token, nil
}

// ResetPassword spends a recovery token to set a new password and revoke all sessions.
func (s *Service) ResetPassword(ctx context.Context, c Client, token, newPassword string) error {
	if err := s.take(ctx, RuleResetIP, c.ipString()); err != nil {
		return err
	}
	if n := len(token); n < 16 || n > 256 {
		return ErrInvalidRecovery
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	hash, err := s.hasher.Hash(ctx, newPassword)
	if err != nil {
		return err
	}
	res, err := s.store.RecoveryTokens().ResetPassword(ctx, s.recoveryHash(token), hash)
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrInvalidRecovery
	}
	if err != nil {
		return err
	}
	cacheErr := s.revokeInCache(ctx, res.RevokedSessions...)
	s.event(ctx, res.UserID, "password_reset", c, map[string]any{"sessions_revoked": len(res.RevokedSessions)})
	return cacheErr
}

// ---------------------------------------------------------------- WebSocket tickets

// IssueTicket creates a one-time WebSocket ticket for the current session.
func (s *Service) IssueTicket(ctx context.Context, p Principal) (string, time.Duration, error) {
	if err := s.take(ctx, RuleTicketUser, fmt.Sprint(p.UserID)); err != nil {
		return "", 0, err
	}
	t, err := s.tickets.Issue(ctx, Ticket(p))
	return t, TicketTTL, err
}

// RedeemTicket consumes a ticket and checks that its session is still active. The WebSocket gateway
// calls it during the handshake.
func (s *Service) RedeemTicket(ctx context.Context, ticket string) (Principal, error) {
	t, err := s.tickets.Redeem(ctx, ticket)
	if err != nil {
		if errors.Is(err, ErrTicketInvalid) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, err
	}
	sess, err := s.store.Sessions().Get(ctx, t.SessionID)
	if errors.Is(err, postgres.ErrNotFound) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	if sess.UserID != t.UserID || !sess.Active(s.now()) {
		return Principal{}, ErrUnauthenticated
	}
	return Principal(t), nil
}
