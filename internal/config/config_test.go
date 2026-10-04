package config

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes, base64

func validEnv() map[string]string {
	return map[string]string{
		"JWT_SECRET":        strings.Repeat("j", 32),
		"ENCRYPTION_KEY":    testKey,
		"RECOVERY_PEPPER":   strings.Repeat("p", 32),
		"DATABASE_URL":      "postgres://u:pw@localhost:5432/db",
		"REDIS_URL":         "redis://localhost:6379/0",
		"SCYLLA_HOSTS":      "localhost,other",
		"ELASTICSEARCH_URL": "http://localhost:9200",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := LoadFrom(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":8080" || cfg.HTTP.BasePath != "/api/v2" || cfg.Env != "development" {
		t.Fatalf("unexpected defaults: %+v", cfg.HTTP)
	}
	if cfg.Postgres.MaxConns != 10 || cfg.Postgres.QueryTimeout != 5*time.Second {
		t.Fatalf("unexpected postgres defaults: %+v", cfg.Postgres)
	}
	if len(cfg.ScyllaHosts) != 2 {
		t.Fatalf("hosts = %v", cfg.ScyllaHosts)
	}
}

func TestRequiredSecrets(t *testing.T) {
	for _, name := range []string{"JWT_SECRET", "ENCRYPTION_KEY", "RECOVERY_PEPPER", "DATABASE_URL", "REDIS_URL", "SCYLLA_HOSTS", "ELASTICSEARCH_URL"} {
		t.Run(name, func(t *testing.T) {
			e := validEnv()
			delete(e, name)
			_, err := LoadFrom(e)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("expected an error naming %s, got %v", name, err)
			}
			e[name] = ""
			if _, err := LoadFrom(e); err == nil {
				t.Fatalf("empty %s must be rejected", name)
			}
		})
	}
}

func TestValidation(t *testing.T) {
	tests := map[string]map[string]string{
		"short jwt secret":     {"JWT_SECRET": "short"},
		"short pepper":         {"RECOVERY_PEPPER": "short"},
		"encryption not b64":   {"ENCRYPTION_KEY": "!!!"},
		"encryption too long":  {"ENCRYPTION_KEY": strings.Repeat("A", 64)},
		"bad env":              {"APP_ENV": "staging"},
		"bad log level":        {"LOG_LEVEL": "loud"},
		"bad database url":     {"DATABASE_URL": "mysql://x/y"},
		"cors wildcard":        {"CORS_ORIGINS": "*"},
		"cors with path":       {"CORS_ORIGINS": "https://a.example/app"},
		"zero timeout":         {"HTTP_REQUEST_TIMEOUT": "0s"},
		"zero pool size":       {"POSTGRES_MAX_CONNS": "0"},
		"negative pool size":   {"POSTGRES_MAX_CONNS": "-1"},
		"zero query timeout":   {"POSTGRES_QUERY_TIMEOUT": "0s"},
		"bad base path":        {"API_BASE_PATH": "api/"},
		"root base path":       {"API_BASE_PATH": "/"},
		"cors userinfo":        {"CORS_ORIGINS": "https://user@app.example.com"},
		"cors query":           {"CORS_ORIGINS": "https://app.example.com?x=1"},
		"cors fragment":        {"CORS_ORIGINS": "https://app.example.com#x"},
		"cors localhost fake":  {"APP_ENV": "production", "CORS_ORIGINS": "http://localhost.attacker.example"},
		"cors http in prod":    {"APP_ENV": "production", "CORS_ORIGINS": "http://app.example.com"},
		"insecure cookie prod": {"APP_ENV": "production", "COOKIE_SECURE": "false"},
		"relative cookie path": {"REFRESH_COOKIE_PATH": "auth"},
		"zero cache ttl":       {"SESSION_CACHE_TTL": "0s"},
		"negative grace":       {"REFRESH_REUSE_GRACE": "-1s"},
		"zero hash workers":    {"PASSWORD_HASH_CONCURRENCY": "0"},
	}
	for name, override := range tests {
		t.Run(name, func(t *testing.T) {
			e := validEnv()
			for k, v := range override {
				e[k] = v
			}
			if _, err := LoadFrom(e); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestAuthDefaults(t *testing.T) {
	cfg, err := LoadFrom(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Auth
	if !a.CookieSecure || a.RefreshCookiePath != "" || a.SessionCacheTTL != 30*time.Second || a.RefreshReuseGrace != 10*time.Second || a.PasswordHashConcurrency != 4 {
		t.Fatalf("unexpected defaults: %+v", a)
	}
	e := validEnv()
	e["COOKIE_SECURE"] = "false"
	e["REFRESH_REUSE_GRACE"] = "0s"
	if cfg, err = LoadFrom(e); err != nil || cfg.Auth.CookieSecure || cfg.Auth.RefreshReuseGrace != 0 {
		t.Fatalf("overrides not applied: %v %+v", err, cfg.Auth)
	}
}

func TestErrorsDoNotLeakValues(t *testing.T) {
	e := validEnv()
	e["JWT_SECRET"] = "super-secret-but-short"
	e["HTTP_READ_TIMEOUT"] = "not-a-duration-secret"
	_, err := LoadFrom(e)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, leak := range []string{"super-secret-but-short", "not-a-duration-secret"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaks a value: %v", err)
		}
	}
}

func TestSecretIsRedacted(t *testing.T) {
	cfg, err := LoadFrom(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	secret := cfg.JWTSecret.Reveal()
	for _, out := range []string{
		fmt.Sprintf("%v", cfg),
		fmt.Sprintf("%+v", cfg),
		fmt.Sprintf("%#v", cfg),
		fmt.Sprint(cfg.JWTSecret),
		fmt.Sprintf("%d %x %q %s %10v", cfg.JWTSecret, cfg.JWTSecret, cfg.JWTSecret, cfg.JWTSecret, cfg.JWTSecret),
	} {
		for _, s := range []string{secret, testKey, "pw@localhost"} {
			if strings.Contains(out, s) {
				t.Fatalf("formatted config leaks %q: %s", s, out)
			}
		}
	}
	var b strings.Builder
	slog.New(slog.NewJSONHandler(&b, nil)).Info("cfg", "secret", cfg.JWTSecret)
	if strings.Contains(b.String(), secret) {
		t.Fatalf("slog leaks the secret: %s", b.String())
	}
}

func TestElasticsearchCredentialsAreRedacted(t *testing.T) {
	e := validEnv()
	e["ELASTICSEARCH_URL"] = "http://elastic:hunter2@localhost:9200"
	cfg, err := LoadFrom(e)
	if err != nil {
		t.Fatal(err)
	}
	if out := fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg); strings.Contains(out, "hunter2") {
		t.Fatalf("formatted config leaks the Elasticsearch password: %s", out)
	}
}

func TestParseErrorNamesTheVariable(t *testing.T) {
	e := validEnv()
	e["HTTP_READ_TIMEOUT"] = "not-a-duration-secret"
	_, err := LoadFrom(e)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "HTTP_READ_TIMEOUT") {
		t.Fatalf("error does not name the variable: %v", err)
	}
	if strings.Contains(err.Error(), "not-a-duration-secret") {
		t.Fatalf("error leaks the value: %v", err)
	}
}
