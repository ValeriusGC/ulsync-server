package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// healthcheckURL is the loopback endpoint Docker HEALTHCHECK probes. The sync
// listener must bind 0.0.0.0:8080 inside the container so this URL reaches it.
const healthcheckURL = "http://127.0.0.1:8080/health"

// healthcheckTimeout bounds how long a probe waits for /health. Shorter than
// Docker's HEALTHCHECK --timeout so the orchestrator sees a clean exit code.
const healthcheckTimeout = 2 * time.Second

// healthClient returns an HTTP client for container probes. A dedicated client
// avoids mutating http.DefaultClient and enforces the probe timeout.
func healthClient() *http.Client {
	return &http.Client{Timeout: healthcheckTimeout}
}

// probeHealth performs GET url and returns 0 only when the status is 200.
// Any other status, network error, or body read failure prints to stderr and
// returns 1 so Docker marks the container unhealthy without a shell or curl.
func probeHealth(client *http.Client, url string) int {
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		fmt.Fprintf(os.Stderr, "healthcheck: HTTP %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
