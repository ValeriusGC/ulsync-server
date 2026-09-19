package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// algHS256 is the JWT HMAC algorithm. It is not in the embedded defaults
// because a process that holds the matching secret can forge any subject.
const algHS256 = "HS256"

// Seed builds a first-run Config for the YAML that will be written at
// configPath. Exactly one of jwksURL or sharedSecret must be non-empty: the
// two authorities cannot be merged without silently picking one (a URL would
// hide a leftover secret that forges tokens; a secret would still fetch a
// foreign JWKS). This process verifies tokens and never issues them, so a
// private PEM is not a seed input and there is no key-file flag here.
//
// origin is left empty: seeding is not an authored pin. admin.bind is
// loopback with an empty token because a seeded Ubuntu process is not the
// Compose container that publishes 0.0.0.0:8081. storage.path is absolute
// under the config directory so a later cwd change does not move the database.
//
// serverBind and adminBind are optional first-run listen addresses (host:port).
// Empty keeps 0.0.0.0:8080 and 127.0.0.1:8081. A second store on the same
// host passes a different pair so two prefixes do not share port 8080.
func Seed(configPath, jwksURL, sharedSecret, serverBind, adminBind string) (*Config, error) {
	jwksURL = strings.TrimSpace(jwksURL)
	sharedSecret = strings.TrimSpace(sharedSecret)
	if (jwksURL == "") == (sharedSecret == "") {
		return nil, fmt.Errorf("exactly one of jwksURL or sharedSecret must be non-empty")
	}

	var cfg Config
	if err := yaml.Unmarshal(defaultConfigYAML, &cfg); err != nil {
		return nil, fmt.Errorf("parse embedded defaults: %w", err)
	}

	abs, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("absolute config path: %w", err)
	}

	cfg.Origin = ""
	cfg.Server.Bind = "0.0.0.0:8080"
	cfg.Storage.Driver = "sqlite"
	cfg.Storage.Path = filepath.Join(filepath.Dir(abs), "data", "ulsync.db")
	cfg.Admin.Bind = "127.0.0.1:8081"
	cfg.Admin.Token = ""

	if b := strings.TrimSpace(serverBind); b != "" {
		if err := validateBind("server.bind", b); err != nil {
			return nil, err
		}
		cfg.Server.Bind = b
	}
	if b := strings.TrimSpace(adminBind); b != "" {
		if err := validateBind("admin.bind", b); err != nil {
			return nil, err
		}
		cfg.Admin.Bind = b
	}
	if !isLoopbackBind(cfg.Admin.Bind) && strings.TrimSpace(cfg.Admin.Token) == "" {
		return nil, fmt.Errorf("admin.bind listens on a non-loopback address while admin.token is empty; bind to 127.0.0.1 or set admin.token")
	}

	if jwksURL != "" {
		cfg.Auth.JWKSURL = jwksURL
		cfg.Auth.DevHS256Secret = ""
		return &cfg, nil
	}

	cfg.Auth.JWKSURL = ""
	cfg.Auth.DevHS256Secret = sharedSecret
	cfg.Auth.AllowedAlgs = withHS256(cfg.Auth.AllowedAlgs)
	return &cfg, nil
}

// withHS256 returns a copy of algs that contains HS256. The copy avoids
// aliasing the slice unmarshalled from embedded defaults.
func withHS256(algs []string) []string {
	out := make([]string, 0, len(algs)+1)
	seen := false
	for _, alg := range algs {
		out = append(out, alg)
		if alg == algHS256 {
			seen = true
		}
	}
	if !seen {
		out = append(out, algHS256)
	}
	return out
}
