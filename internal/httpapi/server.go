// Package httpapi serves the HTTP surface of the sync server.
//
// Round 1 exposes only GET /health for process supervisors. Sync endpoints
// (/v1/sync/push, pull, live) arrive in later steps. POST body size is capped
// at the root handler so future routes inherit the limit without per-route wiring.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/store"
)

// Server wraps net/http.Server with the route table for this process.
type Server struct {
	httpServer *http.Server
}

// New constructs an HTTP server bound to cfg.Server.Bind.
//
// db supplies live storage statistics for /health. version and startedAt are
// echoed verbatim in the health JSON (startedAt is formatted as RFC 3339 UTC).
//
// WriteTimeout is intentionally unset: long-lived SSE connections arrive in
// step 06. Per-handler write deadlines will use http.ResponseController instead.
func New(cfg *config.Config, db *store.Store, version string, startedAt time.Time) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler(db, version, startedAt))

	handler := limitPOSTBody(mux, cfg.Server.MaxBodyBytes)

	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.Server.Bind,
			Handler:           handler,
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Std(),
			IdleTimeout:       cfg.Server.IdleTimeout.Std(),
		},
	}
}

// ListenAndServe accepts connections until the server is shut down or an error
// occurs. It blocks the calling goroutine.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops accepting new connections and waits for in-flight
// requests to finish, or until ctx is canceled.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the root http.Handler, primarily for httptest in unit tests.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// storageHealth is the JSON object under the "storage" key in GET /health.
//
// Example:
//
//	{"path":"./data/ulsync.db","size_bytes":4096}
type storageHealth struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

// healthResponse is the JSON body of GET /health.
//
// Example:
//
//	{
//	  "version":"abc1234",
//	  "started_at":"2026-08-26T09:43:15Z",
//	  "storage":{"path":"./data/ulsync.db","size_bytes":4096}
//	}
type healthResponse struct {
	Version   string        `json:"version"`
	StartedAt string        `json:"started_at"`
	Storage   storageHealth `json:"storage"`
}

// healthHandler reports process liveness and current database file statistics.
// If store.Stats fails, the handler responds with 503 Service Unavailable
// rather than a success payload with stale or zeroed values.
func healthHandler(db *store.Store, version string, startedAt time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats, err := db.Stats(r.Context())
		if err != nil {
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(healthResponse{
			Version:   version,
			StartedAt: startedAt.UTC().Format(time.RFC3339),
			Storage: storageHealth{
				Path:      stats.Path,
				SizeBytes: stats.SizeBytes,
			},
		})
	}
}

// limitPOSTBody wraps next so POST requests cannot read more than maxBytes from
// the body. Oversized bodies receive 413 Request Entity TooLarge via
// http.MaxBytesReader before the route handler runs.
func limitPOSTBody(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}
