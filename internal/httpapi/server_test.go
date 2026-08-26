package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/auth"
	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/store"
)

func TestHealthReturnsJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &config.Config{
		Server: config.Server{
			Bind:              "127.0.0.1:0",
			ReadHeaderTimeout: config.Duration(5 * time.Second),
			IdleTimeout:       config.Duration(120 * time.Second),
			MaxBodyBytes:      1024,
		},
		Storage: config.Storage{
			Driver: "sqlite",
			Path:   filepath.Join(dir, "ulsync.db"),
		},
	}

	ctx := context.Background()
	db, err := store.Open(ctx, cfg.Storage)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer db.Close()

	startedAt := time.Date(2026, 8, 26, 7, 35, 42, 0, time.UTC)
	srv := New(cfg, db, testVerifier(t), "test-version", startedAt)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}

	var body struct {
		Version   string `json:"version"`
		StartedAt string `json:"started_at"`
		Storage   struct {
			Path      string `json:"path"`
			SizeBytes int64  `json:"size_bytes"`
		} `json:"storage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if body.Version != "test-version" {
		t.Fatalf("version = %q", body.Version)
	}
	if body.StartedAt == "" {
		t.Fatal("started_at is empty")
	}
	if body.Storage.Path != cfg.Storage.Path {
		t.Fatalf("storage.path = %q, want %q", body.Storage.Path, cfg.Storage.Path)
	}
	if body.Storage.SizeBytes <= 0 {
		t.Fatalf("storage.size_bytes = %d, want > 0", body.Storage.SizeBytes)
	}
}

func TestLimitPOSTBodyRejectsOversizedPayload(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	handler := limitPOSTBody(mux, 8)
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("0123456789"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestAuthHealthNoHeader(t *testing.T) {
	t.Parallel()

	srv, _ := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("WWW-Authenticate = %q, want empty on /health", rec.Header().Get("WWW-Authenticate"))
	}
}

func TestAuthWhoamiNoHeader(t *testing.T) {
	t.Parallel()

	srv, _ := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/whoami", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
	}
	if body := rec.Body.String(); body != unauthorizedJSON {
		t.Fatalf("body = %q, want %q", body, unauthorizedJSON)
	}
}

func newTestAPI(t *testing.T) (*Server, *config.Config) {
	t.Helper()

	dir := t.TempDir()
	cfg := &config.Config{
		Server: config.Server{
			Bind:              "127.0.0.1:0",
			ReadHeaderTimeout: config.Duration(5 * time.Second),
			IdleTimeout:       config.Duration(120 * time.Second),
			MaxBodyBytes:      1024,
		},
		Storage: config.Storage{
			Driver: "sqlite",
			Path:   filepath.Join(dir, "ulsync.db"),
		},
	}
	db, err := store.Open(context.Background(), cfg.Storage)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	startedAt := time.Date(2026, 8, 26, 7, 35, 42, 0, time.UTC)
	return New(cfg, db, testVerifier(t), "test-version", startedAt), cfg
}

func testVerifier(t *testing.T) *auth.Verifier {
	t.Helper()

	path := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(path, []byte(`{"keys":[]}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	v, err := auth.NewVerifier(config.Auth{
		JWKSFile:     path,
		JWKSCacheTTL: config.Duration(10 * time.Minute),
		AllowedAlgs:  []string{"ES256", "RS256"},
	}, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	return v
}
