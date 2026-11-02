package notifications

import (
	"strings"
	"testing"
	"time"
)

func TestActionTokensAreBoundToPurposeRecipientAndTime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tokens := NewActionTokens("secret")
	tokens.now = func() time.Time { return now }
	read := tokens.Read(1, 2, 3)
	if r, c, m, err := tokens.ParseRead(read); err != nil || r != 1 || c != 2 || m != 3 {
		t.Fatalf("read token: %d %d %d %v", r, c, m, err)
	}
	avatar := tokens.Avatar(1, 5)
	if r, s, err := tokens.ParseAvatar(avatar); err != nil || r != 1 || s != 5 {
		t.Fatalf("avatar token: %d %d %v", r, s, err)
	}
	if _, _, _, err := tokens.ParseRead(avatar); err == nil {
		t.Fatal("an avatar token marked a chat read")
	}
	if _, _, _, err := tokens.ParseRead(strings.Replace(read, "r.1.", "r.9.", 1)); err == nil {
		t.Fatal("a changed recipient was accepted")
	}
	if _, _, _, err := NewActionTokens("other").ParseRead(read); err == nil {
		t.Fatal("a token from another key was accepted")
	}
	tokens.now = func() time.Time { return now.Add(AvatarTokenTTL + time.Second) }
	if _, _, err := tokens.ParseAvatar(avatar); err == nil {
		t.Fatal("an expired avatar token was accepted")
	}
	if _, _, _, err := tokens.ParseRead(read); err != nil {
		t.Fatal("the read token expired with the avatar token")
	}
	if NewActionTokens("") != nil {
		t.Fatal("tokens without a secret")
	}
}
