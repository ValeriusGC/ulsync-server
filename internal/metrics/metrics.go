// Package metrics counts /v1 sync traffic and recent response latencies for the
// operations panel. Admin routes must not use this collector: panel polls would
// dominate the totals the operator is trying to read.
package metrics

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// latencyBufferCap is how many recent /v1 response durations the ring retains.
// One thousand twenty-four samples balance a useful p95 window against sorting
// cost on the once-per-second snapshot tick.
const latencyBufferCap = 1024

// LastError describes the most recent /v1 handler failure (HTTP status ≥ 400).
// It never carries a request body, envelope payload, or user_id.
type LastError struct {
	// Text is method, path without query, and status (for example "GET /v1/sync/pull: 400").
	Text string
	// At is when the error was recorded, in UTC.
	At time.Time
}

// Collector holds atomic counters and a mutex-protected latency ring for p95.
type Collector struct {
	inFlight  atomic.Int64
	total     atomic.Int64
	status4xx atomic.Int64
	status5xx atomic.Int64

	mu        sync.Mutex
	latencies []time.Duration // ring buffer, capacity latencyBufferCap
	ringIdx   int             // next write index in latencies
	ringCount int             // number of valid samples in the ring (≤ cap)

	lastErr LastError
	hasErr  atomic.Bool
}

// New returns an empty metrics collector for one process.
func New() *Collector {
	return &Collector{
		latencies: make([]time.Duration, latencyBufferCap),
	}
}

// Wrap records /v1 request counts and latencies around next. Place it outside
// requireBearer so rejected tokens still increment status_4xx.
func (c *Collector) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.inFlight.Add(1)
		defer c.inFlight.Add(-1)

		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		elapsed := time.Since(start)

		c.total.Add(1)
		switch {
		case sw.status >= 500:
			c.status5xx.Add(1)
		case sw.status >= 400:
			c.status4xx.Add(1)
		}

		if sw.status >= 400 {
			c.recordLastError(r.Method, r.URL.Path, sw.status)
		}

		// live=sse and live=poll hold connections open for seconds or hours.
		// Their duration is connection age, not request latency, so p95 would
		// become meaningless if those samples entered the ring.
		live := r.URL.Query().Get("live")
		if live != "sse" && live != "poll" {
			c.observeLatency(elapsed)
		}
	})
}

// InFlight returns how many /v1 requests are currently inside Wrap.
func (c *Collector) InFlight() int64 {
	return c.inFlight.Load()
}

// Total returns the number of completed /v1 requests since process start.
func (c *Collector) Total() int64 {
	return c.total.Load()
}

// Status4xx returns completed /v1 responses with status 400–499.
func (c *Collector) Status4xx() int64 {
	return c.status4xx.Load()
}

// Status5xx returns completed /v1 responses with status 500–599.
func (c *Collector) Status5xx() int64 {
	return c.status5xx.Load()
}

// P95 returns the duration below which 95% of ring samples completed. Zero when
// the ring is empty. Computed on a copy under lock by the snapshot goroutine.
func (c *Collector) P95() time.Duration {
	c.mu.Lock()
	n := c.ringCount
	samples := make([]time.Duration, n)
	copy(samples, c.latencies[:n])
	c.mu.Unlock()

	if n == 0 {
		return 0
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	idx := int(math.Ceil(0.95*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return samples[idx]
}

// LastError returns the most recent HTTP error and true, or zero and false.
func (c *Collector) LastError() (LastError, bool) {
	if !c.hasErr.Load() {
		return LastError{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr, true
}

func (c *Collector) recordLastError(method, path string, status int) {
	c.mu.Lock()
	c.lastErr = LastError{
		Text: fmt.Sprintf("%s %s: %d", method, path, status),
		At:   time.Now().UTC(),
	}
	c.mu.Unlock()
	c.hasErr.Store(true)
}

func (c *Collector) observeLatency(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latencies[c.ringIdx] = d
	c.ringIdx = (c.ringIdx + 1) % latencyBufferCap
	if c.ringCount < latencyBufferCap {
		c.ringCount++
	}
}

// statusWriter captures the response status for metrics. It must expose Unwrap
// and Flush so http.ResponseController in live SSE reaches the real connection.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

// WriteHeader records the status code for metrics classification.
func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.wrote = true
	sw.ResponseWriter.WriteHeader(code)
}

// Write treats the first Write as implicit 200 if WriteHeader was not called.
func (sw *statusWriter) Write(b []byte) (int, error) {
	if !sw.wrote {
		sw.wrote = true
	}
	return sw.ResponseWriter.Write(b)
}

// Unwrap returns the underlying ResponseWriter for http.ResponseController.
func (sw *statusWriter) Unwrap() http.ResponseWriter {
	return sw.ResponseWriter
}

// Flush forwards to an http.Flusher when present so SSE streams flush.
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
