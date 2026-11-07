package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func validCalls() Calls {
	return Calls{
		Enabled: true, PublicURL: "wss://media.example.com", APIURL: "http://livekit:7880", APIKey: "key",
		APISecret: Secret(strings.Repeat("s", 32)), MaxParticipants: 10, MaxActiveParticipants: 250,
		RingTimeout: 30 * time.Second, EmptyGrace: 20 * time.Second, TokenTTL: 10 * time.Minute,
	}
}

func TestCallsConfiguration(t *testing.T) {
	if p := (Calls{APIKey: "placeholder", PublicURL: "not a url"}).validate(true); len(p) > 0 {
		t.Fatalf("switched off calls must not be validated: %v", p)
	}
	if p := validCalls().validate(true); len(p) > 0 {
		t.Fatal(p)
	}
	largest := validCalls()
	largest.MaxParticipants = 65
	if p := largest.validate(true); len(p) > 0 {
		t.Fatalf("65 participants: %v", p)
	}
	dev := validCalls()
	dev.PublicURL = "ws://localhost:7880"
	if p := dev.validate(false); len(p) > 0 {
		t.Fatalf("ws:// in development: %v", p)
	}
	if p := dev.validate(true); len(p) == 0 {
		t.Fatal("ws:// accepted in production")
	}
	for name, mutate := range map[string]func(*Calls){
		"missing url":     func(c *Calls) { c.APIURL = "" },
		"missing key":     func(c *Calls) { c.APIKey = "" },
		"short secret":    func(c *Calls) { c.APISecret = "short" },
		"https public":    func(c *Calls) { c.PublicURL = "https://media.example.com" },
		"one participant": func(c *Calls) { c.MaxParticipants = 1 },
		"over envelopes":  func(c *Calls) { c.MaxParticipants = 66 },
		"admission":       func(c *Calls) { c.MaxActiveParticipants = 5 },
		"ring":            func(c *Calls) { c.RingTimeout = time.Second },
		"token":           func(c *Calls) { c.TokenTTL = time.Second },
	} {
		c := validCalls()
		mutate(&c)
		if len(c.validate(true)) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCallsSettingsLoad(t *testing.T) {
	env := map[string]string{
		"JWT_SECRET": strings.Repeat("j", 32), "RECOVERY_PEPPER": strings.Repeat("p", 32),
		"ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", "DATABASE_URL": "postgres://u:p@localhost/db",
		"REDIS_URL": "redis://localhost:6379", "SCYLLA_HOSTS": "localhost", "ELASTICSEARCH_URL": "http://localhost:9200",
		"CALLS_API_KEY": "placeholder", "CALLS_TURN_PORT": "3478", "CALLS_NODE_IP": "203.0.113.1",
	}
	cfg, err := LoadFrom(env)
	if err != nil {
		t.Fatalf("calls off with placeholders and deploy-only variables: %v", err)
	}
	if cfg.Calls.Enabled || cfg.Calls.MaxParticipants != 10 || cfg.Calls.RingTimeout != 30*time.Second || cfg.Calls.TokenTTL != 10*time.Minute {
		t.Fatalf("defaults: %+v", cfg.Calls)
	}
	env["CALLS_ENABLED"] = "true"
	if _, err := LoadFrom(env); err == nil || !strings.Contains(err.Error(), "CALLS_ENABLED=true needs") {
		t.Fatalf("incomplete calls configuration: %v", err)
	}
	env["CALLS_PUBLIC_URL"], env["CALLS_API_URL"], env["CALLS_API_SECRET"] = "wss://media.example.com", "http://livekit:7880", strings.Repeat("s", 40)
	cfg, err = LoadFrom(env)
	if err != nil || !cfg.Calls.Enabled {
		t.Fatalf("complete calls configuration: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", cfg.Calls, cfg.Calls, cfg.Calls), strings.Repeat("s", 40)) {
		t.Fatal("the API secret is printable")
	}
}
