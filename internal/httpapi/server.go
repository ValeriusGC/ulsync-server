package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/store"
)

// Server wraps the HTTP listener and route table.
type Server struct {
	httpServer *http.Server
}

// New builds the HTTP server from configuration.
func New(cfg *config.Config, db *store.Store, version string, startedAt time.Time) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler(db, version, startedAt))

	handler := limitPOSTBody(mux, cfg.Server.MaxBodyBytes)

	// Do not set WriteTimeout on the server: long-lived SSE connections arrive in
	// step 06. Per-handler write deadlines use http.ResponseController instead.
	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.Server.Bind,
			Handler:           handler,
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Std(),
			IdleTimeout:       cfg.Server.IdleTimeout.Std(),
		},
	}
}

// ListenAndServe starts accepting connections.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the root HTTP handler (for tests).
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

type storageHealth struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

type healthResponse struct {
	Version   string        `json:"version"`
	StartedAt string        `json:"started_at"`
	Storage   storageHealth `json:"storage"`
}

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

func limitPOSTBody(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}
