package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var testSecret = []byte(strings.Repeat("k", 40))

func newTokens(t *testing.T) *Tokens {
	t.Helper()
	tk, err := NewTokens(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestTokenRoundTrip(t *testing.T) {
	tk := newTokens(t)
	sid := uuid.New()
	raw, exp, err := tk.Issue(TypeAccess, Claims{UserID: 42, SessionID: sid}, AccessTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d < 14*time.Minute || d > 15*time.Minute {
		t.Fatalf("expiry in %v", d)
	}
	got, err := tk.Parse(raw, TypeAccess)
	if err != nil || got.UserID != 42 || got.SessionID != sid {
		t.Fatalf("parse: %+v %v", got, err)
	}
}

func TestShortSecretRefused(t *testing.T) {
	if _, err := NewTokens([]byte("short")); err == nil {
		t.Fatal("a short secret was accepted")
	}
}

func signWith(t *testing.T, method jwt.SigningMethod, key any, c jwt.Claims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, c).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestForgedAndInvalidTokensAreRejected(t *testing.T) {
	tk := newTokens(t)
	sid := uuid.NewString()
	valid := func() wireClaims {
		return wireClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer: tokenIssuer, Subject: "1", Audience: jwt.ClaimStrings{"messenger:access"},
				IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
			},
			SessionID: sid, Type: TypeAccess,
		}
	}
	// Sanity: the helper's claims are accepted with the right key.
	if _, err := tk.Parse(signWith(t, jwt.SigningMethodHS256, testSecret, valid()), TypeAccess); err != nil {
		t.Fatalf("baseline token rejected: %v", err)
	}

	mut := func(f func(c *wireClaims)) wireClaims { c := valid(); f(&c); return c }
	tests := map[string]string{
		"wrong key":    signWith(t, jwt.SigningMethodHS256, []byte(strings.Repeat("x", 40)), valid()),
		"alg none":     signWith(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, valid()),
		"hs512":        signWith(t, jwt.SigningMethodHS512, testSecret, valid()),
		"expired":      signWith(t, jwt.SigningMethodHS256, testSecret, mut(func(c *wireClaims) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute)) })),
		"no expiry":    signWith(t, jwt.SigningMethodHS256, testSecret, mut(func(c *wireClaims) { c.ExpiresAt = nil })),
		"wrong type":   signWith(t, jwt.SigningMethodHS256, testSecret, mut(func(c *wireClaims) { c.Type = TypeMedia })),
		"no type":      signWith(t, jwt.SigningMethodHS256, testSecret, mut(func(c *wireClaims) { c.Type = "" })),
		"wrong aud":    signWith(t, jwt.SigningMethodHS256, testSecret, mut(func(c *wireClaims) { c.Audience = jwt.ClaimStrings{"messenger:media"} })),
		"no aud":       signWith(t, jwt.SigningMethodHS256, testSecret, mut(func(c *wireClaims) { c.Audience = nil })),
		"wrong issuer": signWith(t, jwt.SigningMethodHS256, testSecret, mut(func(c *wireClaims) { c.Issuer = "someone" })),
		"bad subject":  signWith(t, jwt.SigningMethodHS256, testSecret, mut(func(c *wireClaims) { c.Subject = "abc" })),
		"zero subject": signWith(t, jwt.SigningMethodHS256, testSecret, mut(func(c *wireClaims) { c.Subject = "0" })),
		"bad session":  signWith(t, jwt.SigningMethodHS256, testSecret, mut(func(c *wireClaims) { c.SessionID = "1" })),
		"empty":        "",
		"garbage":      "a.b.c",
	}
	for name, raw := range tests {
		if _, err := tk.Parse(raw, TypeAccess); err == nil {
			t.Errorf("%s: token accepted", name)
		}
	}

	// A token of another type is refused where an access token is needed, and the other way round.
	media, _, _ := tk.Issue(TypeMedia, Claims{UserID: 1, SessionID: uuid.New()}, time.Minute)
	if _, err := tk.Parse(media, TypeAccess); err == nil {
		t.Error("a media token was accepted as an access token")
	}
	access, _, _ := tk.Issue(TypeAccess, Claims{UserID: 1, SessionID: uuid.New()}, time.Minute)
	if _, err := tk.Parse(access, TypeMedia); err == nil {
		t.Error("an access token was accepted as a media token")
	}

	// Tampering with the payload breaks the signature.
	parts := strings.Split(access, ".")
	forged := parts[0] + "." + strings.Replace(parts[1], parts[1][:4], "AAAA", 1) + "." + parts[2]
	if _, err := tk.Parse(forged, TypeAccess); err == nil {
		t.Error("a tampered payload was accepted")
	}
}

func TestExpiryFollowsTheClock(t *testing.T) {
	tk := newTokens(t)
	raw, _, _ := tk.Issue(TypeAccess, Claims{UserID: 1, SessionID: uuid.New()}, time.Minute)
	tk.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := tk.Parse(raw, TypeAccess); err == nil {
		t.Fatal("an expired token was accepted")
	}
}
