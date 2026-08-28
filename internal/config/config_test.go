package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestLoadExampleConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "config.example.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Bind != "0.0.0.0:8080" {
		t.Fatalf("server.bind = %q, want 0.0.0.0:8080", cfg.Server.Bind)
	}
	if cfg.Server.ReadHeaderTimeout.Std() != 5*time.Second {
		t.Fatalf("read_header_timeout = %v, want 5s", cfg.Server.ReadHeaderTimeout.Std())
	}
	if cfg.Storage.Path != "./data/ulsync.db" {
		t.Fatalf("storage.path = %q", cfg.Storage.Path)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, "server:\n  bnid: \"0.0.0.0:8080\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "bnid") && !strings.Contains(err.Error(), "server") {
		t.Fatalf("error = %q, want field name in message", err)
	}
}

func TestValidateRejectsExposedAdminWithoutToken(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, strings.Join([]string{
		"server:",
		"  bind: \"0.0.0.0:8080\"",
		"storage:",
		"  path: \"./data/ulsync.db\"",
		"admin:",
		"  bind: \"0.0.0.0:8081\"",
		"  token: \"\"",
	}, "\n")+"\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() expected error for exposed admin bind")
	}
	msg := err.Error()
	if !strings.Contains(msg, "admin.bind") || !strings.Contains(msg, "admin.token") {
		t.Fatalf("error = %q, want admin.bind and admin.token guidance", err)
	}
}

func TestValidateRejectsPullLimitDefaultAboveMax(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, strings.Join([]string{
		"server:",
		"  bind: \"0.0.0.0:8080\"",
		"storage:",
		"  path: \"./data/ulsync.db\"",
		"sync:",
		"  pull_limit_default: 600",
		"  pull_limit_max: 500",
	}, "\n")+"\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() expected validation error")
	}
	if !strings.Contains(err.Error(), "sync.pull_limit_default") {
		t.Fatalf("error = %q", err)
	}
}

func TestDurationUnmarshalsFromString(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, strings.Join([]string{
		"server:",
		"  bind: \"127.0.0.1:9000\"",
		"  read_header_timeout: \"5s\"",
		"storage:",
		"  path: \"./data/ulsync.db\"",
	}, "\n")+"\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.ReadHeaderTimeout.Std() != 5*time.Second {
		t.Fatalf("read_header_timeout = %v, want 5s", cfg.Server.ReadHeaderTimeout.Std())
	}
}

func TestRedactedMasksNonEmptySecrets(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Auth:  Auth{DevHS256Secret: "super-secret"},
		Admin: Admin{Token: "panel-secret"},
	}
	got := cfg.Redacted()
	if got.Auth.DevHS256Secret != redactedSecret {
		t.Fatalf("dev_hs256_secret = %q, want %q", got.Auth.DevHS256Secret, redactedSecret)
	}
	if got.Admin.Token != redactedSecret {
		t.Fatalf("admin.token = %q, want %q", got.Admin.Token, redactedSecret)
	}
}

func TestRedactedMarksEmptySecrets(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Auth:  Auth{DevHS256Secret: ""},
		Admin: Admin{Token: "  "},
	}
	got := cfg.Redacted()
	if got.Auth.DevHS256Secret != emptySecretLabel {
		t.Fatalf("dev_hs256_secret = %q, want %q", got.Auth.DevHS256Secret, emptySecretLabel)
	}
	if got.Admin.Token != emptySecretLabel {
		t.Fatalf("admin.token = %q, want %q", got.Admin.Token, emptySecretLabel)
	}
}

func TestRedactedDoesNotMutateOriginal(t *testing.T) {
	t.Parallel()

	const secret = "keep-me"
	cfg := Config{
		Auth: Auth{
			DevHS256Secret: secret,
			AllowedAlgs:    []string{"ES256"},
			Audience:       []string{"aud"},
		},
		Admin: Admin{Token: "panel"},
	}
	_ = cfg.Redacted()
	if cfg.Auth.DevHS256Secret != secret {
		t.Fatalf("original dev_hs256_secret mutated to %q", cfg.Auth.DevHS256Secret)
	}
	if cfg.Admin.Token != "panel" {
		t.Fatalf("original admin.token mutated to %q", cfg.Admin.Token)
	}
	if len(cfg.Auth.AllowedAlgs) != 1 || cfg.Auth.AllowedAlgs[0] != "ES256" {
		t.Fatalf("original allowed_algs = %v", cfg.Auth.AllowedAlgs)
	}
}

func TestRedactedYAMLDoesNotContainSecret(t *testing.T) {
	t.Parallel()

	const secret = "YAML-LEAK-TEST"
	cfg := Config{
		Auth:  Auth{DevHS256Secret: secret},
		Admin: Admin{Token: "panel"},
	}
	data, err := yaml.Marshal(cfg.Redacted())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	text := string(data)
	if strings.Contains(text, secret) {
		t.Fatalf("redacted YAML contains secret %q:\n%s", secret, text)
	}
	if !strings.Contains(text, redactedSecret) {
		t.Fatalf("redacted YAML missing placeholder %q:\n%s", redactedSecret, text)
	}
}

func TestDurationMarshalYAMLRoundTrip(t *testing.T) {
	t.Parallel()

	d := Duration(5 * time.Second)
	raw, err := yaml.Marshal(map[string]Duration{"timeout": d})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(raw), "5000000000") {
		t.Fatalf("Marshal() wrote nanoseconds: %s", raw)
	}
	if !strings.Contains(string(raw), "5s") {
		t.Fatalf("Marshal() = %q, want human-readable 5s", raw)
	}
}

// writeTempConfig writes YAML to t.TempDir() and returns the file path for Load tests.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
