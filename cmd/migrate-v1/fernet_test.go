package main

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

// fernetEncrypt makes a Fernet token, for fixtures as the Python backend wrote them.
func fernetEncrypt(key, plain, iv []byte, ts uint64) string {
	n := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytesOf(byte(n), n)...)
	block, _ := aes.NewCipher(key[16:])
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	body := binary.BigEndian.AppendUint64([]byte{0x80}, ts)
	body = append(append(body, iv...), ct...)
	h := hmac.New(sha256.New, key[:16])
	h.Write(body)
	return base64.URLEncoding.EncodeToString(h.Sum(body))
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// pythonVector reads testdata/fernet_python.txt: the derived key and a token, one per line.
func pythonVector(t *testing.T) (string, string) {
	t.Helper()
	f, err := os.Open("testdata/fernet_python.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("testdata/fernet_python.txt: %d lines", len(lines))
	}
	return lines[0], lines[1]
}

func TestFernetMatchesPython(t *testing.T) {
	pyKey, pyToken := pythonVector(t)
	key, err := fernetKey(strings.Repeat("x", 40))
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.URLEncoding.EncodeToString(key); got != pyKey {
		t.Fatalf("derived key %s, Python %s", got, pyKey)
	}
	if plain, err := fernetDecrypt(key, pyToken); err != nil || string(plain) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("Python token: %q %v", plain, err)
	}
	// Tokens made here (the fixtures) open as well; tampering and a wrong key are caught.
	tok := fernetEncrypt(key, []byte("JBSWY3DPEHPK3PXP"), bytes.Repeat([]byte{3}, 16), 1)
	if plain, err := fernetDecrypt(key, tok); err != nil || string(plain) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("round trip: %q %v", plain, err)
	}
	if _, err := fernetDecrypt(key, pyToken[:len(pyToken)-8]+"AAAAAAA="); err == nil {
		t.Fatal("tampered token accepted")
	}
	other, _ := fernetKey(strings.Repeat("y", 40))
	if _, err := fernetDecrypt(other, pyToken); err == nil {
		t.Fatal("wrong key accepted")
	}
}
