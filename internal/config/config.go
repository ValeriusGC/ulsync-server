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
var defaultConfigYAML []byte

// Duration is a time.Duration that unmarshals from a Go duration string such
// as "5s". The standard yaml decoder maps such strings to zero, which would
// silently disable every timeout in the configuration.
type Duration time.Duration

// UnmarshalYAML parses a Go duration string from YAML.
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

// Std returns the underlying time.Duration value.
func (d Duration) Std() time.Duration {
	return time.Duration(d)
}

// Config holds all server settings loaded from a single YAML file.
type Config struct {
	Server  Server  `yaml:"server"`
	Storage Storage `yaml:"storage"`
	Auth    Auth    `yaml:"auth"`
	Sync    Sync    `yaml:"sync"`
	Admin   Admin   `yaml:"admin"`
}

// Server holds HTTP listener settings for sync endpoints.
type Server struct {
	Bind              string   `yaml:"bind"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	IdleTimeout       Duration `yaml:"idle_timeout"`
	MaxBodyBytes      int64    `yaml:"max_body_bytes"`
}

// Storage holds persistence settings.
type Storage struct {
	Driver string `yaml:"driver"`
	Path   string `yaml:"path"`
}

// Auth holds JWT verification settings (used from step 03).
type Auth struct {
	JWKSURL        string   `yaml:"jwks_url"`
	JWKSCacheTTL   Duration `yaml:"jwks_cache_ttl"`
	AllowedAlgs    []string `yaml:"allowed_algs"`
	Audience       []string `yaml:"audience"`
	Issuer         string   `yaml:"issuer"`
	DevHS256Secret string   `yaml:"dev_hs256_secret"`
}

// Sync holds sync endpoint limits and timeouts.
type Sync struct {
	MaxEnvelopesPerPush int      `yaml:"max_envelopes_per_push"`
	PullLimitDefault    int      `yaml:"pull_limit_default"`
	PullLimitMax        int      `yaml:"pull_limit_max"`
	LivePollTimeout     Duration `yaml:"live_poll_timeout"`
	LiveHeartbeat       Duration `yaml:"live_heartbeat"`
}

// Admin holds operations panel settings (listener in step 07).
type Admin struct {
	Bind  string `yaml:"bind"`
	Token string `yaml:"token"`
}

// Load reads and validates configuration from path.
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

func validateBind(field, bind string) error {
	if strings.TrimSpace(bind) == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if _, _, err := net.SplitHostPort(bind); err != nil {
		return fmt.Errorf("%s %q is not a valid host:port address: %v", field, bind, err)
	}
	return nil
}

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
