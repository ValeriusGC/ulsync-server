// Package config loads and validates the single YAML configuration file that
// drives every runtime path, bind address, and timeout in the server.
//
// Unknown YAML keys are rejected (KnownFields). Zero values in the file are
// filled from embedded defaults.yaml before validation runs.
//
// Example:
//
//	cfg, err := config.Load("./config.yaml")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Println(cfg.Server.Bind)
package config

import (
	_ "embed"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed defaults.yaml
var defaultConfigYAML []byte // compiled defaults merged before validation

// Duration is a [time.Duration] that unmarshals from a Go duration string in YAML
// (for example "5s" or "120s"). The stock yaml.v3 decoder treats unquoted
// duration strings as zero, which would silently disable server timeouts.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler for Duration fields.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	var raw string
	if err := value.Decode(&raw); err != nil {
		return fmt.Errorf("duration: %w", err)
	}
	if raw == "" {
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("duration %q: %w", raw, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std returns the underlying time.Duration for use with net/http and time APIs.
func (d Duration) Std() time.Duration {
	return time.Duration(d)
}

// MarshalYAML implements yaml.Marshaler so duration fields serialize as human-
// readable strings (for example "5s") instead of raw nanoseconds in snapshots.
func (d Duration) MarshalYAML() (interface{}, error) {
	return time.Duration(d).String(), nil
}

// Config is the root configuration document. Every field maps to a top-level
// YAML section in config.yaml / config.example.yaml.
type Config struct {
	Server  Server  `yaml:"server"`
	Storage Storage `yaml:"storage"`
	// Origin names the application contour for an authored store. Empty or absent
	// means an open store that hello may imprint on first contact.
	Origin string `yaml:"origin"`
	Auth   Auth   `yaml:"auth"`
	Sync   Sync   `yaml:"sync"`
	Admin  Admin  `yaml:"admin"`
}

// Server holds HTTP listener settings for /health and future /v1/* sync routes.
type Server struct {
	// Bind is the host:port address (for example "0.0.0.0:8080").
	Bind string `yaml:"bind"`
	// ReadHeaderTimeout closes connections that do not send headers in time.
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	// IdleTimeout closes idle keep-alive connections.
	IdleTimeout Duration `yaml:"idle_timeout"`
	// MaxBodyBytes is the maximum POST body size accepted by the HTTP stack.
	MaxBodyBytes int64 `yaml:"max_body_bytes"`
}

// Storage holds persistence settings. Round 1 supports driver "sqlite" only.
type Storage struct {
	// Driver names the storage backend ("sqlite" in round 1).
	Driver string `yaml:"driver"`
	// Path is the SQLite database file path. The parent directory is created on open.
	Path string `yaml:"path"`
}

// Auth holds JWT verification settings. Used from step 03 onward; loaded and
// validated here so a single config file describes the full deployment.
type Auth struct {
	// JWKSURL is the URL to fetch the JSON Web Key Set for JWT verification.
	JWKSURL string `yaml:"jwks_url"`
	// JWKSFile, when non-empty, is a path to a static JWKS document. The
	// process reads this file and does not fetch JWKSURL.
	JWKSFile string `yaml:"jwks_file"`
	// JWKSCacheTTL is how long fetched JWKS keys remain cached in memory.
	JWKSCacheTTL Duration `yaml:"jwks_cache_ttl"`
	// AllowedAlgs lists accepted JWT signing algorithms (for example ES256, RS256).
	AllowedAlgs []string `yaml:"allowed_algs"`
	// Audience, when non-empty, requires matching JWT aud claim values.
	Audience []string `yaml:"audience"`
	// Issuer, when non-empty, requires a matching JWT iss claim.
	Issuer string `yaml:"issuer"`
	// DevHS256Secret is a development-only shared secret for HS256 tokens (step 03).
	DevHS256Secret string `yaml:"dev_hs256_secret"`
}

// Sync holds limits and timeouts for push, pull, and live sync endpoints.
type Sync struct {
	// MaxEnvelopesPerPush caps envelopes per push request (1 in round 1).
	MaxEnvelopesPerPush int `yaml:"max_envelopes_per_push"`
	// PullLimitDefault is the page size when the client omits limit on pull.
	PullLimitDefault int `yaml:"pull_limit_default"`
	// PullLimitMax is the hard upper bound for pull limit.
	PullLimitMax int `yaml:"pull_limit_max"`
	// LivePollTimeout is how long live=poll waits before an empty response.
	LivePollTimeout Duration `yaml:"live_poll_timeout"`
	// LiveHeartbeat is the SSE comment interval to keep connections alive.
	LiveHeartbeat Duration `yaml:"live_heartbeat"`
}

// Admin holds settings for the operations panel listener (step 07).
type Admin struct {
	// Bind is the host:port for /admin (default 127.0.0.1:8081).
	Bind string `yaml:"bind"`
	// Token is required when Bind is not loopback-only.
	Token string `yaml:"token"`
}

// Load reads path, merges embedded defaults, and validates the result.
//
// Validation failures aggregate every problem into one error value so operators
// can fix the config file in a single pass. Load rejects unknown YAML keys.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyDefaults fills zero-valued fields from the embedded defaults.yaml.
func (c *Config) applyDefaults() {
	var defaults Config
	if err := yaml.Unmarshal(defaultConfigYAML, &defaults); err != nil {
		panic(fmt.Sprintf("config: parse embedded defaults: %v", err))
	}

	if c.Server.Bind == "" {
		c.Server.Bind = defaults.Server.Bind
	}
	if c.Server.ReadHeaderTimeout == 0 {
		c.Server.ReadHeaderTimeout = defaults.Server.ReadHeaderTimeout
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = defaults.Server.IdleTimeout
	}
	if c.Server.MaxBodyBytes == 0 {
		c.Server.MaxBodyBytes = defaults.Server.MaxBodyBytes
	}
	if c.Storage.Driver == "" {
		c.Storage.Driver = defaults.Storage.Driver
	}
	if c.Storage.Path == "" {
		c.Storage.Path = defaults.Storage.Path
	}
	if c.Auth.JWKSURL == "" {
		c.Auth.JWKSURL = defaults.Auth.JWKSURL
	}
	if c.Auth.JWKSCacheTTL == 0 {
		c.Auth.JWKSCacheTTL = defaults.Auth.JWKSCacheTTL
	}
	if c.Auth.AllowedAlgs == nil {
		c.Auth.AllowedAlgs = defaults.Auth.AllowedAlgs
	}
	if c.Auth.Audience == nil {
		c.Auth.Audience = defaults.Auth.Audience
	}
	if c.Sync.MaxEnvelopesPerPush == 0 {
		c.Sync.MaxEnvelopesPerPush = defaults.Sync.MaxEnvelopesPerPush
	}
	if c.Sync.PullLimitDefault == 0 {
		c.Sync.PullLimitDefault = defaults.Sync.PullLimitDefault
	}
	if c.Sync.PullLimitMax == 0 {
		c.Sync.PullLimitMax = defaults.Sync.PullLimitMax
	}
	if c.Sync.LivePollTimeout == 0 {
		c.Sync.LivePollTimeout = defaults.Sync.LivePollTimeout
	}
	if c.Sync.LiveHeartbeat == 0 {
		c.Sync.LiveHeartbeat = defaults.Sync.LiveHeartbeat
	}
	if c.Admin.Bind == "" {
		c.Admin.Bind = defaults.Admin.Bind
	}
}

// validate checks cross-field constraints that YAML structure alone cannot express.
func (c *Config) validate() error {
	var problems []string

	if err := validateBind("server.bind", c.Server.Bind); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateBind("admin.bind", c.Admin.Bind); err != nil {
		problems = append(problems, err.Error())
	}
	if strings.TrimSpace(c.Storage.Path) == "" {
		problems = append(problems, "storage.path must not be empty")
	}
	if c.Origin != "" {
		if err := ValidateOrigin(c.Origin); err != nil {
			problems = append(problems, "origin: "+err.Error())
		}
	}
	if c.Server.MaxBodyBytes <= 0 {
		problems = append(problems, "server.max_body_bytes must be greater than zero")
	}
	if c.Sync.PullLimitDefault <= 0 {
		problems = append(problems, "sync.pull_limit_default must be greater than zero")
	}
	if c.Sync.PullLimitMax <= 0 {
		problems = append(problems, "sync.pull_limit_max must be greater than zero")
	}
	if c.Sync.PullLimitDefault > c.Sync.PullLimitMax {
		problems = append(
			problems,
			fmt.Sprintf(
				"sync.pull_limit_default (%d) must be less than or equal to sync.pull_limit_max (%d)",
				c.Sync.PullLimitDefault,
				c.Sync.PullLimitMax,
			),
		)
	}
	// Refuse to start with a network-exposed admin listener and no shared secret.
	if !isLoopbackBind(c.Admin.Bind) && strings.TrimSpace(c.Admin.Token) == "" {
		problems = append(
			problems,
			"admin.bind listens on a non-loopback address while admin.token is empty; bind to 127.0.0.1 or set admin.token",
		)
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration:\n- %s", strings.Join(problems, "\n- "))
}

// validateBind ensures bind is a non-empty host:port accepted by net.SplitHostPort.
func validateBind(field, bind string) error {
	if strings.TrimSpace(bind) == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if _, _, err := net.SplitHostPort(bind); err != nil {
		return fmt.Errorf("%s %q is not a valid host:port address: %v", field, bind, err)
	}
	return nil
}

// maxOriginLen is the upper bound from protocol SPEC §1.5 for Ulsync-Origin.
const maxOriginLen = 256

// ValidateOrigin checks the character class and length of an origin string.
// An empty string is valid and means an open store. The same rules apply to
// configuration and to the Ulsync-Origin header so authored pins cannot start
// with a value HTTP would reject on the first request.
func ValidateOrigin(origin string) error {
	if origin == "" {
		return nil
	}
	if len(origin) > maxOriginLen {
		return fmt.Errorf("length %d exceeds maximum %d", len(origin), maxOriginLen)
	}
	for _, r := range origin {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.', r == '_', r == '/', r == '-':
		default:
			return fmt.Errorf("character %q is outside the allowed class", r)
		}
	}
	return nil
}

// isLoopbackBind reports whether bind listens only on localhost/loopback addresses.
func isLoopbackBind(bind string) bool {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

const (
	redactedSecret   = "***"     // placeholder for non-empty secrets in Redacted()
	emptySecretLabel = "(empty)" // placeholder for empty secrets in Redacted()
)

// Redacted returns a copy of c with sensitive fields masked for display.
//
// auth.dev_hs256_secret and admin.token become "***" when non-empty after
// TrimSpace, or "(empty)" when blank. The live configuration is not modified;
// callers must not assign the result back over *c or panel authentication
// would compare against "***".
func (c Config) Redacted() Config {
	out := c
	out.Auth.AllowedAlgs = append([]string(nil), c.Auth.AllowedAlgs...)
	out.Auth.Audience = append([]string(nil), c.Auth.Audience...)
	if strings.TrimSpace(c.Auth.DevHS256Secret) != "" {
		out.Auth.DevHS256Secret = redactedSecret
	} else {
		out.Auth.DevHS256Secret = emptySecretLabel
	}
	if strings.TrimSpace(c.Admin.Token) != "" {
		out.Admin.Token = redactedSecret
	} else {
		out.Admin.Token = emptySecretLabel
	}
	return out
}
