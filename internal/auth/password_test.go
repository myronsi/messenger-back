package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

// Hashes produced by the Python backend: argon2-cffi's PasswordHasher().hash(password) with the "argon2$"
// prefix of server/routes/auth.py, and the legacy PBKDF2-HMAC-SHA256 "salt:hash" format (100000 rounds).
const (
	v1Argon2        = "argon2$$argon2id$v=19$m=65536,t=3,p=4$Ic52iBBWhHAhWzTl5iPTGA$6LnmSHUDPgPQuGYE15MmN/PqRJvw4opZz9Dp3Vm0pFQ"
	v1Argon2Unicode = "argon2$$argon2id$v=19$m=65536,t=3,p=4$bH26+i7/WrzLTrmnFRMyNw$bcjpNGKhNf3StGy9ASOUp9bqHLdIAgw0+enZ3Gq5FBM"
	v1PBKDF2        = "ABEiM0RVZneImaq7zN3u/w==:dJECleQYdOWCbfTP9YpShOyW1/oho4vDaPC+m2OO4wM="
)

func TestVerifyV1Hashes(t *testing.T) {
	h := NewHasher(2)
	ctx := context.Background()
	tests := []struct {
		name, stored, password string
	}{
		{"argon2-cffi", v1Argon2, "correct horse battery"},
		{"argon2-cffi unicode", v1Argon2Unicode, "пароль-ключ-123"},
		{"legacy pbkdf2", v1PBKDF2, "legacy-password"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, rehash, err := h.Verify(ctx, tc.password, tc.stored)
			if err != nil || !ok {
				t.Fatalf("right password: ok=%v err=%v", ok, err)
			}
			if !rehash {
				t.Fatal("a v1 hash must be upgraded to the new format on login")
			}
			ok, _, err = h.Verify(ctx, tc.password+"x", tc.stored)
			if err != nil || ok {
				t.Fatalf("wrong password: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestHashRoundTripAndRehash(t *testing.T) {
	h := NewHasher(1)
	ctx := context.Background()
	hash, err := h.Hash(ctx, "s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("unexpected format: %s", hash)
	}
	ok, rehash, err := h.Verify(ctx, "s3cret-pass", hash)
	if err != nil || !ok || rehash {
		t.Fatalf("fresh hash: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, _, _ := h.Verify(ctx, "other", hash); ok {
		t.Fatal("wrong password accepted")
	}
	other, _ := h.Hash(ctx, "s3cret-pass")
	if other == hash {
		t.Fatal("two hashes of one password are identical: the salt is not random")
	}

	// Weaker parameters are upgraded.
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("s3cret-pass"), salt, 2, 19456, 1, 32)
	weak := "$argon2id$v=19$m=19456,t=2,p=1$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key)
	if ok, rehash, err := h.Verify(ctx, "s3cret-pass", weak); err != nil || !ok || !rehash {
		t.Fatalf("weak params: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	// A wrong password never asks for a rehash, whatever the format.
	if ok, rehash, err := h.Verify(ctx, "wrong", weak); err != nil || ok || rehash {
		t.Fatalf("wrong password on weak params: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, rehash, err := h.Verify(ctx, "wrong", base64.StdEncoding.EncodeToString(salt)+":"+base64.StdEncoding.EncodeToString(key)); err != nil || ok || rehash {
		t.Fatalf("wrong password on PBKDF2: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	h := NewHasher(1)
	for _, stored := range []string{
		"",
		"plaintext",
		"c2FsdHNhbHQ=:AA==", // a one-byte PBKDF2 hash would accept 1 password in 256
		"c2FsdHNhbHQ=:" + base64.StdEncoding.EncodeToString(make([]byte, 31)),
		"argon2$",
		"$argon2id$v=19$m=65536,t=3,p=4$onlysalt",
		"$argon2id$v=18$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=65536,t=3$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=4194304,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", // would allocate 4 GiB
		"$argon2id$v=19$m=65536,t=99999,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2i$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$" + strings.Repeat("A", 4000),                 // a huge derived key
		"$argon2id$v=19$m=65536,t=3,p=4$" + strings.Repeat("A", 4000) + "$aGFzaGhhc2hoYXNoaGFzaA", // a huge salt
		"c2FsdA==:" + strings.Repeat("A", 4000),
		"not-base64:also-not",
	} {
		ok, _, err := h.Verify(context.Background(), "x", stored)
		if ok || !errors.Is(err, ErrHashFormat) {
			t.Errorf("%q: ok=%v err=%v", stored, ok, err)
		}
	}
}

func TestHasherBoundsConcurrency(t *testing.T) {
	h := NewHasher(1)
	release, err := h.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a full hasher must wait for the context, got %v", err)
	}
}
