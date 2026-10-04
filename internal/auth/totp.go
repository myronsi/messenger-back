package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	totpPeriod = 30
	totpIssuer = "Messenger"

	recoveryCodeCount = 10
)

var totpOpts = totp.ValidateOpts{Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

// NewTOTPSecret returns a random base32 secret (160 bits), the format authenticator apps expect.
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// TOTPURI is the otpauth URI that QR codes carry.
func TOTPURI(username, secret string) string {
	label := url.PathEscape(totpIssuer + ":" + username)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", totpIssuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// VerifyTOTP checks a 6-digit code against the current 30-second step and its neighbours (clock drift of a
// phone). It returns the matching step, which callers record to refuse a second use of the same code.
func VerifyTOTP(secret, code string, now time.Time) (step int64, ok bool) {
	if len(code) != 6 {
		return 0, false
	}
	current := now.Unix() / totpPeriod
	match := int64(-1)
	// Every step is compared, so the time taken does not tell which one matched.
	for _, delta := range []int64{-1, 0, 1} {
		want, err := totp.GenerateCodeCustom(secret, time.Unix((current+delta)*totpPeriod, 0), totpOpts)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			match = current + delta
		}
	}
	return match, match >= 0
}

// NewRecoveryCodes returns ten one-time codes in the form XXXXXXXX-XXXXXXXX (hex).
func NewRecoveryCodes() ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("read random code: %w", err)
		}
		h := strings.ToUpper(hex.EncodeToString(b))
		codes[i] = h[:8] + "-" + h[8:]
	}
	return codes, nil
}

// NormalizeRecoveryCode removes separators and upper-cases, so users can type a code the way they like.
func NormalizeRecoveryCode(code string) string {
	r := strings.NewReplacer("-", "", " ", "")
	return strings.ToUpper(r.Replace(strings.TrimSpace(code)))
}

// IsRecoveryCode reports whether the input has the shape of a recovery code (not a TOTP code).
func IsRecoveryCode(code string) bool {
	n := NormalizeRecoveryCode(code)
	if len(n) != 16 {
		return false
	}
	_, err := hex.DecodeString(n)
	return err == nil
}

// HashRecoveryCode is the stored form of a recovery code. A plain SHA-256 is enough because the codes are
// 64 random bits and single use, and it matches the hashes the Python backend wrote.
func HashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(NormalizeRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}
