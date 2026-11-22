package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type testDevice struct {
	key *ecdsa.PrivateKey
}

func newTestDevice(t *testing.T) testDevice {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testDevice{key: k}
}

func (d testDevice) jwk() map[string]any {
	raw, err := d.key.PublicKey.Bytes()
	if err != nil {
		panic(err)
	}
	return map[string]any{
		"kty": "EC", "crv": "P-256",
		"x": base64.RawURLEncoding.EncodeToString(raw[1:33]),
		"y": base64.RawURLEncoding.EncodeToString(raw[33:65]),
	}
}

func (d testDevice) thumbprint() string {
	canonical, err := json.Marshal(d.jwk())
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(canonical)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (d testDevice) signRaw(t *testing.T, header, claims map[string]any) string {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	c, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, d.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (d testDevice) proof(t *testing.T, method, url string, iat time.Time) string {
	t.Helper()
	return d.signRaw(t,
		map[string]any{"typ": "dpop+jwt", "alg": "ES256", "jwk": d.jwk()},
		map[string]any{"htm": method, "htu": url, "iat": iat.Unix(), "jti": uuid.NewString()},
	)
}

func newProofService(t *testing.T) *Service {
	t.Helper()
	rdb, ns := testRedis(t)
	svc, err := NewService(nil, rdb, Config{
		JWTSecret:      []byte(strings.Repeat("j", 40)),
		EncryptionKey:  []byte(strings.Repeat("k", 32)),
		RecoveryPepper: []byte(strings.Repeat("p", 32)),
		Namespace:      ns,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

const refreshURL = "https://chat.example/api/v2/auth/refresh"

func TestVerifyDeviceProofAcceptsAValidProofOnce(t *testing.T) {
	svc := newProofService(t)
	ctx := context.Background()
	d := newTestDevice(t)
	raw := d.proof(t, "POST", refreshURL, time.Now())

	key, err := svc.VerifyDeviceProof(ctx, raw, "POST", "/auth/refresh")
	if err != nil {
		t.Fatal(err)
	}
	if key.Thumbprint != d.thumbprint() || len(key.Thumbprint) != 43 {
		t.Fatalf("thumbprint %q, want the RFC 7638 thumbprint %q", key.Thumbprint, d.thumbprint())
	}
	if _, err := svc.VerifyDeviceProof(ctx, raw, "POST", "/auth/refresh"); !errors.Is(err, ErrInvalidDeviceProof) {
		t.Fatalf("replayed proof: got %v", err)
	}
	other, err := svc.VerifyDeviceProof(ctx, d.proof(t, "post", "http://localhost:8080/api/v2/auth/refresh?x=1", time.Now()), "POST", "/auth/refresh")
	if err != nil || other.Thumbprint != key.Thumbprint {
		t.Fatalf("a new proof of the same key: %v %q", err, other.Thumbprint)
	}
}

func TestVerifyDeviceProofStaleIssuedAt(t *testing.T) {
	svc := newProofService(t)
	ctx := context.Background()
	d := newTestDevice(t)
	for name, iat := range map[string]time.Time{"old": time.Now().Add(-6 * time.Minute), "future": time.Now().Add(6 * time.Minute)} {
		if _, err := svc.VerifyDeviceProof(ctx, d.proof(t, "POST", refreshURL, iat), "POST", "/auth/refresh"); !errors.Is(err, ErrDeviceProofStale) {
			t.Fatalf("%s iat: got %v", name, err)
		}
	}
	if _, err := svc.VerifyDeviceProof(ctx, d.proof(t, "POST", refreshURL, time.Now().Add(-4*time.Minute)), "POST", "/auth/refresh"); err != nil {
		t.Fatalf("iat within the skew: %v", err)
	}
}

func TestVerifyDeviceProofRejectsBadProofs(t *testing.T) {
	svc := newProofService(t)
	ctx := context.Background()
	d := newTestDevice(t)
	other := newTestDevice(t)
	now := time.Now().Unix()
	header := func(change func(map[string]any)) map[string]any {
		h := map[string]any{"typ": "dpop+jwt", "alg": "ES256", "jwk": d.jwk()}
		change(h)
		return h
	}
	claims := func(change func(map[string]any)) map[string]any {
		c := map[string]any{"htm": "POST", "htu": refreshURL, "iat": now, "jti": uuid.NewString()}
		change(c)
		return c
	}
	keep := func(map[string]any) {}
	valid := d.proof(t, "POST", refreshURL, time.Now())
	parts := strings.Split(valid, ".")
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"htm":"POST","htu":"`+refreshURL+`","iat":1,"jti":"x"}`)) + "." + parts[2]
	foreign := other.signRaw(t, header(keep), claims(keep))
	privateJWK := d.jwk()
	privateJWK["d"] = "AAAA"
	rsaJWK := d.jwk()
	rsaJWK["kty"] = "RSA"
	offCurve := d.jwk()
	offCurve["y"] = offCurve["x"]

	cases := map[string]struct {
		raw       string
		method    string
		operation string
	}{
		"empty":           {"", "POST", "/auth/refresh"},
		"not a jws":       {"abc.def", "POST", "/auth/refresh"},
		"too long":        {strings.Repeat("a", 3000), "POST", "/auth/refresh"},
		"tampered":        {tampered, "POST", "/auth/refresh"},
		"other signer":    {foreign, "POST", "/auth/refresh"},
		"hs256":           {d.signRaw(t, header(func(h map[string]any) { h["alg"] = "HS256" }), claims(keep)), "POST", "/auth/refresh"},
		"none":            {d.signRaw(t, header(func(h map[string]any) { h["alg"] = "none" }), claims(keep)), "POST", "/auth/refresh"},
		"wrong typ":       {d.signRaw(t, header(func(h map[string]any) { h["typ"] = "JWT" }), claims(keep)), "POST", "/auth/refresh"},
		"no jwk":          {d.signRaw(t, header(func(h map[string]any) { delete(h, "jwk") }), claims(keep)), "POST", "/auth/refresh"},
		"private jwk":     {d.signRaw(t, header(func(h map[string]any) { h["jwk"] = privateJWK }), claims(keep)), "POST", "/auth/refresh"},
		"rsa jwk":         {d.signRaw(t, header(func(h map[string]any) { h["jwk"] = rsaJWK }), claims(keep)), "POST", "/auth/refresh"},
		"point off curve": {d.signRaw(t, header(func(h map[string]any) { h["jwk"] = offCurve }), claims(keep)), "POST", "/auth/refresh"},
		"wrong method":    {d.signRaw(t, header(keep), claims(keep)), "GET", "/auth/refresh"},
		"wrong path":      {d.signRaw(t, header(keep), claims(keep)), "POST", "/auth/login"},
		"longer path":     {d.signRaw(t, header(keep), claims(func(c map[string]any) { c["htu"] = refreshURL + "x" })), "POST", "/auth/refresh"},
		"no iat":          {d.signRaw(t, header(keep), claims(func(c map[string]any) { delete(c, "iat") })), "POST", "/auth/refresh"},
		"no jti":          {d.signRaw(t, header(keep), claims(func(c map[string]any) { delete(c, "jti") })), "POST", "/auth/refresh"},
		"long jti":        {d.signRaw(t, header(keep), claims(func(c map[string]any) { c["jti"] = strings.Repeat("j", 65) })), "POST", "/auth/refresh"},
	}
	for name, tc := range cases {
		if _, err := svc.VerifyDeviceProof(ctx, tc.raw, tc.method, tc.operation); !errors.Is(err, ErrInvalidDeviceProof) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func (e *env) bound(t *testing.T, id uuid.UUID) *string {
	t.Helper()
	sess, err := e.store.Sessions().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return sess.DeviceKeyThumbprint
}

func withKey(c Client, d testDevice) Client {
	c.DeviceKey = &DeviceKey{Thumbprint: d.thumbprint()}
	return c
}

func TestRefreshBindsAnUnboundSessionOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.41")
	d := newTestDevice(t)
	ts := e.register(t, "kim", "correct horse")
	if e.bound(t, ts.SessionID) != nil {
		t.Fatal("a session created without a proof must stay unbound")
	}

	ts, err := e.svc.Refresh(ctx, c, ts.RefreshToken)
	if err != nil {
		t.Fatalf("unbound session without proof: %v", err)
	}
	ts, err = e.svc.Refresh(ctx, withKey(c, d), ts.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.bound(t, ts.SessionID); got == nil || *got != d.thumbprint() {
		t.Fatalf("bound to %v", got)
	}
	ts, err = e.svc.Refresh(ctx, withKey(c, d), ts.RefreshToken)
	if err != nil {
		t.Fatalf("same key again: %v", err)
	}

	if _, err := e.svc.Refresh(ctx, c, ts.RefreshToken); !errors.Is(err, ErrDeviceKeyMismatch) {
		t.Fatalf("bound session without proof: got %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, ts.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("the session must be ended at once, got %v", err)
	}
	if _, err := e.svc.Refresh(ctx, withKey(c, d), ts.RefreshToken); !errors.Is(err, ErrInvalidRefreshToken) {
		t.Fatalf("the real device signs in again, got %v", err)
	}
	events, err := e.store.SecurityEvents().List(ctx, ts.User.ID, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		found = found || ev.Type == "device_key_mismatch"
	}
	if !found {
		t.Fatal("the mismatch must be in the security log")
	}
}

func TestSignInWithProofBindsTheSession(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d := newTestDevice(t)
	thief := newTestDevice(t)
	c := withKey(e.client("10.0.0.42"), d)
	ts, err := e.svc.Register(ctx, c, "lena", "Lena", "correct horse", nil, Consent{})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.bound(t, ts.SessionID); got == nil || *got != d.thumbprint() {
		t.Fatalf("register with proof: bound to %v", got)
	}
	res, err := e.svc.Login(ctx, c, "lena", "correct horse")
	if err != nil || res.Tokens == nil {
		t.Fatalf("login: %v", err)
	}
	if got := e.bound(t, res.Tokens.SessionID); got == nil || *got != d.thumbprint() {
		t.Fatalf("login with proof: bound to %v", got)
	}

	if _, err := e.svc.Refresh(ctx, withKey(e.client("203.0.113.9"), thief), res.Tokens.RefreshToken); !errors.Is(err, ErrDeviceKeyMismatch) {
		t.Fatalf("copied cookie with another key: got %v", err)
	}
	if _, err := e.svc.Refresh(ctx, c, ts.RefreshToken); err != nil {
		t.Fatalf("the other session of the user is not affected: %v", err)
	}
}

func TestRequireDeviceKeyEndsUnboundSessions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.43")
	d := newTestDevice(t)
	old := e.register(t, "mila", "correct horse")
	upgraded := e.register(t, "nora", "correct horse")
	e.svc.requireDeviceKey = true

	if _, err := e.svc.Refresh(ctx, c, old.RefreshToken); !errors.Is(err, ErrDeviceKeyMismatch) {
		t.Fatalf("unbound session without proof: got %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, old.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("got %v", err)
	}
	next, err := e.svc.Refresh(ctx, withKey(c, d), upgraded.RefreshToken)
	if err != nil {
		t.Fatalf("an unbound session that brings a key is upgraded: %v", err)
	}
	if got := e.bound(t, next.SessionID); got == nil || *got != d.thumbprint() {
		t.Fatalf("bound to %v", got)
	}
}

func TestRotateStaleIgnoresTheDeviceKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d := newTestDevice(t)
	c := withKey(e.client("10.0.0.44"), d)
	ts, err := e.svc.Register(ctx, c, "olga", "Olga", "correct horse", nil, Consent{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Refresh(ctx, c, ts.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Refresh(ctx, e.client("10.0.0.44"), ts.RefreshToken); !errors.Is(err, ErrRefreshSuperseded) {
		t.Fatalf("a racing tab keeps the session: got %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, ts.AccessToken); err != nil {
		t.Fatalf("got %v", err)
	}
}
