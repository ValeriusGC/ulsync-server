package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"gopkg.in/yaml.v3"
)

// ensureConfig seeds path when the file is absent. An existing file is left
// untouched even if jwksURL or sharedSecret is set: after the first start the
// operator's YAML is the source of truth, not a repeat of the install flags.
//
// A missing file requires exactly one of the two flags. Neither, or both,
// returns an error that names -jwks-url and -shared-secret so a stranger
// without YAML sees the flags, not a wrapped errno from read config.
func ensureConfig(path, jwksURL, sharedSecret, listen, adminListen string) error {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return nil
	case !os.IsNotExist(err):
		return fmt.Errorf("stat config: %w", err)
	}

	jwksURL = strings.TrimSpace(jwksURL)
	sharedSecret = strings.TrimSpace(sharedSecret)
	switch {
	case jwksURL == "" && sharedSecret == "":
		return fmt.Errorf("config file %q is missing; pass exactly one of -jwks-url or -shared-secret to seed it", path)
	case jwksURL != "" && sharedSecret != "":
		return fmt.Errorf("-jwks-url and -shared-secret are mutually exclusive; pass exactly one to seed %q", path)
	}
	return seedConfig(path, jwksURL, sharedSecret, listen, adminListen)
}

// seedConfig writes a first-run YAML at path. Exactly one of jwksURL or
// sharedSecret must be non-empty. It does not overwrite an existing file:
// the operator's edits are the source of truth after the first start.
//
// URL seed keeps dev_hs256_secret empty so the box does not invent
// local-dev-only. Secret seed leaves jwks_url empty so applyDefaults cannot
// point the process at the Supabase placeholder. A private PEM is not accepted:
// this process verifies tokens, it does not issue them.
func seedConfig(path, jwksURL, sharedSecret, listen, adminListen string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config file %q already exists", path)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat config: %w", err)
	}

	cfg, err := config.Seed(path, jwksURL, sharedSecret, listen, adminListen)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return writeFileAtomic(path, buf.Bytes(), 0o600)
}

// writeFileAtomic creates the parent directory of path, writes data to a
// temporary file in that directory, and Rename's it into place so a crash
// cannot leave a truncated YAML at the operator path. Mode is set on the
// temp file before the rename (0600 for a file that may hold a shared secret).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".ulsync-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config file %q already exists", path)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat config: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	cleanup = false
	return nil
}
