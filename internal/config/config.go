// Package config loads the configuration from environment variables and validates it on startup.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/myronsi/messenger-back/internal/version"
)

const minSecretLength = 32

// Secret is a string that never shows up in logs, error messages or formatted output.
type Secret string

// String implements fmt.Stringer.
func (Secret) String() string { return "[redacted]" }

// GoString implements fmt.GoStringer so %#v does not leak the value either.
func (Secret) GoString() string { return "[redacted]" }

// Format redacts the value for every verb, including ones a Stringer is not consulted for (%d, %x, %q).
func (Secret) Format(state fmt.State, _ rune) { _, _ = state.Write([]byte("[redacted]")) }

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

	DatabaseURL Secret `env:"DATABASE_URL,required,notEmpty"`
	Postgres    Postgres
	RedisURL    Secret `env:"REDIS_URL,required,notEmpty"`
	Redis       Redis
	ScyllaHosts []string `env:"SCYLLA_HOSTS,required,notEmpty" envSeparator:","`
	// ScyllaKeyspace is optional so the readiness check works before the keyspace is migrated.
	ScyllaKeyspace   string `env:"SCYLLA_KEYSPACE"`
	Scylla           Scylla
	ElasticsearchURL Secret `env:"ELASTICSEARCH_URL,required,notEmpty"`

	Auth     Auth
	Realtime Realtime
	Media    Media
	Tracing  Tracing

	// WorkerAddr is where the worker serves /healthz and /metrics.
	WorkerAddr string `env:"WORKER_ADDR" envDefault:":8081"`
}

// Postgres tunes the connection pool; the connection URL is DATABASE_URL.
type Postgres struct {
	// MaxConns is the size of the pool: one pool per process.
	MaxConns int32 `env:"POSTGRES_MAX_CONNS" envDefault:"10"`
	// QueryTimeout bounds every repository call (a whole transaction counts as one call).
	QueryTimeout time.Duration `env:"POSTGRES_QUERY_TIMEOUT" envDefault:"5s"`
}

// Redis tunes the Redis client; the connection URL is REDIS_URL.
type Redis struct {
	// Timeout bounds every command (blocking stream reads excepted).
	Timeout time.Duration `env:"REDIS_TIMEOUT" envDefault:"2s"`
	// MembersCacheTTL is how long the members of a chat are cached (changes invalidate at once).
	MembersCacheTTL time.Duration `env:"MEMBERS_CACHE_TTL" envDefault:"10m"`
}

// Media configures where uploads are stored and how they are checked.
type Media struct {
	// Backend is "disk" (development) or "s3" (any S3-compatible service).
	Backend string `env:"STORAGE_BACKEND" envDefault:"disk"`
	// Dir is where the disk backend keeps the files.
	Dir string `env:"STORAGE_DIR" envDefault:"data/media"`
	// S3 settings: the endpoint URL, credentials and bucket.
	S3Endpoint  string `env:"S3_ENDPOINT"`
	S3AccessKey Secret `env:"S3_ACCESS_KEY"`
	S3SecretKey Secret `env:"S3_SECRET_KEY"`
	S3Bucket    string `env:"S3_BUCKET" envDefault:"messenger-media"`
	S3Region    string `env:"S3_REGION" envDefault:"us-east-1"`
	// SignedURLs answers downloads with a short-lived redirect to the bucket instead of streaming them through
	// the API. The bucket then needs CORS for the app's origin; S3PublicEndpoint is the address clients reach.
	SignedURLs       bool   `env:"MEDIA_SIGNED_URLS" envDefault:"false"`
	S3PublicEndpoint string `env:"S3_PUBLIC_ENDPOINT"`
	// FFprobe and FFmpeg measure voice messages; empty looks them up on PATH (the image ships both). Without
	// them the client's duration and waveform are used.
	FFprobe string `env:"FFPROBE_PATH"`
	FFmpeg  string `env:"FFMPEG_PATH"`
}

