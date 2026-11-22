package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	DeviceProofHeader     = "Device-Proof"
	deviceProofMaxLen     = 2048
	deviceProofMaxSkew    = 5 * time.Minute
	deviceProofReplayTTL  = 2*deviceProofMaxSkew + time.Minute
	deviceProofMaxJTILen  = 64
	deviceProofType       = "dpop+jwt"
	deviceProofAlgorithm  = "ES256"
	deviceProofCoordBytes = 32
)

var (
	ErrInvalidDeviceProof = errors.New("invalid device proof")
	ErrDeviceProofStale   = errors.New("device proof issued at is out of range")
	ErrDeviceKeyMismatch  = errors.New("refresh without the device key of the session")
)

type DeviceKey struct {
	Thumbprint string
}

type deviceProofJWK struct {
	Kty string  `json:"kty"`
	Crv string  `json:"crv"`
	X   string  `json:"x"`
	Y   string  `json:"y"`
	D   *string `json:"d,omitempty"`
}

type deviceProofHeader struct {
	Typ string          `json:"typ"`
	Alg string          `json:"alg"`
	JWK *deviceProofJWK `json:"jwk"`
}

type deviceProofClaims struct {
	HTM string `json:"htm"`
	HTU string `json:"htu"`
	IAT *int64 `json:"iat"`
	JTI string `json:"jti"`
}

func decodeSegment(seg string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (k deviceProofJWK) publicKey() (*ecdsa.PublicKey, error) {
	if k.Kty != "EC" || k.Crv != "P-256" || k.D != nil {
		return nil, ErrInvalidDeviceProof
	}
	x, errX := base64.RawURLEncoding.DecodeString(k.X)
	y, errY := base64.RawURLEncoding.DecodeString(k.Y)
	if errX != nil || errY != nil || len(x) != deviceProofCoordBytes || len(y) != deviceProofCoordBytes {
		return nil, ErrInvalidDeviceProof
	}
	point := append(append([]byte{4}, x...), y...)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		return nil, ErrInvalidDeviceProof
	}
	return pub, nil
}

func (k deviceProofJWK) thumbprint() string {
	canonical := fmt.Sprintf(`{"crv":"P-256","kty":"EC","x":"%s","y":"%s"}`, k.X, k.Y)
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func proofPathMatches(htu, operation string) bool {
	u, err := url.Parse(htu)
	if err != nil || u.Path == "" || operation == "" || !strings.HasPrefix(operation, "/") {
		return false
	}
	p := path.Clean(u.Path)
	return p == operation || strings.HasSuffix(p, operation)
}

func (s *Service) VerifyDeviceProof(ctx context.Context, raw, method, operation string) (DeviceKey, error) {
	if raw == "" || len(raw) > deviceProofMaxLen {
		return DeviceKey{}, ErrInvalidDeviceProof
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return DeviceKey{}, ErrInvalidDeviceProof
	}
	var head deviceProofHeader
	if err := decodeSegment(parts[0], &head); err != nil || head.Typ != deviceProofType || head.Alg != deviceProofAlgorithm || head.JWK == nil {
		return DeviceKey{}, ErrInvalidDeviceProof
	}
	pub, err := head.JWK.publicKey()
	if err != nil {
		return DeviceKey{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 2*deviceProofCoordBytes {
		return DeviceKey{}, ErrInvalidDeviceProof
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:deviceProofCoordBytes])
	sv := new(big.Int).SetBytes(sig[deviceProofCoordBytes:])
	if !ecdsa.Verify(pub, digest[:], r, sv) {
		return DeviceKey{}, ErrInvalidDeviceProof
	}
	var claims deviceProofClaims
	if err := decodeSegment(parts[1], &claims); err != nil || claims.IAT == nil {
		return DeviceKey{}, ErrInvalidDeviceProof
	}
	if !strings.EqualFold(claims.HTM, method) || !proofPathMatches(claims.HTU, operation) {
		return DeviceKey{}, ErrInvalidDeviceProof
	}
	if n := len(claims.JTI); n == 0 || n > deviceProofMaxJTILen {
		return DeviceKey{}, ErrInvalidDeviceProof
	}
	issued := time.Unix(*claims.IAT, 0)
	if skew := s.now().Sub(issued); skew > deviceProofMaxSkew || skew < -deviceProofMaxSkew {
		return DeviceKey{}, ErrDeviceProofStale
	}
	key := DeviceKey{Thumbprint: head.JWK.thumbprint()}
	fresh, err := s.proofs.SetNX(ctx, s.proofKey(key.Thumbprint, claims.JTI), 1, deviceProofReplayTTL).Result()
	if err != nil {
		return DeviceKey{}, fmt.Errorf("record device proof: %w", err)
	}
	if !fresh {
		return DeviceKey{}, ErrInvalidDeviceProof
	}
	return key, nil
}

func (s *Service) proofKey(thumbprint, jti string) string {
	return s.ns + ":dproof:" + digest(thumbprint+"|"+jti)
}
