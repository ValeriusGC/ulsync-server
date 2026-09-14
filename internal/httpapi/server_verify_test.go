package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/auth"
	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

func TestVerifyES256(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	rec := env.whoami(t, signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice")))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var body struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if body.UserID != "alice" {
		t.Fatalf("user_id = %q, want alice", body.UserID)
	}
}

func TestVerifyRS256(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	rec := env.whoami(t, signHTTPToken(t, jwt.SigningMethodRS256, env.rsaPriv, env.rsaKid, httpClaims("bob")))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestVerifyWrongKey(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	rec := env.whoami(t, signHTTPToken(t, jwt.SigningMethodES256, other, env.ecKid, httpClaims("alice")))
	assertUnauthorized(t, rec)
}

func TestVerifyAlgorithmConfusion(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	der, err := x509.MarshalPKIXPublicKey(&env.rsaPriv.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	rec := env.whoami(t, signHTTPToken(t, jwt.SigningMethodHS256, der, env.rsaKid, httpClaims("attacker")))
	assertUnauthorized(t, rec)
}

func TestVerifyAlgNone(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	rec := env.whoami(t, noneHTTPToken(t, env.ecKid, httpClaims("alice")))
	assertUnauthorized(t, rec)
}

func TestVerifyExpired(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	claims := httpClaims("alice")
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-60 * time.Second))
	rec := env.whoami(t, signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, claims))
	assertUnauthorized(t, rec)
}

func TestVerifyLeeway(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	claims := httpClaims("alice")
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-10 * time.Second))
	rec := env.whoami(t, signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, claims))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 within 60s leeway; body = %q", rec.Code, rec.Body.String())
	}
}

func TestVerifyEmptySub(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	rec := env.whoami(t, signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("")))
	assertUnauthorized(t, rec)
}

func TestVerifyMissingSub(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = env.ecKid
	token, err := tok.SignedString(env.ecPriv)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	assertUnauthorized(t, env.whoami(t, token))
}

func TestVerifyUnknownKIDRateLimit(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	token := signHTTPToken(t, jwt.SigningMethodES256, other, "unknown-kid", httpClaims("alice"))

	afterStart := env.hits.Load()
	assertUnauthorized(t, env.whoami(t, token))
	afterFirst := env.hits.Load()
	if afterFirst != afterStart+1 {
		t.Fatalf("unknown kid JWKS fetches = %d, want 1 (hits %d -> %d)", afterFirst-afterStart, afterStart, afterFirst)
	}

	assertUnauthorized(t, env.whoami(t, token))
	afterSecond := env.hits.Load()
	if afterSecond != afterFirst {
		t.Fatalf("second unknown kid hit JWKS (hits %d -> %d)", afterFirst, afterSecond)
	}
}

func TestVerifyAudienceIssuer(t *testing.T) {
	t.Parallel()

	const (
		wantAud = "ulsync"
		wantIss = "https://idp.example"
	)
	env := newHTTPEnv(t, func(cfg *config.Auth) {
		cfg.Audience = []string{wantAud}
		cfg.Issuer = wantIss
	})

	mismatch := httpClaims("alice")
	mismatch.Audience = jwt.ClaimStrings{"other"}
	mismatch.Issuer = "https://other.example"
	assertUnauthorized(t, env.whoami(t, signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, mismatch)))

	match := httpClaims("alice")
	match.Audience = jwt.ClaimStrings{wantAud}
	match.Issuer = wantIss
	rec := env.whoami(t, signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, match))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for matching aud/iss; body = %q", rec.Code, rec.Body.String())
	}
}

func TestVerifyJWKSOutageUsesCache(t *testing.T) {
	t.Parallel()

	env := newHTTPEnv(t, nil)
	token := signHTTPToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, httpClaims("alice"))
	rec := env.whoami(t, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d before outage, body = %q", rec.Code, rec.Body.String())
	}

	env.jwks.Close()

	rec = env.whoami(t, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d after JWKS outage, want 200; body = %q", rec.Code, rec.Body.String())
	}
}

type httpEnv struct {
	srv *Server
	// db is kept so push tests can assert stored rows without duplicating SQL.
	db *store.Store
	// jwks serves a synthetic JWKS document; hits counts fetch attempts.
	jwks    *httptest.Server
	hits    *atomic.Int32
	ecPriv  *ecdsa.PrivateKey // signs ES256 test tokens
	rsaPriv *rsa.PrivateKey   // signs RS256 test tokens
	ecKid   string
	rsaKid  string
}

// newHTTPEnv builds a full HTTP stack with ephemeral SQLite and a mock JWKS URL.
func newHTTPEnv(t *testing.T, tweak func(*config.Auth)) *httpEnv {
	return newHTTPEnvWith(t, tweak, nil)
}

