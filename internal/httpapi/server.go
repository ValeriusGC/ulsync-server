// Package httpapi serves the HTTP surface of the sync server.
//
// GET /health stays public for process supervisors. Every /v1/* route requires
// a bearer token. POST /v1/sync/push accepts one envelope under last-write-wins.
// POST body size is capped at the root handler so future routes inherit the limit
// without per-route wiring.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/auth"
	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/store"
)

// unauthorizedJSON is the only body returned for a rejected bearer token.
// Distinct messages would let an attacker distinguish expired, unknown kid,
// and malformed tokens.
const unauthorizedJSON = `{"error":"unauthorized"}`

// userIDKey is the context key for the subject placed by requireBearer.
// An unexported type prevents other packages from overwriting it.
type userIDKey struct{}

// Server wraps net/http.Server with the route table for this process.
type Server struct {
	httpServer *http.Server
}

// New constructs an HTTP server bound to cfg.Server.Bind.
//
// db supplies live storage statistics for /health. verifier authenticates
// every /v1/* request. version and startedAt are echoed verbatim in the
// health JSON (startedAt is formatted as RFC 3339 UTC).
//
// WriteTimeout is intentionally unset: long-lived SSE connections arrive in
// step 06. Per-handler write deadlines will use http.ResponseController instead.
func New(cfg *config.Config, db *store.Store, verifier *auth.Verifier, version string, startedAt time.Time) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler(db, version, startedAt))

	// All /v1/* routes share one auth wrapper and one POST body limit ancestor.
	protected := http.NewServeMux()
	protected.HandleFunc("GET /v1/whoami", whoami)
	protected.HandleFunc("POST /v1/sync/push", pushHandler(db, cfg.Sync.MaxEnvelopesPerPush))
	mux.Handle("/v1/", requireBearer(verifier, protected))

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

// UserID returns the subject placed in the context by the auth middleware.
// It panics if the middleware did not run: that is a wiring bug, not a
// request error.
func UserID(ctx context.Context) string {
	id, ok := ctx.Value(userIDKey{}).(string)
	if !ok {
		panic("httpapi.UserID: auth middleware did not run")
	}
	return id
}

// requireBearer wraps next so only requests with a valid bearer token reach
// /v1/* handlers. The verified subject is stored in the request context for
// UserID; failures always return the same 401 body (no token oracle).
func requireBearer(verifier *auth.Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
		if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
			writeUnauthorized(w)
			return
		}
		userID, err := verifier.Verify(r.Context(), strings.TrimSpace(token))
		if err != nil {
			writeUnauthorized(w)
			return
		}
		ctx := context.WithValue(r.Context(), userIDKey{}, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// writeUnauthorized is the only 401 response on /v1/* sync routes.
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(unauthorizedJSON))
}

// whoami returns the authenticated subject. It exists for operators and for
// the token-check path in the operations panel (step 07), not as a debug stub.
func whoami(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		UserID string `json:"user_id"`
	}{UserID: UserID(r.Context())})
}