// Realtime configures the WebSocket gateway.
type Realtime struct {
	// InstanceID identifies this process among the running API instances (presence, ID leases). Empty
	// derives one from the host name and a random suffix.
	InstanceID string `env:"INSTANCE_ID"`
	// NodeID is this instance's Snowflake node number (0-1023). Unset leases a free one from Redis; set it
	// only when every instance gets its own number some other way.
	NodeID *int `env:"NODE_ID"`
	// PresenceTTL is how long a user stays online without a heartbeat from the instance holding the
	// connection (a crashed instance's users go offline within about this time).
	PresenceTTL time.Duration `env:"PRESENCE_TTL" envDefault:"60s"`
	// MinClientAPIVersion is the oldest contract version still served; older clients get close code 4426
	// on the WebSocket (and 426 on REST).
	MinClientAPIVersion string `env:"MIN_CLIENT_API_VERSION" envDefault:"2.0.0-alpha.1"`
	// SendBuffer is how many events may wait for a slow client before it is disconnected.
	SendBuffer int `env:"WS_SEND_BUFFER" envDefault:"256"`
	// PingInterval is how often idle connections are pinged.
	PingInterval time.Duration `env:"WS_PING_INTERVAL" envDefault:"25s"`
}

// Scylla tunes the ScyllaDB session; the hosts are SCYLLA_HOSTS.
type Scylla struct {
	// Consistency of reads and writes: local_quorum (production, replication factor 3), quorum, one or
	// local_one. Lightweight transactions always use LOCAL_SERIAL.
	Consistency string `env:"SCYLLA_CONSISTENCY" envDefault:"local_quorum"`
	// Timeout bounds every repository call (a page that reads several partitions counts as one).
	Timeout time.Duration `env:"SCYLLA_TIMEOUT" envDefault:"5s"`
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

// Auth configures sessions and the refresh cookie.
type Auth struct {
	// CookieSecure marks the refresh cookie Secure. Only turn it off for plain-HTTP local development;
	// production refuses to start without it.
	CookieSecure bool `env:"COOKIE_SECURE" envDefault:"true"`
	// RefreshCookiePath overrides the cookie path, which defaults to <API_BASE_PATH>/auth/refresh. During
	// the migration from the Python backend (MSGC-77) set it to the old path (/auth) so the browser sends
	// the existing cookie and the first refresh replaces it.
	RefreshCookiePath string `env:"REFRESH_COOKIE_PATH"`
	// SessionCacheTTL is how long Redis caches "this session is active"; revocations apply immediately.
	SessionCacheTTL time.Duration `env:"SESSION_CACHE_TTL" envDefault:"30s"`
	// RefreshReuseGrace tolerates the previous refresh token for this long after a rotation, so two tabs
	// refreshing together are not mistaken for token theft. 0 disables the tolerance.
	RefreshReuseGrace time.Duration `env:"REFRESH_REUSE_GRACE" envDefault:"10s"`
	// PasswordHashConcurrency bounds simultaneous Argon2 hashes (each uses 64 MiB).
	PasswordHashConcurrency int `env:"PASSWORD_HASH_CONCURRENCY" envDefault:"4"`
	// TrustedProxies lists the IPs or CIDRs of reverse proxies whose X-Forwarded-For and X-Real-IP headers
	// are believed when working out the client address (rate limits, session records). Empty trusts none.
	TrustedProxies []string `env:"TRUSTED_PROXIES" envSeparator:","`
}

// TrustedProxyPrefixes parses TrustedProxies; a bare IP is a single-address prefix.
func (a Auth) TrustedProxyPrefixes() ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range a.TrustedProxies {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		ip, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address or CIDR", s)
		}
		ip = ip.Unmap()
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
	}
	return out, nil
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
	if c.Postgres.MaxConns <= 0 {
		add("POSTGRES_MAX_CONNS must be positive")
	}
	if c.Postgres.QueryTimeout <= 0 {
		add("POSTGRES_QUERY_TIMEOUT must be positive")
	}
	if c.Redis.Timeout <= 0 {
		add("REDIS_TIMEOUT must be positive")
	}
	if c.Redis.MembersCacheTTL <= 0 {
		add("MEMBERS_CACHE_TTL must be positive")
	}
	problems = append(problems, c.Realtime.validate()...)
	problems = append(problems, c.Media.validate()...)
	switch c.Scylla.Consistency {
	case "local_quorum", "quorum", "one", "local_one":
	default:
		add("SCYLLA_CONSISTENCY must be local_quorum, quorum, one or local_one")
	}
	if c.Scylla.Timeout <= 0 {
		add("SCYLLA_TIMEOUT must be positive")
	}
	problems = append(problems, c.HTTP.validate()...)
	problems = append(problems, c.Auth.validate(c.Env == "production")...)

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

