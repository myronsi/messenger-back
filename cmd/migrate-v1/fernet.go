package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// The Python backend encrypted TOTP secrets (and the server's recovery share) with Fernet, under a key derived
// from SECRET_KEY: urlsafe_b64(HKDF-SHA256(SECRET_KEY, salt=None, info="messenger-totp-encryption-v1", 32)).

const fernetInfo = "messenger-totp-encryption-v1"

// fernetKey derives the Fernet key of the Python backend from its SECRET_KEY.
func fernetKey(secretKey string) ([]byte, error) {
	if len(secretKey) < 32 {
		return nil, errors.New("V1_SECRET_KEY must be the Python backend's SECRET_KEY (32+ characters)")
	}
	return hkdf.Key(sha256.New, []byte(secretKey), nil, fernetInfo, 32)
}

var errFernet = errors.New("fernet: invalid token")

// fernetDecrypt opens a Fernet token: version 0x80, timestamp (8), IV (16), AES-128-CBC ciphertext, HMAC-SHA256
// (32) over everything before it. The first half of the key signs, the second encrypts. The token's age is not
// checked: the secrets were stored for good.
func fernetDecrypt(key []byte, token string) ([]byte, error) {
	if len(key) != 32 {
		return nil, errFernet
	}
	raw, err := base64.URLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		if raw, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(token), "=")); err != nil {
			return nil, errFernet
		}
	}
	if len(raw) < 1+8+16+16+32 || raw[0] != 0x80 || (len(raw)-1-8-16-32)%aes.BlockSize != 0 {
		return nil, errFernet
	}
	body, mac := raw[:len(raw)-32], raw[len(raw)-32:]
	h := hmac.New(sha256.New, key[:16])
	h.Write(body)
	if !hmac.Equal(h.Sum(nil), mac) {
		return nil, errFernet
	}
	iv, ct := body[9:25], body[25:]
	block, err := aes.NewCipher(key[16:])
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(ct))
	// Fernet is AES-CBC with encrypt-then-MAC: the HMAC above was checked before anything is decrypted, so a
	// changed ciphertext never reaches the padding check. CBC is what v1 stored; this only reads it.
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct) // NOSONAR: reading v1's Fernet tokens, authenticated above

	n := int(plain[len(plain)-1])
	if n == 0 || n > aes.BlockSize || n > len(plain) {
		return nil, errFernet
	}
	for _, b := range plain[len(plain)-n:] {
		if int(b) != n {
			return nil, errFernet
		}
	}
	return plain[:len(plain)-n], nil
}
