package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeHealthOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if got := probeHealth(healthClient(), srv.URL); got != 0 {
		t.Fatalf("probeHealth(200) = %d, want 0", got)
	}
}

func TestProbeHealthServiceUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if got := probeHealth(healthClient(), srv.URL); got != 1 {
		t.Fatalf("probeHealth(503) = %d, want 1", got)
	}
}

func TestProbeHealthConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	addr := srv.URL
	srv.Close()

	if got := probeHealth(healthClient(), addr); got != 1 {
		t.Fatalf("probeHealth(closed) = %d, want 1", got)
	}
}

func TestHealthcheckURLConstant(t *testing.T) {
	const want = "http://127.0.0.1:8080/health"
	if healthcheckURL != want {
		t.Fatalf("healthcheckURL = %q, want %q", healthcheckURL, want)
	}
}
