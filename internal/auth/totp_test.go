package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestTOTP(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil || len(secret) != 32 || strings.ToUpper(secret) != secret {
		t.Fatalf("secret %q %v", secret, err)
	}
	now := time.Unix(1_700_000_010, 0)
	code, err := totp.GenerateCodeCustom(secret, now, totpOpts)
	if err != nil {
		t.Fatal(err)
	}
	step, ok := VerifyTOTP(secret, code, now)
	if !ok || step != now.Unix()/30 {
		t.Fatalf("current code: %d %v", step, ok)
	}
	// One step of drift either way is allowed, two is not.
	if _, ok := VerifyTOTP(secret, code, now.Add(30*time.Second)); !ok {
		t.Error("code from the previous step rejected")
	}
	if _, ok := VerifyTOTP(secret, code, now.Add(-30*time.Second)); !ok {
		t.Error("code from the next step rejected")
	}
	if _, ok := VerifyTOTP(secret, code, now.Add(90*time.Second)); ok {
		t.Error("a stale code was accepted")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "000000 "} {
		if _, ok := VerifyTOTP(secret, bad, now); ok && bad != code {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, ok := VerifyTOTP("not base32 !!", code, now); ok {
		t.Error("an invalid secret verified")
	}
}

func TestTOTPKnownVector(t *testing.T) {
	// RFC 6238 appendix B, SHA-1, T=59 -> 94287082 (8 digits); the 6-digit code is its last six digits.
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32("12345678901234567890")
	if _, ok := VerifyTOTP(secret, "287082", time.Unix(59, 0)); !ok {
		t.Fatal("RFC 6238 test vector rejected")
	}
}

func TestTOTPURI(t *testing.T) {
	got := TOTPURI("alice", "ABC234")
	for _, want := range []string{"otpauth://totp/Messenger:alice?", "secret=ABC234", "issuer=Messenger", "algorithm=SHA1", "digits=6", "period=30"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing %q", got, want)
		}
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := NewRecoveryCodes()
	if err != nil || len(codes) != 10 {
		t.Fatalf("%v %v", codes, err)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 17 || c[8] != '-' || !IsRecoveryCode(c) || seen[c] {
			t.Fatalf("bad or repeated code %q", c)
		}
		seen[c] = true
	}
	c := codes[0]
	want := HashRecoveryCode(c)
	for _, variant := range []string{strings.ToLower(c), strings.ReplaceAll(c, "-", ""), " " + c + " ", strings.ReplaceAll(c, "-", " ")} {
		if HashRecoveryCode(variant) != want {
			t.Errorf("%q hashes differently", variant)
		}
	}
	if HashRecoveryCode(codes[1]) == want {
		t.Error("different codes share a hash")
	}
	// The stored format of the Python backend: sha256 hex of the upper-cased code without the dash.
	sum := sha256.Sum256([]byte("ABCD12340000FFFF"))
	if got := HashRecoveryCode("abcd1234-0000ffff"); got != hex.EncodeToString(sum[:]) {
		t.Errorf("hash %q", got)
	}
	if IsRecoveryCode("123456") || IsRecoveryCode("ZZZZZZZZ-ZZZZZZZZ") {
		t.Error("a non-code was recognised as a recovery code")
	}
}
