package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWrapInFlight(t *testing.T) {
	t.Parallel()

	c := New()
	block := make(chan struct{})
	h := c.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))

	done := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/v1/whoami", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for c.InFlight() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("InFlight() never reached 1")
		}
		time.Sleep(time.Millisecond)
	}
	close(block)
	<-done
	if c.InFlight() != 0 {
		t.Fatalf("InFlight() = %d, want 0 after completion", c.InFlight())
	}
}

func TestWrapStatusCounters(t *testing.T) {
	t.Parallel()

	c := New()
	c401 := c.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	c500 := c.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	req401 := httptest.NewRequest(http.MethodGet, "/v1/whoami", nil)
	c401.ServeHTTP(httptest.NewRecorder(), req401)
	req500 := httptest.NewRequest(http.MethodGet, "/v1/sync/pull", nil)
	c500.ServeHTTP(httptest.NewRecorder(), req500)

	if c.Status4xx() != 1 {
		t.Fatalf("Status4xx() = %d, want 1", c.Status4xx())
	}
	if c.Status5xx() != 1 {
		t.Fatalf("Status5xx() = %d, want 1", c.Status5xx())
	}
	if c.Total() != 2 {
		t.Fatalf("Total() = %d, want 2", c.Total())
	}
}

func TestP95FromRing(t *testing.T) {
	t.Parallel()

	c := New()
	for i := 1; i <= 100; i++ {
		c.observeLatency(time.Duration(i) * time.Millisecond)
	}
	got := c.P95()
	want := 95 * time.Millisecond
	if got != want {
		t.Fatalf("P95() = %v, want %v", got, want)
	}
}

func TestLastErrorTextHasNoBody(t *testing.T) {
	t.Parallel()

	c := New()
	h := c.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/sync/push", strings.NewReader("secret-body"))
	h.ServeHTTP(httptest.NewRecorder(), req)

	err, ok := c.LastError()
	if !ok {
		t.Fatal("LastError() = false, want true")
	}
	if strings.Contains(err.Text, "secret-body") {
		t.Fatalf("LastError text leaked body: %q", err.Text)
	}
	if err.Text != "POST /v1/sync/push: 400" {
		t.Fatalf("LastError text = %q", err.Text)
	}
}

func TestLiveSSEDoesNotMoveP95Ring(t *testing.T) {
	t.Parallel()

	c := New()
	var wg sync.WaitGroup
	wg.Add(1)
	h := c.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer wg.Done()
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/sync/pull?live=sse", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	wg.Wait()

	if c.Total() != 1 {
		t.Fatalf("Total() = %d, want 1", c.Total())
	}
	if c.P95() != 0 {
		t.Fatalf("P95() = %v, want 0 when only live=sse ran", c.P95())
	}

	c.observeLatency(3 * time.Millisecond)
	if c.P95() != 3*time.Millisecond {
		t.Fatalf("P95() = %v after manual sample", c.P95())
	}
}

func TestStatusWriterUnwrapFlush(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}
	if sw.Unwrap() != rec {
		t.Fatal("Unwrap() did not return underlying writer")
	}
	sw.WriteHeader(http.StatusOK)
	if _, err := sw.Write([]byte("x")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}
