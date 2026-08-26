package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/config"
)

// Server wraps the HTTP listener and route table.
type Server struct {
	httpServer *http.Server
}

// New builds the HTTP server from configuration.
func New(cfg *config.Config, version string, startedAt time.Time) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler(version, startedAt, cfg.Storage.Path))

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

type healthResponse struct {
	Version   string `json:"version"`
	StartedAt string `json:"started_at"`
	Storage   string `json:"storage"`
}

func healthHandler(version string, startedAt time.Time, storagePath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(healthResponse{
			Version:   version,
			StartedAt: startedAt.UTC().Format(time.RFC3339),
			Storage:   storagePath,
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
