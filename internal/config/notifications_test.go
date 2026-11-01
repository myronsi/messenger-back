package config

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestNotificationConfiguration(t *testing.T) {
	if problems := (Notifications{}).validate(); len(problems) > 0 {
		t.Fatal("notifications must be optional")
	}
	if problems := (Notifications{VAPIDPublic: "partial"}).validate(); len(problems) == 0 {
		t.Fatal("partial VAPID configuration accepted")
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	n := Notifications{VAPIDPublic: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), VAPIDPrivate: Secret(base64.RawURLEncoding.EncodeToString(key.Bytes())), VAPIDSubject: "mailto:operator@example.test"}
	if problems := n.validate(); len(problems) > 0 {
		t.Fatal(problems)
	}
	n.VAPIDPublic = "invalid"
	if len(n.validate()) == 0 {
		t.Fatal("invalid public key accepted")
	}
	if len((Notifications{APNsKeyID: "partial"}).validate()) == 0 {
		t.Fatal("partial APNs configuration accepted")
	}
}
