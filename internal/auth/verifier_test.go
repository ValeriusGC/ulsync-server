package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

func TestVerifyES256(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t, nil)
	token := signToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, validClaims("alice"))

	got, err := env.verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got != "alice" {
		t.Fatalf("sub = %q, want alice", got)
	}
	t.Logf("sub=%s", got)
}

func TestVerifyRS256(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t, nil)
	token := signToken(t, jwt.SigningMethodRS256, env.rsaPriv, env.rsaKid, validClaims("bob"))

	got, err := env.verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got != "bob" {
		t.Fatalf("sub = %q, want bob", got)
	}
}

func TestVerifyWrongKey(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t, nil)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	token := signToken(t, jwt.SigningMethodES256, other, env.ecKid, validClaims("alice"))

	if _, err := env.verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify() succeeded for a token signed by a different key")
	}
}

func TestVerifyAlgorithmConfusion(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t, nil)
	der, err := x509.MarshalPKIXPublicKey(&env.rsaPriv.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	token := signToken(t, jwt.SigningMethodHS256, der, env.rsaKid, validClaims("attacker"))

	if _, err := env.verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify() succeeded for HS256 signed with the RSA public key")
	}
}

func TestVerifyAlgNone(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t, nil)
	token := noneToken(t, env.ecKid, validClaims("alice"))

	if _, err := env.verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify() succeeded for alg=none")
	}
}

func TestVerifyExpired(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t, nil)
	claims := validClaims("alice")
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-60 * time.Second))
	token := signToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, claims)

	if _, err := env.verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify() succeeded for a token expired by 60s")
	}
}

func TestVerifyLeeway(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t, nil)
	claims := validClaims("alice")
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-10 * time.Second))
	token := signToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, claims)

	got, err := env.verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() error = %v, want success within 60s leeway", err)
	}
	if got != "alice" {
		t.Fatalf("sub = %q, want alice", got)
	}
}

func TestVerifyEmptySub(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t, nil)
	claims := validClaims("")
	token := signToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, claims)

	if _, err := env.verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify() succeeded for empty sub")
	}
}

func TestVerifyMissingSub(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t, nil)
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = env.ecKid
	token, err := tok.SignedString(env.ecPriv)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}

	if _, err := env.verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify() succeeded for missing sub")
	}
}

func TestVerifyAudienceIssuer(t *testing.T) {
	t.Parallel()

	const (
		wantAud = "ulsync"
		wantIss = "https://idp.example"
	)
	env := newVerifyEnv(t, func(cfg *config.Auth) {
		cfg.Audience = []string{wantAud}
		cfg.Issuer = wantIss
	})

	mismatch := validClaims("alice")
	mismatch.Audience = jwt.ClaimStrings{"other"}
	mismatch.Issuer = "https://other.example"
	if _, err := env.verifier.Verify(context.Background(), signToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, mismatch)); err == nil {
		t.Fatal("Verify() succeeded for mismatched aud/iss")
	}

	match := validClaims("alice")
	match.Audience = jwt.ClaimStrings{wantAud}
	match.Issuer = wantIss
	got, err := env.verifier.Verify(context.Background(), signToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, match))
	if err != nil {
		t.Fatalf("Verify() error = %v, want success for matching aud/iss", err)
	}
	if got != "alice" {
		t.Fatalf("sub = %q, want alice", got)
	}
}

func TestVerifyUnknownKIDRateLimit(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t, nil)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	token := signToken(t, jwt.SigningMethodES256, other, "unknown-kid", validClaims("alice"))

	afterStart := env.hits.Load()
	if _, err := env.verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify() succeeded for unknown kid")
	}
	afterFirst := env.hits.Load()
	if afterFirst != afterStart+1 {
		t.Fatalf("unknown kid JWKS fetches = %d, want 1 refetch (hits %d -> %d)", afterFirst-afterStart, afterStart, afterFirst)
	}

	if _, err := env.verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("Verify() succeeded for unknown kid on retry")
	}
	afterSecond := env.hits.Load()
	if afterSecond != afterFirst {
		t.Fatalf("second unknown kid hit JWKS (hits %d -> %d); refetch must be at most once a minute", afterFirst, afterSecond)
	}
}

func TestVerifyJWKSOutageUsesCache(t *testing.T) {
	t.Parallel()

	env := newVerifyEnv(t, nil)
	token := signToken(t, jwt.SigningMethodES256, env.ecPriv, env.ecKid, validClaims("alice"))
	got, err := env.verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() error = %v before outage", err)
	}
	if got != "alice" {
		t.Fatalf("sub = %q, want alice", got)
	}

	env.server.Close()

	got, err = env.verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() error = %v after JWKS outage; cached keys should still verify", err)
	}
	if got != "alice" {
		t.Fatalf("sub = %q, want alice", got)
	}
}

type verifyEnv struct {
	verifier *Verifier
	server   *httptest.Server
	hits     *atomic.Int32
	ecPriv   *ecdsa.PrivateKey
	rsaPriv  *rsa.PrivateKey
	ecKid    string
	rsaKid   string
}

func newVerifyEnv(t *testing.T, tweak func(*config.Auth)) *verifyEnv {
	t.Helper()

	ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() error = %v", err)
	}
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}

	env := &verifyEnv{
		ecPriv:  ecPriv,
		rsaPriv: rsaPriv,
		ecKid:   "ec-1",
		rsaKid:  "rsa-1",
		hits:    &atomic.Int32{},
	}
	raw, err := json.Marshal(map[string]any{
		"keys": []map[string]string{
			ecJWK(env.ecKid, &ecPriv.PublicKey),
			rsaJWK(env.rsaKid, &rsaPriv.PublicKey),
		},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	env.server = srv

	cfg := config.Auth{
		JWKSURL:      srv.URL,
		JWKSCacheTTL: config.Duration(10 * time.Minute),
		AllowedAlgs:  []string{"ES256", "RS256"},
	}
	if tweak != nil {
		tweak(&cfg)
	}

	v, err := NewVerifier(cfg, srv.Client(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	env.verifier = v
	return env
}

func validClaims(sub string) jwt.RegisteredClaims {
	now := time.Now()
	return jwt.RegisteredClaims{
		Subject:   sub,
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(now),
	}
}

func signToken(t *testing.T, method jwt.SigningMethod, key any, kid string, claims jwt.RegisteredClaims) string {
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

func noneToken(t *testing.T, kid string, claims jwt.RegisteredClaims) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{
		"alg": "none",
		"typ": "JWT",
		"kid": kid,
	})
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