// newHTTPEnvWith builds the same stack as newHTTPEnv, then lets the test
// override config fields (pull limits in step 05).
func newHTTPEnvWith(t *testing.T, tweakAuth func(*config.Auth), tweakCfg func(*config.Config)) *httpEnv {
	t.Helper()

	ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() error = %v", err)
	}
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}

	env := &httpEnv{
		ecPriv:  ecPriv,
		rsaPriv: rsaPriv,
		ecKid:   "ec-1",
		rsaKid:  "rsa-1",
		hits:    &atomic.Int32{},
	}
	raw, err := json.Marshal(map[string]any{
		"keys": []map[string]string{
			httpECJWK(env.ecKid, &ecPriv.PublicKey),
			httpRSAJWK(env.rsaKid, &rsaPriv.PublicKey),
		},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(jwks.Close)
	env.jwks = jwks

	authCfg := config.Auth{
		JWKSURL:      jwks.URL,
		JWKSCacheTTL: config.Duration(10 * time.Minute),
		AllowedAlgs:  []string{"ES256", "RS256"},
	}
	if tweakAuth != nil {
		tweakAuth(&authCfg)
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
			MaxBodyBytes:      1024,
		},
		Storage: config.Storage{
			Driver: "sqlite",
			Path:   filepath.Join(dir, "ulsync.db"),
		},
		Auth: authCfg,
		Sync: config.Sync{
			MaxEnvelopesPerPush: 1,
			PullLimitDefault:    100,
			PullLimitMax:        500,
			// newHTTPEnvWith builds Config by hand, bypassing config.Load.
			// Zero durations would make live=poll return immediately
			// (time.NewTimer(0)) and live=sse panic (time.NewTicker(0)).
			LivePollTimeout: config.Duration(55 * time.Second),
			LiveHeartbeat:   config.Duration(15 * time.Second),
		},
	}
	if tweakCfg != nil {
		tweakCfg(cfg)
	}
	db, err := store.Open(context.Background(), cfg.Storage)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	env.srv = New(cfg, db, verifier, "test-version", time.Now().UTC())
	env.db = db
	return env
}

// pull GETs /v1/sync/pull with rawQuery (no leading ?) and an optional bearer.
// An empty token omits Authorization so the 401 path can be exercised.
func (e *httpEnv) pull(t *testing.T, token, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	return e.pullCtx(t, context.Background(), token, rawQuery)
}

// pullCtx is pull with a caller-supplied context so live tests can cancel a
// waiting request and ordinary pull can run under a short deadline.
func (e *httpEnv) pullCtx(t *testing.T, ctx context.Context, token, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/sync/pull?"+rawQuery, nil).WithContext(ctx)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// diff posts one divergence-check request with the given bearer token and JSON
// body through the full route table (auth middleware, body limit, diff handler).
func (e *httpEnv) diff(t *testing.T, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/sync/diff", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// push posts one push request with the given bearer token and JSON body through
// the full route table (auth middleware, body limit, push handler).
func (e *httpEnv) push(t *testing.T, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/sync/push", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// whoami hits GET /v1/whoami with the given bearer token.
func (e *httpEnv) whoami(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// assertUnauthorized checks the fixed 401 body and WWW-Authenticate header.
func assertUnauthorized(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
	}
	if body := rec.Body.String(); body != unauthorizedJSON {
		t.Fatalf("body = %q, want %q", body, unauthorizedJSON)
	}
}

// httpClaims returns registered claims with a one-hour lifetime for test tokens.
func httpClaims(sub string) jwt.RegisteredClaims {
	now := time.Now()
	return jwt.RegisteredClaims{
		Subject:   sub,
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(now),
	}
}

// signHTTPToken builds a signed JWT for httptest, optionally setting kid.
func signHTTPToken(t *testing.T, method jwt.SigningMethod, key any, kid string, claims jwt.RegisteredClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	return signed
}

// noneHTTPToken builds an unsigned JWT with alg=none for rejection tests.
func noneHTTPToken(t *testing.T, kid string, claims jwt.RegisteredClaims) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "none", "typ": "JWT", "kid": kid})
	if err != nil {
		t.Fatalf("Marshal header: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("Marshal claims: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "."
}

// httpRSAJWK serializes an RSA public key into JWKS JSON field map form.
func httpRSAJWK(kid string, pub *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA",
		"kid": kid,
		"use": "sig",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

// httpECJWK serializes a P-256 public key into JWKS JSON field map form.
func httpECJWK(kid string, pub *ecdsa.PublicKey) map[string]string {
	xb := make([]byte, 32)
	yb := make([]byte, 32)
	pub.X.FillBytes(xb)
	pub.Y.FillBytes(yb)
	return map[string]string{
		"kty": "EC",
		"kid": kid,
		"use": "sig",
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(xb),
		"y":   base64.RawURLEncoding.EncodeToString(yb),
	}
}
