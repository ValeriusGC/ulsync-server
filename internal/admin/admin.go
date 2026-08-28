// Package admin serves the read-only operations panel on admin.bind.
//
// Routes are isolated from the sync listener: /admin* never shares server.bind.
// Metrics come from the same /v1 collector passed in at construction time.
package admin

import (
	"context"
	"crypto/subtle"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/auth"
	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/live"
	"github.com/ValeriusGC/ulsync-server/internal/metrics"
	"github.com/ValeriusGC/ulsync-server/internal/store"
)

//go:embed index.html
var pageFS embed.FS

// indexHTML is the embedded operations page served at GET /admin.
var indexHTML []byte

func init() {
	data, err := fs.ReadFile(pageFS, "index.html")
	if err != nil {
		panic("admin: read embedded index.html: " + err.Error())
	}
	indexHTML = data
}

// Options tunes snapshot timing in tests. Zero values select production defaults.
type Options struct {
	// SnapshotEvery is how often the SSE stream emits a state frame. Zero → 1s.
	SnapshotEvery time.Duration
	// StatsEvery is the minimum interval between store.Stats calls. Zero → 5s.
	StatsEvery time.Duration
}

// Server is the admin.bind HTTP listener for the operations panel.
type Server struct {
	cfg       *config.Config
	db        *store.Store
	verifier  *auth.Verifier
	version   string
	startedAt time.Time
	counters  *metrics.Collector
	registry  *live.Registry
	opts      Options

	httpServer *http.Server
	// stopSnapshot cancels the background snapshot ticker goroutine.
	stopSnapshot context.CancelFunc
}

// New constructs the admin listener. It panics when a required dependency is
// nil because that is a wiring bug, not a client error. counters and registry
// must be the same instances as the sync server so panel numbers match reality.
func New(
	cfg *config.Config,
	db *store.Store,
	verifier *auth.Verifier,
	version string,
	startedAt time.Time,
	counters *metrics.Collector,
	registry *live.Registry,
	opts Options,
) *Server {
	if cfg == nil {
		panic("admin.New: cfg is nil")
	}
	if db == nil {
		panic("admin.New: db is nil")
	}
	if verifier == nil {
		panic("admin.New: verifier is nil")
	}
	if counters == nil {
		panic("admin.New: counters is nil")
	}
	if registry == nil {
		panic("admin.New: registry is nil")
	}

	mux := http.NewServeMux()
	s := &Server{
		cfg:       cfg,
		db:        db,
		verifier:  verifier,
		version:   version,
		startedAt: startedAt,
		counters:  counters,
		registry:  registry,
		opts:      opts,
		httpServer: &http.Server{
			Addr:              cfg.Admin.Bind,
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Std(),
			IdleTimeout:       cfg.Server.IdleTimeout.Std(),
		},
	}

	mux.Handle("GET /admin", http.HandlerFunc(s.servePage))

	root := http.Handler(mux)
	if token := strings.TrimSpace(cfg.Admin.Token); token != "" {
		root = requireAdminBearer(token, root)
	}
	s.httpServer.Handler = root

	return s
}

// ListenAndServe accepts connections on admin.bind until shutdown or error.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the admin listener and the snapshot goroutine.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.stopSnapshot != nil {
		s.stopSnapshot()
	}
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the root handler for httptest.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// servePage returns the embedded HTML shell. Live values are filled by SSE in
// a later route; the markup itself carries no bind addresses or paths.
func (s *Server) servePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexHTML)
}

// requireAdminBearer protects all panel routes when admin.token is configured.
// Comparison uses subtle.ConstantTimeCompare on the bearer value bytes.
func requireAdminBearer(want string, next http.Handler) http.Handler {
	wantBytes := []byte(want)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
		if !found || !strings.EqualFold(scheme, "Bearer") {
			writeAdminUnauthorized(w)
			return
		}
		got := []byte(strings.TrimSpace(token))
		if subtle.ConstantTimeCompare(got, wantBytes) != 1 {
			writeAdminUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeAdminUnauthorized is the 401 body for rejected panel bearer tokens.
func writeAdminUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}
