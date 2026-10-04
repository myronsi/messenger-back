package auth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parameters of newly created hashes; the same as the argon2-cffi defaults the Python backend used.
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 4
	argonKeyLen    = 32
	argonSaltLen   = 16

	// Upper bounds for the parameters of a stored hash, so a corrupted row cannot make a login allocate gigabytes.
	maxArgonMemoryKiB = 512 * 1024
	maxArgonTime      = 16
	maxArgonThreads   = 16
	// Bounds for the salt and the derived key of a stored hash (the key length is also the output size).
	maxSaltLen = 64
	minKeyLen  = 16
	maxKeyLen  = 64

	legacyPBKDF2Iterations = 100_000
	// v1Prefix marks hashes written by the Python backend: "argon2$" followed by the PHC string.
	v1Prefix = "argon2$"
)

// ErrHashFormat is returned for a stored hash in no known format.
var ErrHashFormat = errors.New("unsupported password hash format")

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
	keyLen  uint32
}

var currentParams = argonParams{memory: argonMemoryKiB, time: argonTime, threads: argonThreads, keyLen: argonKeyLen}

// Hasher hashes and verifies passwords. Argon2id runs use 64 MiB each, so the number of concurrent runs is
// bounded: a burst of logins queues instead of exhausting memory.
type Hasher struct {
	slots chan struct{}
}

// NewHasher returns a Hasher that runs at most maxConcurrent hashes at a time (minimum 1).
func NewHasher(maxConcurrent int) *Hasher {
	return &Hasher{slots: make(chan struct{}, max(maxConcurrent, 1))}
}

func (h *Hasher) acquire(ctx context.Context) (release func(), err error) {
	select {
	case h.slots <- struct{}{}:
		return func() { <-h.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Hash returns an argon2id hash of the password as a PHC string.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	release, err := h.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read random salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, currentParams.time, currentParams.memory, currentParams.threads, currentParams.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		currentParams.memory, currentParams.time, currentParams.threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks the password against a stored hash in any supported format. needsRehash is true when the
// password is right but the hash is a legacy format or uses weaker parameters than the current ones.
func (h *Hasher) Verify(ctx context.Context, password, stored string) (ok, needsRehash bool, err error) {
	release, err := h.acquire(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()

	phc := strings.TrimPrefix(stored, v1Prefix)
	switch {
	case strings.HasPrefix(phc, "$argon2id$"):
		return verifyArgon2id(password, phc, stored != phc)
	case strings.HasPrefix(stored, "$argon2"):
		// argon2i and argon2d are never produced by this system.
		return false, false, ErrHashFormat
	}
	if salt, want, found := parseLegacyPBKDF2(stored); found {
		got, err := pbkdf2.Key(sha256.New, password, salt, legacyPBKDF2Iterations, len(want))
		if err != nil {
			return false, false, err
		}
		ok = subtle.ConstantTimeCompare(got, want) == 1
		return ok, ok, nil
	}
	return false, false, ErrHashFormat
}

func verifyArgon2id(password, phc string, prefixed bool) (ok, needsRehash bool, err error) {
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return false, false, ErrHashFormat
	}
	var p argonParams
	var m, t, th uint64
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, found := strings.Cut(kv, "=")
		if !found {
			return false, false, ErrHashFormat
		}
		n, perr := strconv.ParseUint(v, 10, 32)
		if perr != nil {
			return false, false, ErrHashFormat
		}
		switch k {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			th = n
		default:
			return false, false, ErrHashFormat
		}
	}
	if m == 0 || t == 0 || th == 0 || m > maxArgonMemoryKiB || t > maxArgonTime || th > maxArgonThreads {
		return false, false, ErrHashFormat
	}
	if len(parts[4]) > base64.RawStdEncoding.EncodedLen(maxSaltLen) || len(parts[5]) > base64.RawStdEncoding.EncodedLen(maxKeyLen) {
		return false, false, ErrHashFormat
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, false, ErrHashFormat
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < minKeyLen {
		return false, false, ErrHashFormat
	}
	p = argonParams{memory: uint32(m), time: uint32(t), threads: uint8(th), keyLen: uint32(len(want))} //nolint:gosec // bounded above
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	ok = subtle.ConstantTimeCompare(got, want) == 1
	return ok, ok && (prefixed || p != currentParams), nil
}

// parseLegacyPBKDF2 reads the first format of the Python backend: base64(salt) ":" base64(hash).
func parseLegacyPBKDF2(stored string) (salt, hash []byte, ok bool) {
	s, h, found := strings.Cut(stored, ":")
	if !found {
		return nil, nil, false
	}
	if len(s) > base64.StdEncoding.EncodedLen(maxSaltLen) || len(h) > base64.StdEncoding.EncodedLen(maxKeyLen) {
		return nil, nil, false
	}
	salt, err1 := base64.StdEncoding.DecodeString(s)
	hash, err2 := base64.StdEncoding.DecodeString(h)
	if err1 != nil || err2 != nil || len(salt) == 0 || len(hash) == 0 {
		return nil, nil, false
	}
	return salt, hash, true
}

// dummyHash is a valid hash of a random password. Verifying against it for unknown usernames keeps the
// response time of a miss close to that of a wrong password.
func (h *Hasher) dummyHash(ctx context.Context) (string, error) {
	pw := make([]byte, 16)
	if _, err := rand.Read(pw); err != nil {
		return "", err
	}
	return h.Hash(ctx, base64.RawStdEncoding.EncodeToString(pw))
}
