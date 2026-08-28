package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

func TestLivePollWakesOnPush(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- env.pullCtx(t, ctx, token, "since=0&live=poll")
	}()
	waitLiveLen(t, env.srv, 1, 2*time.Second)
	pushOrFatal(t, env, token, validWireEnvelope())

	select {
	case rec := <-done:
		got := decodePull(t, rec)
		if len(got.Envelopes) != 1 {
			t.Fatalf("len = %d, want 1; body = %q", len(got.Envelopes), rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poll did not return within 2s after push")
	}
}

func TestLiveSSEEmitsCatchUpBeforeHeartbeat(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	pushOrFatal(t, env, token, validWireEnvelope())

	ts := startStreamServer(t, env)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp := openSSE(t, ctx, ts, token, "since=0&live=sse")
	defer resp.Body.Close()

	sawEnvelope := false
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		switch sc.Text() {
		case ": ping":
			t.Fatal("heartbeat arrived before catch-up cursor")
		case "event: envelope":
			sawEnvelope = true
		case "event: cursor":
			if !sawEnvelope {
				t.Fatal("cursor event before envelope event")
			}
			return
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	t.Fatal("stream ended before cursor event")
}

func TestLiveSSEWakesOnPush(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	ts := startStreamServer(t, env)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := openSSE(t, ctx, ts, token, "since=0&live=sse")
	defer resp.Body.Close()

	waitLiveLen(t, env.srv, 1, 2*time.Second)
	pushOrFatal(t, env, token, validWireEnvelope())
	waitSSELine(t, resp.Body, "event: envelope", 2*time.Second)
}

func TestLiveSSEHeartbeat(t *testing.T) {
	t.Parallel()

	env := newHTTPEnvWith(t, nil, func(cfg *config.Config) {
		cfg.Sync.LiveHeartbeat = config.Duration(100 * time.Millisecond)
	})
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	ts := startStreamServer(t, env)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp := openSSE(t, ctx, ts, token, "since=0&live=sse")
	defer resp.Body.Close()

	pings := 0
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() == ": ping" {
			pings++
		}
	}
	if pings < 2 {
		t.Fatalf("pings = %d, want at least 2 in one second of silence", pings)
	}
}

func TestLiveDisconnectRemovesWaiter(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	ts := startStreamServer(t, env)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/sync/pull?since=0&live=sse", nil)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
	}()

	waitLiveLen(t, env.srv, 1, 2*time.Second)
	cancel()
	waitLiveLen(t, env.srv, 0, 2*time.Second)
}

func TestLivePollAppliedFalseDoesNotWake(t *testing.T) {
	t.Parallel()

	env := newHTTPEnvWith(t, nil, func(cfg *config.Config) {
		cfg.Sync.LivePollTimeout = config.Duration(300 * time.Millisecond)
	})
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	pushOrFatal(t, env, token, validWireEnvelope())
	seeded := decodePull(t, env.pull(t, token, "since=0"))
	query := fmt.Sprintf("since=%d&live=poll", seeded.NextCursor)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- env.pullCtx(t, ctx, token, query)
	}()
	waitLiveLen(t, env.srv, 1, 2*time.Second)

	started := time.Now()
	pushOrFatal(t, env, token, validWireEnvelope())
	rec := <-done
	elapsed := time.Since(started)
	if elapsed < 150*time.Millisecond {
		t.Fatalf("poll returned in %s after applied:false; want wait until timeout", elapsed)
	}

	got := decodePull(t, rec)
	if len(got.Envelopes) != 0 {
		t.Fatalf("len = %d, want 0 after applied:false", len(got.Envelopes))
	}
}

func TestPullRejectsUnknownLive(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.pull(t, token, "live=nonsense")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "live") {
		t.Fatalf("body %q does not name live", rec.Body.String())
	}
}

// waitLiveLen polls Registry().Len until it equals want or timeout.
func waitLiveLen(t *testing.T, srv *Server, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if srv.Registry().Len() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Registry().Len() = %d, want %d after %s", srv.Registry().Len(), want, timeout)
}

// startStreamServer exposes env.srv over a real HTTP listener so SSE tests
// can read the body while the handler is still running.
func startStreamServer(t *testing.T, env *httpEnv) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(env.srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// openSSE GETs a live=sse pull and fails unless the status is 200.
func openSSE(t *testing.T, ctx context.Context, ts *httptest.Server, token, rawQuery string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/sync/pull?"+rawQuery, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want 200; body = %q", resp.StatusCode, body)
	}
	return resp
}

// waitSSELine reads SSE lines until want appears or timeout elapses.
func waitSSELine(t *testing.T, r io.Reader, want string, timeout time.Duration) {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			ch <- result{line: sc.Text()}
		}
		if err := sc.Err(); err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{err: io.EOF}
	}()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("did not see %q within %s", want, timeout)
		case got := <-ch:
			if got.err != nil {
				t.Fatalf("waiting for %q: %v", want, got.err)
			}
			if got.line == want {
				return
			}
		}
	}
}
