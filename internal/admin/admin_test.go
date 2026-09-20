package admin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/auth"
	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/httpapi"
	"github.com/ValeriusGC/ulsync-server/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

type adminEnv struct {
	syncSrv  *httpapi.Server
	adminSrv *Server
	db       *store.Store
	cfg      *config.Config
	ecPriv   *ecdsa.PrivateKey
	ecKid    string
	jwtToken string
}

func newAdminEnv(t *testing.T, tweak func(*config.Config)) *adminEnv {
	t.Helper()
	return newAdminEnvOpts(t, tweak, Options{
		SnapshotEvery: 100 * time.Millisecond,
		StatsEvery:    50 * time.Millisecond,
	})
}

func newAdminEnvOpts(t *testing.T, tweak func(*config.Config), opts Options) *adminEnv {
	t.Helper()

	ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}
	ecKid := "ec-1"
	rsaKid := "rsa-1"

	jwksHandler := func(w http.ResponseWriter, r *http.Request) {
		raw, _ := json.Marshal(map[string]any{
			"keys": []map[string]string{
				adminECJWK(ecKid, &ecPriv.PublicKey),
				adminRSAJWK(rsaKid, &rsaPriv.PublicKey),
			},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}
	jwks := httptest.NewServer(http.HandlerFunc(jwksHandler))
	t.Cleanup(jwks.Close)

	authCfg := config.Auth{
		JWKSURL:      jwks.URL,
		JWKSCacheTTL: config.Duration(10 * time.Minute),
		AllowedAlgs:  []string{"ES256", "RS256"},
	}
	verifier, err := auth.NewVerifier(authCfg, jwks.Client(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}

	dir := t.TempDir()
	cfg := &config.Config{
		Server: config.Server{
			Bind:              "127.0.0.1:0",
			ReadHeaderTimeout: config.Duration(5 * time.Second),
			IdleTimeout:       config.Duration(120 * time.Second),
			MaxBodyBytes:      1 << 20,
		},
		Storage: config.Storage{Driver: "sqlite", Path: filepath.Join(dir, "ulsync.db")},
		Auth:    authCfg,
		Sync: config.Sync{
			MaxEnvelopesPerPush: 1,
			PullLimitDefault:    100,
			PullLimitMax:        500,
			LivePollTimeout:     config.Duration(55 * time.Second),
			LiveHeartbeat:       config.Duration(15 * time.Second),
		},
		Admin: config.Admin{Bind: "127.0.0.1:0", Token: ""},
	}
	if tweak != nil {
		tweak(cfg)
	}

	db, err := store.Open(context.Background(), cfg.Storage)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	startedAt := time.Now().UTC().Add(-time.Minute)
	syncSrv := httpapi.New(cfg, db, verifier, "test-version", startedAt)
	adminSrv := New(cfg, db, verifier, "test-version", startedAt, syncSrv.Metrics(), syncSrv.Registry(), opts)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = adminSrv.Shutdown(ctx)
	})

	token := signAdminJWT(t, ecPriv, ecKid, "alice")
	return &adminEnv{
		syncSrv:  syncSrv,
		adminSrv: adminSrv,
		db:       db,
		cfg:      cfg,
		ecPriv:   ecPriv,
		ecKid:    ecKid,
		jwtToken: token,
	}
}

