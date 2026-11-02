package notifications

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestActionTokensAreBoundToPurposeRecipientDeviceAndTime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	device := uuid.New()
	tokens := NewActionTokens("secret")
	tokens.now = func() time.Time { return now }
	read := tokens.Read(1, device, 2, 3)
	if got, err := tokens.ParseRead(read); err != nil || got.Recipient != 1 || got.Device != device || got.IDs[0] != 2 || got.IDs[1] != 3 {
		t.Fatalf("read token: %+v %v", got, err)
	}
	avatar := tokens.Avatar(1, device, 5)
	if got, err := tokens.ParseAvatar(avatar); err != nil || got.Recipient != 1 || got.Device != device || got.IDs[0] != 5 {
		t.Fatalf("avatar token: %+v %v", got, err)
	}
	if _, err := tokens.ParseRead(avatar); err == nil {
		t.Fatal("an avatar token marked a chat read")
	}
	if _, err := tokens.ParseRead(strings.Replace(read, "r.1.", "r.9.", 1)); err == nil {
		t.Fatal("a changed recipient was accepted")
	}
	if _, err := tokens.ParseRead(strings.Replace(read, device.String(), uuid.NewString(), 1)); err == nil {
		t.Fatal("a changed device was accepted")
	}
	if _, err := NewActionTokens("other").ParseRead(read); err == nil {
		t.Fatal("a token from another key was accepted")
	}
	tokens.now = func() time.Time { return now.Add(AvatarTokenTTL + time.Second) }
	if _, err := tokens.ParseAvatar(avatar); err == nil {
		t.Fatal("an expired avatar token was accepted")
	}
	if _, err := tokens.ParseRead(read); err != nil {
		t.Fatal("the read token expired with the avatar token")
	}
	if NewActionTokens("") != nil {
		t.Fatal("tokens without a secret")
	}
}
