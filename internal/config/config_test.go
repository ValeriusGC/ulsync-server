package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
