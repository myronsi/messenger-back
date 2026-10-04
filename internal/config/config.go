// Package config loads the configuration from environment variables and validates it on startup.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

const minSecretLength = 32

// Secret is a string that never shows up in logs, error messages or formatted output.
type Secret string

// String implements fmt.Stringer.
func (Secret) String() string { return "[redacted]" }

// GoString implements fmt.GoStringer so %#v does not leak the value either.
func (Secret) GoString() string { return "[redacted]" }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// MarshalText keeps the value out of JSON and other text encodings.
func (Secret) MarshalText() ([]byte, error) { return []byte("[redacted]"), nil }

// Reveal returns the secret value. Call it only where the value is actually used.
func (s Secret) Reveal() string { return string(s) }

// Config is the configuration shared by the API and the worker.
type Config struct {
	Env      string `env:"APP_ENV" envDefault:"development"`
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`

	// Secrets: the process refuses to start without them.
	JWTSecret      Secret `env:"JWT_SECRET,required,notEmpty"`
	EncryptionKey  Secret `env:"ENCRYPTION_KEY,required,notEmpty"`
	RecoveryPepper Secret `env:"RECOVERY_PEPPER,required,notEmpty"`

	HTTP HTTP

	DatabaseURL Secret   `env:"DATABASE_URL,required,notEmpty"`
	RedisURL    Secret   `env:"REDIS_URL,required,notEmpty"`
	ScyllaHosts []string `env:"SCYLLA_HOSTS,required,notEmpty" envSeparator:","`
	// ScyllaKeyspace is optional so the readiness check works before the keyspace is migrated.
	ScyllaKeyspace   string `env:"SCYLLA_KEYSPACE"`
	ElasticsearchURL Secret `env:"ELASTICSEARCH_URL,required,notEmpty"`

	Tracing Tracing

	// WorkerAddr is where the worker serves /healthz and /metrics.
	WorkerAddr string `env:"WORKER_ADDR" envDefault:":8081"`
}

// HTTP configures the API server.
type HTTP struct {
	Addr string `env:"HTTP_ADDR" envDefault:":8080"`
	// BasePath is where the REST contract is served; it must match `servers` in api/openapi.yaml.
	BasePath          string        `env:"API_BASE_PATH" envDefault:"/api/v2"`
	ReadHeaderTimeout time.Duration `env:"HTTP_READ_HEADER_TIMEOUT" envDefault:"5s"`
	ReadTimeout       time.Duration `env:"HTTP_READ_TIMEOUT" envDefault:"30s"`
	WriteTimeout      time.Duration `env:"HTTP_WRITE_TIMEOUT" envDefault:"60s"`
	IdleTimeout       time.Duration `env:"HTTP_IDLE_TIMEOUT" envDefault:"120s"`
	RequestTimeout    time.Duration `env:"HTTP_REQUEST_TIMEOUT" envDefault:"30s"`
	ShutdownTimeout   time.Duration `env:"HTTP_SHUTDOWN_TIMEOUT" envDefault:"25s"`
	// DrainDelay is how long /readyz answers 503 before the server stops listening, so load
	// balancers notice first. 0 stops immediately (fine for local runs).
	DrainDelay   time.Duration `env:"HTTP_DRAIN_DELAY" envDefault:"0s"`
	MaxBodyBytes int64         `env:"HTTP_MAX_BODY_BYTES" envDefault:"1048576"`
	// CORSOrigins lists the allowed origins. Empty means same-origin only; "*" is rejected.
	CORSOrigins []string `env:"CORS_ORIGINS" envSeparator:","`
	// ReadinessTimeout bounds every dependency check of /readyz.
	ReadinessTimeout time.Duration `env:"READINESS_TIMEOUT" envDefault:"2s"`
}

// Tracing configures optional OpenTelemetry tracing. The exporter itself is configured with the
// standard OTEL_* variables (for example OTEL_EXPORTER_OTLP_ENDPOINT).
type Tracing struct {
	Enabled     bool   `env:"OTEL_TRACING_ENABLED" envDefault:"false"`
	ServiceName string `env:"OTEL_SERVICE_NAME" envDefault:"messenger"`
}

// Load reads the configuration from the process environment and validates it.
func Load() (Config, error) {
	return LoadFrom(nil)
}

// LoadFrom is like Load but reads from the given map when it is not nil (used by tests).
func LoadFrom(environ map[string]string) (Config, error) {
	var cfg Config
	opts := env.Options{}
	if environ != nil {
		opts.Environment = environ
	}
	if err := env.ParseWithOptions(&cfg, opts); err != nil {
		return Config{}, redactParseError(err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// redactParseError drops the offending values from the parser error: they may be secrets.
func redactParseError(err error) error {
	var agg env.AggregateError
	if !errors.As(err, &agg) {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	msgs := make([]string, 0, len(agg.Errors))
	for _, e := range agg.Errors {
		var missing env.VarIsNotSetError
		var empty env.EmptyVarError
		var parse env.ParseError
		switch {
		case errors.As(e, &missing):
			msgs = append(msgs, missing.Key+" is required")
		case errors.As(e, &empty):
			msgs = append(msgs, empty.Key+" must not be empty")
		case errors.As(e, &parse):
			msgs = append(msgs, envName(reflect.TypeOf(Config{}), parse.Name)+" has an invalid value")
		default:
			msgs = append(msgs, "a variable has an invalid value")
		}
	}
	return fmt.Errorf("invalid configuration: %s", strings.Join(msgs, "; "))
}

// envName finds the environment variable of the (possibly nested) struct field called field.
func envName(t reflect.Type, field string) string {
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Name == field {
			if name, _, _ := strings.Cut(f.Tag.Get("env"), ","); name != "" {
				return name
			}
		}
		if f.Type.Kind() == reflect.Struct {
			if name := envName(f.Type, field); name != "a variable" {
				return name
			}
		}
	}
	return "a variable"
}

// Validate checks the values that the parser cannot: secret strength, URLs, ranges.
func (c Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	switch c.Env {
	case "development", "test", "production":
	default:
		add("APP_ENV must be development, test or production")
	}
	if _, err := ParseLogLevel(c.LogLevel); err != nil {
		add("LOG_LEVEL must be debug, info, warn or error")
	}

	checkSecret := func(name string, s Secret) {
		if len(s) < minSecretLength {
			add("%s must be at least %d characters", name, minSecretLength)
		}
	}
	checkSecret("JWT_SECRET", c.JWTSecret)
	checkSecret("RECOVERY_PEPPER", c.RecoveryPepper)
	if key, err := base64.StdEncoding.DecodeString(c.EncryptionKey.Reveal()); err != nil || len(key) != 32 {
		add("ENCRYPTION_KEY must be 32 random bytes, base64 encoded (openssl rand -base64 32)")
	}

	if !validURL(c.DatabaseURL.Reveal(), "postgres", "postgresql") {
		add("DATABASE_URL must be a postgres:// URL")
	}
	if !validURL(c.RedisURL.Reveal(), "redis", "rediss") {
		add("REDIS_URL must be a redis:// or rediss:// URL")
	}
	if !validURL(c.ElasticsearchURL.Reveal(), "http", "https") {
		add("ELASTICSEARCH_URL must be an http:// or https:// URL")
	}
	for _, h := range c.ScyllaHosts {
		if strings.TrimSpace(h) == "" {
			add("SCYLLA_HOSTS must not contain empty entries")
			break
		}
	}

	if c.Env == "production" {
		for _, o := range c.HTTP.CORSOrigins {
			if u, err := url.Parse(o); err == nil && u.Scheme == "http" && !strings.EqualFold(u.Hostname(), "localhost") {
				add("CORS_ORIGINS must use https in production")
				break
			}
		}
	}
	problems = append(problems, c.HTTP.validate()...)

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

func (h HTTP) validate() []string {
	var problems []string
	if h.BasePath == "/" || !strings.HasPrefix(h.BasePath, "/") || strings.HasSuffix(h.BasePath, "/") {
		problems = append(problems, "API_BASE_PATH must start with /, contain a path segment and not end with /")
	}
	for name, d := range map[string]time.Duration{
		"HTTP_READ_HEADER_TIMEOUT": h.ReadHeaderTimeout,
		"HTTP_READ_TIMEOUT":        h.ReadTimeout,
		"HTTP_WRITE_TIMEOUT":       h.WriteTimeout,
		"HTTP_IDLE_TIMEOUT":        h.IdleTimeout,
		"HTTP_REQUEST_TIMEOUT":     h.RequestTimeout,
		"HTTP_SHUTDOWN_TIMEOUT":    h.ShutdownTimeout,
		"READINESS_TIMEOUT":        h.ReadinessTimeout,
	} {
		if d <= 0 {
			problems = append(problems, name+" must be positive")
		}
	}
	if h.DrainDelay < 0 {
		problems = append(problems, "HTTP_DRAIN_DELAY must not be negative")
	}
	if h.MaxBodyBytes <= 0 {
		problems = append(problems, "HTTP_MAX_BODY_BYTES must be positive")
	}
	for _, o := range h.CORSOrigins {
		u, err := url.Parse(o)
		if o == "*" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			problems = append(problems, "CORS_ORIGINS entries must be origins such as https://app.example.com (\"*\" is not accepted)")
			break
		}
	}
	return problems
}

func validURL(raw string, schemes ...string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	for _, s := range schemes {
		if u.Scheme == s {
			return true
		}
	}
	return false
}

// ParseLogLevel converts a LOG_LEVEL value to a slog level.
func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q", s)
}
