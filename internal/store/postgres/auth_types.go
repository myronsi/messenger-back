package postgres

import (
	"context"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// DefaultSessionDays is the session lifetime of an account that never changed the setting.
const DefaultSessionDays = 90

// Session is a login of a user on one device. The refresh token itself is never stored, only its hash.
type Session struct {
	ID           uuid.UUID
	UserID       int64
	UserAgent    *string
	IP           *netip.Addr
	CreatedAt    time.Time
	LastActiveAt time.Time
	ExpiresAt    time.Time
	RevokedAt    *time.Time
}

// Active reports whether the session can still be used at the given time.
func (s Session) Active(now time.Time) bool {
	return s.RevokedAt == nil && s.ExpiresAt.After(now)
}

// NewSession describes a session to create. RefreshTokenHash is the SHA-256 (hex) of the refresh token.
type NewSession struct {
	UserID           int64
	RefreshTokenHash string
	UserAgent        *string
	IP               *netip.Addr
	ExpiresAt        time.Time
}

// RotateOutcome is the result of presenting a refresh token.
type RotateOutcome int

// The outcomes of SessionRepository.Rotate.
const (
	// RotateInvalid: the token is unknown, expired, revoked, or the stale token of a rotation that just
	// happened (a second tab racing the first). Nothing changed.
	RotateInvalid RotateOutcome = iota
	// RotateOK: the token was replaced; Session carries the new expiry.
	RotateOK
	// RotateReuse: a token that was rotated out earlier came back, so the session was revoked.
	RotateReuse
)

// RotateRequest is a refresh. NewExpiresAt is used when the rotation succeeds.
type RotateRequest struct {
	PresentedHash string
	NewHash       string
	NewExpiresAt  func(sessionDays int) time.Time
	UserAgent     *string
	IP            *netip.Addr
	// ReuseGrace is how long after a rotation the previous token is answered with RotateInvalid instead
	// of being treated as theft.
	ReuseGrace time.Duration
}

// RotateResult carries the session for RotateOK and RotateReuse.
type RotateResult struct {
	Outcome RotateOutcome
	Session Session
}

// SessionRepository stores sessions and their refresh token hashes.
type SessionRepository interface {
	Create(ctx context.Context, s NewSession) (Session, error)
	// Rotate swaps the refresh token of the session that owns PresentedHash for NewHash in one
	// transaction, and detects the reuse of an already rotated token.
	Rotate(ctx context.Context, r RotateRequest) (RotateResult, error)
	Get(ctx context.Context, id uuid.UUID) (Session, error)
	// ByRefreshHash finds the session a refresh token belongs to (ErrNotFound for none).
	ByRefreshHash(ctx context.Context, hash string) (Session, error)
	// ListActive returns the sessions that are neither revoked nor expired, most recently used first.
	ListActive(ctx context.Context, userID int64) ([]Session, error)
	// Revoke revokes one session of the user; ErrNotFound when it is not theirs or already revoked.
	Revoke(ctx context.Context, userID int64, id uuid.UUID) error
	// RevokeByID revokes a session without knowing the owner; reports whether one was revoked.
	RevokeByID(ctx context.Context, id uuid.UUID) (bool, error)
	// RevokeOthers revokes every session of the user except keepID and returns their ids.
	RevokeOthers(ctx context.Context, userID int64, keepID uuid.UUID) ([]uuid.UUID, error)
	// RevokeAll revokes every session of the user and returns their ids.
	RevokeAll(ctx context.Context, userID int64) ([]uuid.UUID, error)
	// Touch records activity. Callers throttle it.
	Touch(ctx context.Context, id uuid.UUID) error
}

// SecuritySettings are the per-account security options. The two-factor secrets are stored sealed
// (encrypted); this package never sees them in the clear.
type SecuritySettings struct {
	SessionDurationDays    int
	TwoFactorEnabled       bool
	TwoFactorSecret        *string
	PendingTwoFactorSecret *string
}

// SecuritySettingsRepository stores the security options and the second factor of an account.
type SecuritySettingsRepository interface {
	// Get returns the settings, or the defaults for an account that never changed them.
	Get(ctx context.Context, userID int64) (SecuritySettings, error)
	SetSessionDuration(ctx context.Context, userID int64, days int) error
	// BeginTwoFactor stores a pending secret; ErrConflict when two-factor is already on.
	BeginTwoFactor(ctx context.Context, userID int64, sealedSecret string) error
	// ConfirmTwoFactor turns two-factor on if the pending secret is still sealedPending, and replaces
	// the recovery codes (hashes) in the same transaction; ErrConflict otherwise.
	ConfirmTwoFactor(ctx context.Context, userID int64, sealedPending string, recoveryCodeHashes []string) error
	// DisableTwoFactor turns it off and deletes the secrets and recovery codes.
	DisableTwoFactor(ctx context.Context, userID int64) error
	// UseRecoveryCode consumes one unused code; false when there is no such code.
	UseRecoveryCode(ctx context.Context, userID int64, codeHash string) (bool, error)
	CountRecoveryCodes(ctx context.Context, userID int64) (int, error)
}

// PasswordReset is the outcome of consuming a recovery token.
type PasswordReset struct {
	UserID          int64
	RevokedSessions []uuid.UUID
}

// RecoveryTokenRepository stores single-use account recovery tokens (hashes only).
type RecoveryTokenRepository interface {
	Issue(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error
	// ResetPassword consumes the token, sets the password and revokes every session, atomically.
	// ErrNotFound when the token is unknown, used or expired.
	ResetPassword(ctx context.Context, tokenHash, newPasswordHash string) (PasswordReset, error)
	// DeleteStale removes tokens that expired long ago.
	DeleteStale(ctx context.Context) (int64, error)
}
