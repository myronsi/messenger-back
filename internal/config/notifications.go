package config

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"net/url"
)

// Notifications is optional. A platform is offered to clients only when configured.
type Notifications struct {
	VAPIDPublic       string `env:"PUSH_VAPID_PUBLIC_KEY"`
	VAPIDPrivate      Secret `env:"PUSH_VAPID_PRIVATE_KEY"`
	VAPIDSubject      string `env:"PUSH_VAPID_SUBJECT"`
	FCMServiceAccount Secret `env:"PUSH_FCM_SERVICE_ACCOUNT"`
	APNsKey           Secret `env:"PUSH_APNS_KEY"`
	APNsKeyID         string `env:"PUSH_APNS_KEY_ID"`
	APNsTeamID        string `env:"PUSH_APNS_TEAM_ID"`
	APNsTopic         string `env:"PUSH_APNS_TOPIC"`
	APNsSandbox       bool   `env:"PUSH_APNS_SANDBOX" envDefault:"false"`
}

func (n Notifications) WebEnabled() bool {
	return n.VAPIDPublic != "" && n.VAPIDPrivate != "" && n.VAPIDSubject != ""
}
func (n Notifications) AndroidEnabled() bool { return n.FCMServiceAccount != "" }
func (n Notifications) IOSEnabled() bool {
	return n.APNsKey != "" && n.APNsKeyID != "" && n.APNsTeamID != "" && n.APNsTopic != ""
}
func (n Notifications) validate() []string {
	var problems []string
	if n.VAPIDPublic != "" || n.VAPIDPrivate != "" || n.VAPIDSubject != "" {
		if !n.WebEnabled() {
			problems = append(problems, "set all PUSH_VAPID_PUBLIC_KEY, PUSH_VAPID_PRIVATE_KEY and PUSH_VAPID_SUBJECT")
		} else {
			raw, err := base64.RawURLEncoding.DecodeString(n.VAPIDPrivate.Reveal())
			pub, e2 := base64.RawURLEncoding.DecodeString(n.VAPIDPublic)
			key, e3 := ecdh.P256().NewPrivateKey(raw)
			if err != nil || e2 != nil || e3 != nil || key == nil || !bytes.Equal(key.PublicKey().Bytes(), pub) {
				problems = append(problems, "PUSH_VAPID keys must be a matching P-256 key pair")
			}
			subject, err := url.Parse(n.VAPIDSubject)
			if err != nil || subject == nil || subject.Scheme != "mailto" && subject.Scheme != "https" {
				problems = append(problems, "PUSH_VAPID_SUBJECT must be a mailto: or https: contact")
			}
		}
	}
	if n.APNsKey != "" || n.APNsKeyID != "" || n.APNsTeamID != "" || n.APNsTopic != "" {
		if !n.IOSEnabled() {
			problems = append(problems, "set all PUSH_APNS_KEY, PUSH_APNS_KEY_ID, PUSH_APNS_TEAM_ID and PUSH_APNS_TOPIC")
		}
	}
	return problems
}
