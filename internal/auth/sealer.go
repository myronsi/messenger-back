package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const sealedPrefix = "v1:"

// ErrSealed is returned when a sealed value cannot be opened: wrong key, wrong context, or corruption.
var ErrSealed = errors.New("cannot open sealed value")

// Sealer encrypts small secrets (two-factor seeds) with AES-256-GCM. The associated data binds a value to
// its owner, so a sealed secret copied to another account does not open.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer returns a Sealer for a 32-byte key.
func NewSealer(key []byte) (*Sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("encryption key must be 32 bytes")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal returns "v1:" followed by base64url(nonce || ciphertext).
func (s *Sealer) Seal(plain, aad string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("read random nonce: %w", err)
	}
	out := s.aead.Seal(nonce, nonce, []byte(plain), []byte(aad))
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open reverses Seal.
func (s *Sealer) Open(sealed, aad string) (string, error) {
	enc, ok := strings.CutPrefix(sealed, sealedPrefix)
	if !ok {
		return "", ErrSealed
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", ErrSealed
	}
	plain, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], []byte(aad))
	if err != nil {
		return "", ErrSealed
	}
	return string(plain), nil
}
