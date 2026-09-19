package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/auth"
	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/httpapi"
	"github.com/ValeriusGC/ulsync-server/internal/store"
	"gopkg.in/yaml.v3"
)

const (
	// seedJWKSURL is the RFC 2606 reserved name used as a dead JWKS so a
	// failed fetch cannot fail startup (NewVerifier already behaves that way).
	seedJWKSURL = "https://example.invalid/jwks.json"
	// seedSecret is an operator-chosen string; the box must not replace it
	// with a canned local-dev-only value.
	seedSecret = "operator-chosen-shared-secret"
	// placeholder is the embedded Supabase JWKS URL that URL-seed must not write.
	placeholder = "https://<project>.supabase.co/auth/v1/.well-known/jwks.json"
)

// TestSeed pins first-run CLI behaviour. Subtest names are frozen for accept_40.sh.
func TestSeed(t *testing.T) {
	t.Run("missing config and missing both seed flags exits 1 naming the flags", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.yaml")
		stderr := &bytes.Buffer{}
		code := runMain([]string{"-config", path}, io.Discard, stderr)
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		msg := stderr.String()
		if !strings.Contains(msg, "-jwks-url") || !strings.Contains(msg, "-shared-secret") {
			t.Fatalf("stderr = %q, want flag names -jwks-url and -shared-secret", msg)
		}
		if strings.Contains(msg, "read config") {
			t.Fatalf("stderr = %q, must not wrap as read config", msg)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("config file created: %v", err)
		}
	})

	t.Run("jwks-url and shared-secret together exit 1", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "nested", "config.yaml")
		stderr := &bytes.Buffer{}
		code := runMain([]string{
			"-config", path,
			"-jwks-url", seedJWKSURL,
			"-shared-secret", seedSecret,
		}, io.Discard, stderr)
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		msg := stderr.String()
		if !strings.Contains(msg, "-jwks-url") || !strings.Contains(msg, "-shared-secret") {
			t.Fatalf("stderr = %q, want both flag names", msg)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("config file created: %v", err)
		}
	})

	t.Run("jwks-url seeds yaml then GET /health is 200", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "etc", "ulsync", "config.yaml")
		if err := seedConfig(path, seedJWKSURL, ""); err != nil {
			t.Fatalf("seedConfig() error = %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %o, want 0600", info.Mode().Perm())
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Auth.JWKSURL != seedJWKSURL {
			t.Fatalf("jwks_url = %q, want flag %q", cfg.Auth.JWKSURL, seedJWKSURL)
		}
		if cfg.Auth.JWKSURL == placeholder {
			t.Fatal("jwks_url is the Supabase placeholder")
		}
		if cfg.Server.Bind != "0.0.0.0:8080" {
			t.Fatalf("server.bind = %q, want 0.0.0.0:8080", cfg.Server.Bind)
		}
		wantDB := filepath.Join(filepath.Dir(path), "data", "ulsync.db")
		if cfg.Storage.Path != wantDB {
			t.Fatalf("storage.path = %q, want absolute %q", cfg.Storage.Path, wantDB)
		}
		if cfg.Origin != "" {
			t.Fatalf("origin = %q, want empty", cfg.Origin)
		}
		if got := liveHealthStatus(t, path); got != http.StatusOK {
			t.Fatalf("GET /health status = %d, want 200", got)
		}
	})

	t.Run("existing yaml is not overwritten", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.yaml")
		original := []byte("server:\n  bind: \"0.0.0.0:8080\"\nstorage:\n  path: \"./keep.db\"\nauth:\n  jwks_url: \"https://already.example/jwks.json\"\n")
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := ensureConfig(path, seedJWKSURL, seedSecret); err != nil {
			t.Fatalf("ensureConfig() error = %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if !bytes.Equal(got, original) {
			t.Fatalf("config rewritten:\n%s", got)
		}
	})

	t.Run("seeded yaml has empty dev_hs256_secret", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := seedConfig(path, seedJWKSURL, ""); err != nil {
			t.Fatalf("seedConfig() error = %v", err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Auth.DevHS256Secret != "" {
			t.Fatalf("dev_hs256_secret = %q, want empty", cfg.Auth.DevHS256Secret)
		}
	})

	t.Run("seeded admin.bind is loopback", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := seedConfig(path, seedJWKSURL, ""); err != nil {
			t.Fatalf("seedConfig() error = %v", err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Admin.Bind != "127.0.0.1:8081" {
			t.Fatalf("admin.bind = %q, want 127.0.0.1:8081", cfg.Admin.Bind)
		}
		if strings.TrimSpace(cfg.Admin.Token) != "" {
			t.Fatalf("admin.token = %q, want empty", cfg.Admin.Token)
		}
	})

	t.Run("shared-secret seeds yaml then GET /health is 200", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := seedConfig(path, "", seedSecret); err != nil {
			t.Fatalf("seedConfig() error = %v", err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Auth.DevHS256Secret != seedSecret {
			t.Fatalf("dev_hs256_secret = %q, want flag", cfg.Auth.DevHS256Secret)
		}
		if cfg.Auth.JWKSURL != "" {
			t.Fatalf("jwks_url = %q, want empty after secret seed", cfg.Auth.JWKSURL)
		}
		if !containsAlg(cfg.Auth.AllowedAlgs, "HS256") {
			t.Fatalf("allowed_algs = %v, want HS256", cfg.Auth.AllowedAlgs)
		}
		if got := liveHealthStatus(t, path); got != http.StatusOK {
			t.Fatalf("GET /health status = %d, want 200", got)
		}
	})
}

// TestVersionAndHealthcheckDoNotSeed checks that -version and -healthcheck
// still skip the config file and never write one.
func TestVersionAndHealthcheckDoNotSeed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	stdout := &bytes.Buffer{}
	code := runMain([]string{"-config", path, "-version"}, stdout, io.Discard)
	if code != 0 {
		t.Fatalf("-version exit = %d, want 0", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("-version seeded a config file")
	}

	code = runMain([]string{"-config", path, "-healthcheck"}, io.Discard, io.Discard)
	if code != 1 {
		t.Fatalf("-healthcheck exit = %d, want 1 when nothing listens", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("-healthcheck seeded a config file")
	}
}

// liveHealthStatus copies seeded YAML, binds 127.0.0.1 to a free port, and
// serves the existing HTTP stack so go test never occupies 8080.
func liveHealthStatus(t *testing.T, seededPath string) int {
	t.Helper()

	orig, err := config.Load(seededPath)
	if err != nil {
		t.Fatalf("Load(seeded) error = %v", err)
	}
	if orig.Server.Bind != "0.0.0.0:8080" {
		t.Fatalf("seeded server.bind = %q, want 0.0.0.0:8080", orig.Server.Bind)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	addr := ln.Addr().String()

	orig.Server.Bind = addr
	raw, err := yaml.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	copyPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(copyPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := config.Load(copyPath)
	if err != nil {
		t.Fatalf("Load(copy) error = %v", err)
	}

	db, err := store.Open(context.Background(), cfg.Storage)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	client := &http.Client{Timeout: 250 * time.Millisecond}
	verifier, err := auth.NewVerifier(cfg.Auth, client, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}

	api := httpapi.New(cfg, db, verifier, "test", time.Now().UTC())
	hs := &http.Server{Handler: api.Handler()}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { _ = hs.Close() })

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health error = %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// containsAlg reports whether want is present in algs.
func containsAlg(algs []string, want string) bool {
	for _, alg := range algs {
		if alg == want {
			return true
		}
	}
	return false
}
