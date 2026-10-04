package auth

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TokenType says what a JWT may be used for. Every parse names the type it expects, so a token minted for
// one purpose is never accepted for another.
type TokenType string

// Token types. TypeMedia is reserved for short-lived media URLs.
const (
	TypeAccess TokenType = "access"
	TypeMedia  TokenType = "media"
)

const (
	tokenIssuer = "messenger"
	// AccessTokenTTL is the lifetime of an access token.
	AccessTokenTTL = 15 * time.Minute
	// MinJWTSecretLen is the shortest signing secret accepted.
	MinJWTSecretLen = 32
)

// ErrInvalidToken is returned for every token that is not valid: forged, expired, of the wrong type,
// audience or algorithm. Callers must not tell the client which.
var ErrInvalidToken = errors.New("invalid token")

// ErrTokenExpired is returned for a genuine token that is past its expiry.
var ErrTokenExpired = errors.New("token expired")

// Claims is the content of a verified token.
type Claims struct {
	UserID    int64
	SessionID uuid.UUID
}

type wireClaims struct {
	jwt.RegisteredClaims
	SessionID string    `json:"sid"`
	Type      TokenType `json:"typ"`
}

// Tokens signs and verifies JWTs with HS256.
type Tokens struct {
	secret []byte
	now    func() time.Time
}

// NewTokens returns a signer for the secret (at least MinJWTSecretLen bytes).
func NewTokens(secret []byte) (*Tokens, error) {
	if len(secret) < MinJWTSecretLen {
		return nil, fmt.Errorf("jwt secret must be at least %d bytes", MinJWTSecretLen)
	}
	return &Tokens{secret: secret, now: time.Now}, nil
}

func audienceFor(t TokenType) string { return "messenger:" + string(t) }

// Issue creates a signed token of the given type that expires after ttl.
func (t *Tokens) Issue(typ TokenType, c Claims, ttl time.Duration) (string, time.Time, error) {
	now := t.now()
	exp := now.Add(ttl)
	claims := wireClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    tokenIssuer,
			Subject:   strconv.FormatInt(c.UserID, 10),
			Audience:  jwt.ClaimStrings{audienceFor(typ)},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        uuid.NewString(),
		},
		SessionID: c.SessionID.String(),
		Type:      typ,
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return s, exp, nil
}

// Parse verifies the signature (HS256 only), expiry, issuer, audience and type.
func (t *Tokens) Parse(raw string, want TokenType) (Claims, error) {
	var wc wireClaims
	_, err := jwt.ParseWithClaims(raw, &wc,
		func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithAudience(audienceFor(want)),
		jwt.WithTimeFunc(t.now),
	)
	if err != nil || wc.Type != want {
		// Only a token that was genuine (signature, audience and type checked) is reported as expired, so
		// the client knows to refresh instead of signing in again.
		if errors.Is(err, jwt.ErrTokenExpired) && wc.Type == want {
			return Claims{}, ErrTokenExpired
		}
		return Claims{}, ErrInvalidToken
	}
	uid, err := strconv.ParseInt(wc.Subject, 10, 64)
	if err != nil || uid <= 0 {
		return Claims{}, ErrInvalidToken
	}
	sid, err := uuid.Parse(wc.SessionID)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	return Claims{UserID: uid, SessionID: sid}, nil
}