func (m Media) validate() []string {
	var problems []string
	switch m.Backend {
	case "disk":
		if strings.TrimSpace(m.Dir) == "" {
			problems = append(problems, "STORAGE_DIR must not be empty")
		}
	case "s3":
		if !validURL(m.S3Endpoint, "http", "https") {
			problems = append(problems, "S3_ENDPOINT must be an http:// or https:// URL")
		}
		if m.S3AccessKey == "" || m.S3SecretKey == "" || m.S3Bucket == "" {
			problems = append(problems, "S3_ACCESS_KEY, S3_SECRET_KEY and S3_BUCKET are required for STORAGE_BACKEND=s3")
		}
		if m.S3PublicEndpoint != "" && !validURL(m.S3PublicEndpoint, "http", "https") {
			problems = append(problems, "S3_PUBLIC_ENDPOINT must be an http:// or https:// URL")
		}
	default:
		problems = append(problems, "STORAGE_BACKEND must be disk or s3")
	}
	return problems
}

func (r Realtime) validate() []string {
	var problems []string
	if r.NodeID != nil && (*r.NodeID < 0 || *r.NodeID > 1023) {
		problems = append(problems, "NODE_ID must be between 0 and 1023")
	}
	if r.PresenceTTL < 3*time.Second {
		problems = append(problems, "PRESENCE_TTL must be at least 3s")
	}
	if min, err := version.Parse(r.MinClientAPIVersion); err != nil || min.Major != version.MustParse(version.API).Major || min.Compare(version.MustParse(version.API)) > 0 {
		problems = append(problems, "MIN_CLIENT_API_VERSION must be a version with the API's major version, not above "+version.API)
	}
	if r.SendBuffer < 16 {
		problems = append(problems, "WS_SEND_BUFFER must be at least 16")
	}
	if r.PingInterval < time.Second {
		problems = append(problems, "WS_PING_INTERVAL must be at least 1s")
	}
	if len(r.InstanceID) > 64 {
		problems = append(problems, "INSTANCE_ID must be at most 64 characters")
	}
	return problems
}

func (a Auth) validate(production bool) []string {
	var problems []string
	if production && !a.CookieSecure {
		problems = append(problems, "COOKIE_SECURE must be true in production")
	}
	if a.RefreshCookiePath != "" && (!strings.HasPrefix(a.RefreshCookiePath, "/") || strings.ContainsAny(a.RefreshCookiePath, ";, \r\n")) {
		problems = append(problems, "REFRESH_COOKIE_PATH must be an absolute path")
	}
	if _, err := a.TrustedProxyPrefixes(); err != nil {
		problems = append(problems, "TRUSTED_PROXIES must be a comma-separated list of IPs or CIDRs")
	}
	if a.SessionCacheTTL <= 0 {
		problems = append(problems, "SESSION_CACHE_TTL must be positive")
	}
	if a.RefreshReuseGrace < 0 {
		problems = append(problems, "REFRESH_REUSE_GRACE must not be negative")
	}
	if a.PasswordHashConcurrency <= 0 {
		problems = append(problems, "PASSWORD_HASH_CONCURRENCY must be positive")
	}
	return problems
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
