package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/config"
)

func TestHealthReturnsJSON(t *testing.T) {
	t.Parallel()

	cfgPath := filepath.Join("..", "..", "config.example.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	startedAt := time.Date(2026, 8, 26, 7, 35, 42, 0, time.UTC)
	srv := New(cfg, "test-version", startedAt)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	for _, key := range []string{"version", "started_at", "storage"} {
		if body[key] == "" {
			t.Fatalf("response[%q] is empty: %#v", key, body)
		}
	}
	if body["version"] != "test-version" {
		t.Fatalf("version = %q", body["version"])
	}
	if body["storage"] != cfg.Storage.Path {
		t.Fatalf("storage = %q, want %q", body["storage"], cfg.Storage.Path)
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
