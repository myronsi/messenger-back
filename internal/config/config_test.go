package config

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
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
		"short jwt secret":    {"JWT_SECRET": "short"},
		"short pepper":        {"RECOVERY_PEPPER": "short"},
		"encryption not b64":  {"ENCRYPTION_KEY": "!!!"},
		"encryption too long": {"ENCRYPTION_KEY": strings.Repeat("A", 64)},
		"bad env":             {"APP_ENV": "staging"},
		"bad log level":       {"LOG_LEVEL": "loud"},
		"bad database url":    {"DATABASE_URL": "mysql://x/y"},
		"cors wildcard":       {"CORS_ORIGINS": "*"},
		"cors with path":      {"CORS_ORIGINS": "https://a.example/app"},
		"zero timeout":        {"HTTP_REQUEST_TIMEOUT": "0s"},
		"bad base path":       {"API_BASE_PATH": "api/"},
		"cors http in prod":   {"APP_ENV": "production", "CORS_ORIGINS": "http://app.example.com"},
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
