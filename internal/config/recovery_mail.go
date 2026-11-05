package config

import (
	"net"
	"net/mail"
	"net/url"
	"strings"
)

type RecoveryMail struct {
	Host     string `env:"SMTP_HOST"`
	Port     int    `env:"SMTP_PORT" envDefault:"587"`
	Username string `env:"SMTP_USERNAME"`
	Password Secret `env:"SMTP_PASSWORD"`
	From     string `env:"SMTP_FROM"`
	TLSMode  string `env:"SMTP_TLS_MODE" envDefault:"starttls"`
	WebURL   string `env:"RECOVERY_WEB_URL"`
}

func (m RecoveryMail) validate() []string {
	if m.Host == "" {
		return nil
	}
	var out []string
	if strings.ContainsAny(m.Host, "/\r\n ") || m.Port < 1 || m.Port > 65535 {
		out = append(out, "SMTP_HOST and SMTP_PORT must identify a mail server")
	}
	from, err := mail.ParseAddress(m.From)
	if err != nil || from.Address != m.From || strings.ContainsAny(m.From, "\r\n") {
		out = append(out, "SMTP_FROM must be an email address")
	}
	local := m.Host == "localhost"
	if ip := net.ParseIP(m.Host); ip != nil && ip.IsLoopback() {
		local = true
	}
	if m.TLSMode != "starttls" && m.TLSMode != "tls" && (m.TLSMode != "plain" || !local) {
		out = append(out, "SMTP_TLS_MODE must be starttls or tls (plain is allowed only for a local test server)")
	}
	u, err := url.Parse(m.WebURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		out = append(out, "RECOVERY_WEB_URL must be a frontend origin")
	} else {
		localWeb := u.Hostname() == "localhost"
		if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsLoopback() {
			localWeb = true
		}
		if u.Scheme != "https" && (u.Scheme != "http" || !localWeb) {
			out = append(out, "RECOVERY_WEB_URL must use HTTPS except for local testing")
		}
	}
	return out
}
