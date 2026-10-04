package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealer(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	s, err := NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal("JBSWY3DPEHPK3PXP", "totp:1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, "JBSWY3DPEHPK3PXP") {
		t.Fatalf("unexpected sealed value %q", sealed)
	}
	if got, err := s.Open(sealed, "totp:1"); err != nil || got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := s.Open(sealed, "totp:2"); err == nil {
		t.Fatal("a secret opened for another account")
	}
	other, _ := NewSealer(bytes.Repeat([]byte{8}, 32))
	if _, err := other.Open(sealed, "totp:1"); err == nil {
		t.Fatal("a secret opened with another key")
	}
	again, _ := s.Seal("JBSWY3DPEHPK3PXP", "totp:1")
	if again == sealed {
		t.Fatal("the nonce is reused")
	}
	for _, bad := range []string{"", "v1:", "v1:!!!", "v2:abcd", sealed[:len(sealed)-2] + "AA", "plain"} {
		if _, err := s.Open(bad, "totp:1"); err == nil {
			t.Errorf("%q opened", bad)
		}
	}
}

func TestSealerRejectsBadKeys(t *testing.T) {
	for _, n := range []int{0, 16, 24, 31, 33} {
		if _, err := NewSealer(make([]byte, n)); err == nil {
			t.Errorf("%d-byte key accepted", n)
		}
	}
}