func (e *adminEnv) adminTS(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(e.adminSrv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func (e *adminEnv) syncTS(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(e.syncSrv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestAdminRequiresTokenOnAllThreeRoutes(t *testing.T) {
	t.Parallel()

	env := newAdminEnv(t, func(cfg *config.Config) {
		cfg.Admin.Token = "panel-secret"
	})
	ts := env.adminTS(t)
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/admin"},
		{http.MethodGet, "/admin/events"},
		{http.MethodPost, "/admin/token-check"},
	}
	for _, route := range routes {
		var body io.Reader
		if route.method == http.MethodPost {
			body = strings.NewReader(`{"token":"x"}`)
		}
		req, err := http.NewRequest(route.method, ts.URL+route.path, body)
		if err != nil {
			t.Fatalf("NewRequest() error = %v", err)
		}
		if route.method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s Do() error = %v", route.method, route.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", route.method, route.path, resp.StatusCode)
		}
	}
}

func TestAdminRejectsWrongToken(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatalf("ReadFile(admin.go) error = %v", err)
	}
	text := string(src)
	if !strings.Contains(text, "subtle.ConstantTimeCompare") {
		t.Fatal("admin.go must compare panel tokens with subtle.ConstantTimeCompare")
	}
	if strings.Contains(text, "cfg.Admin.Token ==") || strings.Contains(text, "got == want") {
		t.Fatal("admin.go must not compare panel tokens with ==")
	}

	env := newAdminEnv(t, func(cfg *config.Config) {
		cfg.Admin.Token = "panel-secret"
	})
	ts := env.adminTS(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAdminLoopbackEmptyTokenAllowsAnonymous(t *testing.T) {
	t.Parallel()

	env := newAdminEnv(t, nil)
	ts := env.adminTS(t)
	resp, err := http.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestAdminPageServesHTML(t *testing.T) {
	t.Parallel()

	env := newAdminEnv(t, nil)
	ts := env.adminTS(t)
	resp, err := http.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "<!DOCTYPE html") {
		t.Fatalf("body prefix = %q, want DOCTYPE html", string(body[:min(40, len(body))]))
	}
}

func TestAdminEventsStreamsAtLeastTwoSnapshots(t *testing.T) {
	t.Parallel()

	env := newAdminEnv(t, nil)
	ts := env.adminTS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/admin/events", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	frames := readDataFrames(t, resp.Body, 2, 2*time.Second)
	if len(frames) < 2 {
		t.Fatalf("got %d data frames, want at least 2", len(frames))
	}
}

func TestAdminSnapshotRedactsSecretsAndOmitsPayload(t *testing.T) {
	t.Parallel()

	const (
		devSecret   = "SUPERSECRET"
		panelSecret = "PANELSECRET"
	)
	env := newAdminEnv(t, func(cfg *config.Config) {
		cfg.Auth.DevHS256Secret = devSecret
		cfg.Admin.Token = panelSecret
	})
	snap := env.adminSrv.assembleSnapshot(context.Background())
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	text := string(raw)
	for _, key := range []string{"version", "storage", "config", "live_connections", "requests"} {
		if !strings.Contains(text, `"`+key+`"`) {
			t.Fatalf("snapshot missing key %q: %s", key, text)
		}
	}
	if strings.Contains(text, devSecret) || strings.Contains(text, panelSecret) {
		t.Fatalf("snapshot leaked secret: %s", text)
	}
	if strings.Contains(text, `"payload"`) {
		t.Fatalf("snapshot must not contain envelope payload key: %s", text)
	}
	if !strings.Contains(text, "***") {
		t.Fatalf("snapshot missing redaction placeholder: %s", text)
	}
}

func TestAdminTokenCheck(t *testing.T) {
	t.Parallel()

	env := newAdminEnv(t, nil)
	ts := env.adminTS(t)

	validBody, _ := json.Marshal(map[string]string{"token": env.jwtToken})
	resp, err := http.Post(ts.URL+"/admin/token-check", "application/json", bytes.NewReader(validBody))
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid status = %d, want 200", resp.StatusCode)
	}
	var okBody struct {
		Valid   bool   `json:"valid"`
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&okBody); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !okBody.Valid || okBody.Subject != "alice" {
		t.Fatalf("valid body = %+v", okBody)
	}

	badBody := []byte(`{"token":"nope"}`)
	resp2, err := http.Post(ts.URL+"/admin/token-check", "application/json", bytes.NewReader(badBody))
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("invalid status = %d, want 200", resp2.StatusCode)
	}
	var badResp struct {
		Valid  bool   `json:"valid"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&badResp); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if badResp.Valid || badResp.Reason == "" {
		t.Fatalf("invalid body = %+v", badResp)
	}
}

func TestMetricsCountV1NotAdmin(t *testing.T) {
	t.Parallel()

	env := newAdminEnv(t, nil)
	syncTS := env.syncTS(t)
	adminTS := env.adminTS(t)

	before := env.syncSrv.Metrics().Total()
	whoamiReq, _ := http.NewRequest(http.MethodGet, syncTS.URL+"/v1/whoami", nil)
	whoamiReq.Header.Set("Authorization", "Bearer "+env.jwtToken)
	whoamiResp, err := http.DefaultClient.Do(whoamiReq)
	if err != nil {
		t.Fatalf("whoami Do() error = %v", err)
	}
	_ = whoamiResp.Body.Close()
	afterWhoami := env.syncSrv.Metrics().Total()
	if afterWhoami != before+1 {
		t.Fatalf("total after whoami = %d, want %d", afterWhoami, before+1)
	}

	adminResp, err := http.Get(adminTS.URL + "/admin")
	if err != nil {
		t.Fatalf("Get /admin error = %v", err)
	}
	_ = adminResp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	eventsReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, adminTS.URL+"/admin/events", nil)
	eventsResp, err := http.DefaultClient.Do(eventsReq)
	if err != nil && ctx.Err() == nil {
		t.Fatalf("Get /admin/events error = %v", err)
	}
	if eventsResp != nil {
		_ = eventsResp.Body.Close()
	}

	if got := env.syncSrv.Metrics().Total(); got != afterWhoami {
		t.Fatalf("total after admin = %d, want %d (panel must not count)", got, afterWhoami)
	}
}

func TestSnapshotLiveConnectionsMatchesRegistry(t *testing.T) {
	t.Parallel()

	env := newAdminEnv(t, nil)
	syncTS := env.syncTS(t)
	adminTS := env.adminTS(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sseReq, err := http.NewRequestWithContext(ctx, http.MethodGet, syncTS.URL+"/v1/sync/pull?since=0&live=sse", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	sseReq.Header.Set("Authorization", "Bearer "+env.jwtToken)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer sseResp.Body.Close()

	waitUntil(t, 2*time.Second, func() bool {
		return env.syncSrv.Registry().Len() == 1
	})
	if env.syncSrv.Registry().Len() != 1 {
		t.Fatalf("Registry().Len() = %d, want 1", env.syncSrv.Registry().Len())
	}

	snap := waitSnapshotLiveConnections(t, adminTS.URL+"/admin/events", 1, 2*time.Second)
	if snap.LiveConnections != 1 {
		t.Fatalf("live_connections = %d, want 1", snap.LiveConnections)
	}

	cancel()
	waitUntil(t, 2*time.Second, func() bool {
		return env.syncSrv.Registry().Len() == 0
	})

	snap0 := waitSnapshotLiveConnections(t, adminTS.URL+"/admin/events", 0, 2*time.Second)
	if snap0.LiveConnections != 0 {
		t.Fatalf("live_connections after cancel = %d, want 0", snap0.LiveConnections)
	}
}

func TestStatsCachedAcrossImmediateSnapshots(t *testing.T) {
	t.Parallel()

	// Production ticker is 1s / 5s. The shared test env ticks every 100ms and
	// refreshes Stats every 50ms; under -race that tick lands between Upsert
	// and the "immediate" snapshot, so the cache looks broken. This test owns
	// the intervals: ticker must not fire, TTL is long enough to cover Upsert.
	env := newAdminEnvOpts(t, nil, Options{
		SnapshotEvery: time.Hour,
		StatsEvery:    200 * time.Millisecond,
	})
	ctx := context.Background()
	first := env.adminSrv.assembleSnapshot(ctx)
	before := first.Storage.Envelopes

	_, err := env.db.Upsert(ctx, "alice", store.Envelope{
		ID:              "stats-cache-test",
		Part:            "full",
		EntityType:      "counter_operation",
		SourceID:        "device-a",
		PayloadEncoding: "json",
		CreatedAtMS:     1000,
		LastEditedAtMS:  1000,
		Revision:        1,
		Payload:         []byte(`{"type":"increment"}`),
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	second := env.adminSrv.assembleSnapshot(ctx)
	if second.Storage.Envelopes != before {
		t.Fatalf("immediate second snapshot envelopes = %d, want cached %d", second.Storage.Envelopes, before)
	}

	time.Sleep(250 * time.Millisecond)
	third := env.adminSrv.assembleSnapshot(ctx)
	if third.Storage.Envelopes <= before {
		t.Fatalf("envelopes after cache TTL = %d, want > %d", third.Storage.Envelopes, before)
	}
}

func TestAdminRoutesAbsentOnSyncPort(t *testing.T) {
	t.Parallel()

	env := newAdminEnv(t, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	env.syncSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("sync /admin status = %d, want 404", rec.Code)
	}
}

func TestSyncRoutesAbsentOnAdminPort(t *testing.T) {
	t.Parallel()

	env := newAdminEnv(t, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/whoami", nil)
	env.adminSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("admin /v1/whoami status = %d, want 404", rec.Code)
	}
}

type snapshotWire struct {
	LiveConnections int `json:"live_connections"`
	Storage         struct {
		Envelopes int64 `json:"envelopes"`
	} `json:"storage"`
}

func waitSnapshotLiveConnections(t *testing.T, eventsURL string, want int, timeout time.Duration) snapshotWire {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, eventsURL, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()

	deadline := time.Now().Add(timeout)
	sc := bufio.NewScanner(resp.Body)
	for time.Now().Before(deadline) {
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				t.Fatalf("scan error = %v", err)
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var snap snapshotWire
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &snap); err != nil {
			continue
		}
		if snap.LiveConnections == want {
			return snap
		}
	}
	t.Fatalf("did not see live_connections=%d within %s", want, timeout)
	return snapshotWire{}
}

func readDataFrames(t *testing.T, r io.Reader, want int, timeout time.Duration) []string {
	t.Helper()
	deadline := time.After(timeout)
	sc := bufio.NewScanner(r)
	var frames []string
	for len(frames) < want {
		select {
		case <-deadline:
			return frames
		default:
		}
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				t.Fatalf("scan error = %v", err)
			}
			time.Sleep(5 * time.Millisecond)
			continue
		}
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			frames = append(frames, strings.TrimPrefix(line, "data: "))
		}
	}
	return frames
}

func waitUntil(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func signAdminJWT(t *testing.T, key *ecdsa.PrivateKey, kid, sub string) string {
	t.Helper()
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   sub,
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(now),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	return signed
}

func adminRSAJWK(kid string, pub *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA", "kid": kid, "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func adminECJWK(kid string, pub *ecdsa.PublicKey) map[string]string {
	xb := make([]byte, 32)
	yb := make([]byte, 32)
	pub.X.FillBytes(xb)
	pub.Y.FillBytes(yb)
	return map[string]string{
		"kty": "EC", "kid": kid, "use": "sig", "crv": "P-256",
		"x": base64.RawURLEncoding.EncodeToString(xb),
		"y": base64.RawURLEncoding.EncodeToString(yb),
	}
}
