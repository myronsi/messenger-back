package config

import (
	"net/url"
	"strings"
	"time"
)

const maxCallParticipants = 65

type Calls struct {
	Enabled               bool          `env:"CALLS_ENABLED" envDefault:"false"`
	PublicURL             string        `env:"CALLS_PUBLIC_URL"`
	APIURL                string        `env:"CALLS_API_URL"`
	APIKey                string        `env:"CALLS_API_KEY"`
	APISecret             Secret        `env:"CALLS_API_SECRET"`
	MaxParticipants       int           `env:"CALLS_MAX_PARTICIPANTS" envDefault:"10"`
	MaxActiveParticipants int           `env:"CALLS_MAX_ACTIVE_PARTICIPANTS" envDefault:"50"`
	RingTimeout           time.Duration `env:"CALLS_RING_TIMEOUT" envDefault:"30s"`
	EmptyGrace            time.Duration `env:"CALLS_EMPTY_GRACE" envDefault:"20s"`
	TokenTTL              time.Duration `env:"CALLS_TOKEN_TTL" envDefault:"10m"`
}

func (c Calls) validate(production bool) []string {
	if !c.Enabled {
		return nil
	}
	var problems []string
	add := func(s string) { problems = append(problems, s) }
	if c.PublicURL == "" || c.APIURL == "" || c.APIKey == "" || c.APISecret == "" {
		add("CALLS_ENABLED=true needs CALLS_PUBLIC_URL, CALLS_API_URL, CALLS_API_KEY and CALLS_API_SECRET")
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		secure := err == nil && u.Scheme == "wss"
		local := err == nil && u.Scheme == "ws" && !production
		if err != nil || u.Host == "" || u.User != nil || (!secure && !local) {
			add("CALLS_PUBLIC_URL must be a wss:// URL (ws:// only outside production)")
		}
	}
	if c.APIURL != "" && (!validURL(c.APIURL, "http", "https") || strings.Contains(c.APIURL, "@")) {
		add("CALLS_API_URL must be an http:// or https:// URL")
	}
	if c.APISecret != "" && len(c.APISecret) < minSecretLength {
		add("CALLS_API_SECRET must be at least 32 characters")
	}
	if c.MaxParticipants < 2 || c.MaxParticipants > maxCallParticipants {
		add("CALLS_MAX_PARTICIPANTS must be between 2 and 65 (a session seals its key for at most 64 others per request)")
	}
	if c.MaxActiveParticipants < c.MaxParticipants {
		add("CALLS_MAX_ACTIVE_PARTICIPANTS must be at least CALLS_MAX_PARTICIPANTS")
	}
	if c.RingTimeout < 5*time.Second || c.RingTimeout > 5*time.Minute {
		add("CALLS_RING_TIMEOUT must be between 5s and 5m")
	}
	if c.EmptyGrace < time.Second || c.EmptyGrace > 5*time.Minute {
		add("CALLS_EMPTY_GRACE must be between 1s and 5m")
	}
	if c.TokenTTL < time.Minute || c.TokenTTL > 24*time.Hour {
		add("CALLS_TOKEN_TTL must be between 1m and 24h")
	}
	return problems
}
