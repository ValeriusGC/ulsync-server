package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"gopkg.in/yaml.v3"
)

// statsCacheTTL is how long store.Stats results are reused between snapshot
// ticks. This is an implementation property, not an operator setting: calling
// COUNT(*) every second would deny service under load.
const statsCacheTTL = 5 * time.Second

// sseWriteTimeout bounds each SSE frame write so dead connections do not hold
// the snapshot goroutine forever. A global http.Server.WriteTimeout would kill
// long-lived streams, so the deadline is per frame like live pull in httpapi.
const sseWriteTimeout = 10 * time.Second

// snapshotInterval is the production tick for atomic metrics and SSE frames.
const snapshotInterval = time.Second

// storageJSON is the storage subsection of the operations snapshot.
type storageJSON struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Users     int64  `json:"users"`
	Envelopes int64  `json:"envelopes"`
}

// requestsJSON holds /v1 traffic counters from the shared metrics collector.
type requestsJSON struct {
	InFlight  int64 `json:"in_flight"`
	Total     int64 `json:"total"`
	Status4xx int64 `json:"status_4xx"`
	Status5xx int64 `json:"status_5xx"`
}

// lastErrorJSON is the non-null last_error object in the snapshot wire form.
type lastErrorJSON struct {
	Text string `json:"text"`
	At   string `json:"at"`
}

// Snapshot is the JSON document emitted on GET /admin/events once per tick.
type Snapshot struct {
	Version         string         `json:"version"`
	StartedAt       string         `json:"started_at"`
	UptimeSeconds   int64          `json:"uptime_seconds"`
	Storage         storageJSON    `json:"storage"`
	Requests        requestsJSON   `json:"requests"`
	LiveConnections int            `json:"live_connections"`
	P95Ms           int64          `json:"p95_ms"`
	LastError       *lastErrorJSON `json:"last_error"`
	Config          map[string]any `json:"config"`
}

// snapshotState holds the cached store.Stats and the latest assembled frame.
type snapshotState struct {
	mu sync.RWMutex
	// statsCached holds the last successful store.Stats values.
	statsCached storageJSON
	// statsAt is when statsCached was last refreshed from SQLite.
	statsAt time.Time
	// current is the latest snapshot handed to SSE clients.
	current Snapshot
}

func (s *Server) snapshotEvery() time.Duration {
	if s.opts.SnapshotEvery > 0 {
		return s.opts.SnapshotEvery
	}
	return snapshotInterval
}

func (s *Server) statsEvery() time.Duration {
	if s.opts.StatsEvery > 0 {
		return s.opts.StatsEvery
	}
	return statsCacheTTL
}

// enableSnapshot registers GET /admin/events and starts the ticker goroutine.
func (s *Server) enableSnapshot(mux *http.ServeMux) {
	ctx, cancel := context.WithCancel(context.Background())
	s.stopSnapshot = cancel
	s.snap = &snapshotState{}
	s.refreshStats(ctx, s.snap)
	s.buildSnapshot(s.snap)

	go func() {
		ticker := time.NewTicker(s.snapshotEvery())
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refreshStats(ctx, s.snap)
				s.buildSnapshot(s.snap)
			}
		}
	}()
	mux.Handle("GET /admin/events", http.HandlerFunc(s.serveEvents))
}

func (s *Server) refreshStats(ctx context.Context, state *snapshotState) {
	every := s.statsEvery()
	state.mu.Lock()
	stale := state.statsAt.IsZero() || time.Since(state.statsAt) >= every
	state.mu.Unlock()
	if !stale {
		return
	}

	stats, err := s.db.Stats(ctx)
	state.mu.Lock()
	defer state.mu.Unlock()
	if err != nil {
		if state.statsAt.IsZero() {
			state.statsCached = storageJSON{
				Path:      s.cfg.Storage.Path,
				Users:     0,
				Envelopes: 0,
				SizeBytes: 0,
			}
			state.statsAt = time.Now()
		}
		return
	}
	state.statsCached = storageJSON{
		Path:      stats.Path,
		SizeBytes: stats.SizeBytes,
		Users:     stats.Users,
		Envelopes: stats.Envelopes,
	}
	state.statsAt = time.Now()
}

func (s *Server) buildSnapshot(state *snapshotState) {
	state.mu.Lock()
	storage := state.statsCached
	state.mu.Unlock()

	var lastErr *lastErrorJSON
	if le, ok := s.counters.LastError(); ok {
		lastErr = &lastErrorJSON{
			Text: le.Text,
			At:   le.At.UTC().Format(time.RFC3339),
		}
	}

	snap := Snapshot{
		Version:       s.version,
		StartedAt:     s.startedAt.UTC().Format(time.RFC3339),
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		Storage:       storage,
		Requests: requestsJSON{
			InFlight:  s.counters.InFlight(),
			Total:     s.counters.Total(),
			Status4xx: s.counters.Status4xx(),
			Status5xx: s.counters.Status5xx(),
		},
		LiveConnections: s.registry.Len(),
		P95Ms:           s.counters.P95().Milliseconds(),
		LastError:       lastErr,
		Config:          redactedConfigMap(s.cfg),
	}

	state.mu.Lock()
	state.current = snap
	state.mu.Unlock()
}

func (s *Server) currentSnapshot() Snapshot {
	if s.snap == nil {
		return Snapshot{Config: map[string]any{}}
	}
	s.snap.mu.RLock()
	defer s.snap.mu.RUnlock()
	return s.snap.current
}

func redactedConfigMap(cfg *config.Config) map[string]any {
	redacted := cfg.Redacted()
	raw, err := yaml.Marshal(redacted)
	if err != nil {
		return map[string]any{"error": "config marshal failed"}
	}
	var out map[string]any
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return map[string]any{"error": "config unmarshal failed"}
	}
	return out
}

// serveEvents streams the snapshot as default SSE message events once per tick.
func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if err := s.writeSnapshotFrame(w, s.currentSnapshot()); err != nil {
		return
	}

	ticker := time.NewTicker(s.snapshotEvery())
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := s.writeSnapshotFrame(w, s.currentSnapshot()); err != nil {
				return
			}
		}
	}
}

func (s *Server) writeSnapshotFrame(w http.ResponseWriter, snap Snapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	payload := append([]byte("data: "), data...)
	payload = append(payload, '\n', '\n')

	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// assembleSnapshot builds a snapshot immediately for tests without SSE.
func (s *Server) assembleSnapshot(ctx context.Context) Snapshot {
	state := &snapshotState{}
	s.refreshStats(ctx, state)
	s.buildSnapshot(state)
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.current
}
