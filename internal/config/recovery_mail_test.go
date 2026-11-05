package config

import "testing"

func TestRecoveryMailTransportSecurity(t *testing.T) {
	valid := RecoveryMail{Host: "smtp.example.com", Port: 587, From: "no-reply@example.com", TLSMode: "starttls", WebURL: "https://chat.example.com"}
	if errors := valid.validate(); len(errors) > 0 {
		t.Fatal(errors)
	}
	for _, change := range []func(*RecoveryMail){
		func(c *RecoveryMail) { c.TLSMode = "plain" }, func(c *RecoveryMail) { c.From = "x@example.com\r\nBcc: attacker@example.com" },
		func(c *RecoveryMail) { c.WebURL = "http://chat.example.com" }, func(c *RecoveryMail) { c.WebURL = "https://chat.example.com/reset?token=x" },
		func(c *RecoveryMail) { c.Port = 0 },
	} {
		c := valid
		change(&c)
		if len(c.validate()) == 0 {
			t.Fatal("accepted insecure mail configuration")
		}
	}
	local := RecoveryMail{Host: "127.0.0.1", Port: 1025, From: "no-reply@example.com", TLSMode: "plain", WebURL: "http://localhost:4173"}
	if errors := local.validate(); len(errors) > 0 {
		t.Fatal("local test transport", errors)
	}
}
