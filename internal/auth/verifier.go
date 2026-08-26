// Package auth verifies bearer tokens against a JSON Web Key Set.
//
// It never issues tokens and never talks to the identity provider except
// to fetch public keys.
package auth

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

const (
	// expirationLeeway is the maximum clock skew accepted when checking exp.
	// It is a protocol property, not a configuration knob: exposing it in YAML
	// would let an operator silently accept expired tokens.
	expirationLeeway = 60 * time.Second

	// maxJWKSBytes caps a JWKS response. The URL is an untrusted source.
	maxJWKSBytes = 1 << 20

	// jwksFetchTimeout bounds outbound JWKS fetches; the URL is operator-configured
	// but still treated as an untrusted network peer.
	jwksFetchTimeout = 5 * time.Second

	// unknownKIDRefetchInterval caps JWKS reloads triggered by a missing kid.
	// Without this, a stream of invented kids would amplify traffic onto the
	// identity provider. The limit is process-wide, not per kid. A failed
	// fetch still counts: retrying a dead URL every request is the same attack.
	unknownKIDRefetchInterval = time.Minute
)

// errUnauthorized is the only error Verify returns. The reason belongs in the
// log, not in the value the HTTP layer forwards to the client.
var errUnauthorized = errors.New("unauthorized")

// Verifier checks bearer tokens against a set of public keys and extracts the
// subject. It never issues tokens and never talks to the identity provider
// except to fetch public keys.
type Verifier struct {
	cfg    config.Auth
	client *http.Client
	log    *slog.Logger
	parser *jwt.Parser

	mu                  sync.RWMutex
	keys                map[string]crypto.PublicKey // kid -> public key for signature verify
	loadedAt            time.Time                   // when keys was last successfully reloaded
	lastUnknownKIDFetch time.Time                   // rate-limits refetch on invented kids
}

// NewVerifier loads the initial key set and prepares a parser with the
// configured algorithm allowlist. A failed fetch of jwks_url does not fail
// startup: the process comes up with an empty set so /health stays up. A
// missing or unreadable jwks_file is an operator error and is returned.
func NewVerifier(cfg config.Auth, client *http.Client, log *slog.Logger) (*Verifier, error) {
	if log == nil {
		log = slog.Default()
	}
	if len(cfg.AllowedAlgs) == 0 {
		return nil, fmt.Errorf("auth.allowed_algs must not be empty")
	}
	if client == nil {
		client = &http.Client{Timeout: jwksFetchTimeout}
	}

	if cfg.DevHS256Secret != "" {
		log.Warn("auth.dev_hs256_secret is set; HS256 is development-only and lets this process forge tokens for any subject")
	}

	opts := []jwt.ParserOption{
		jwt.WithValidMethods(cfg.AllowedAlgs),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(expirationLeeway),
	}
	if len(cfg.Audience) > 0 {
		opts = append(opts, jwt.WithAudience(cfg.Audience...))
	}
	if iss := strings.TrimSpace(cfg.Issuer); iss != "" {
		opts = append(opts, jwt.WithIssuer(iss))
	}

	v := &Verifier{
		cfg:    cfg,
		client: client,
		log:    log,
		parser: jwt.NewParser(opts...),
		keys:   map[string]crypto.PublicKey{},
	}

	if strings.TrimSpace(cfg.JWKSFile) != "" {
		if err := v.loadFile(); err != nil {
			return nil, err
		}
		return v, nil
	}

	if strings.TrimSpace(cfg.JWKSURL) != "" {
		ctx, cancel := context.WithTimeout(context.Background(), jwksFetchTimeout)
		defer cancel()
		if err := v.fetchURL(ctx); err != nil {
			log.Warn("JWKS fetch failed at startup; /v1 requests will fail until a later refresh succeeds",
				"error", err)
			v.mu.Lock()
			v.loadedAt = time.Now()
			v.mu.Unlock()
		}
	}
	return v, nil
}

// Verify returns the subject of a valid token. Every failure returns the same
// opaque error to the caller: the reason goes to the log, not to the client.
func (v *Verifier) Verify(ctx context.Context, bearer string) (userID string, err error) {
	if bearer == "" {
		v.reject("", "missing token")
		return "", errUnauthorized
	}

	v.refreshIfStale(ctx)

	// Refetch before signature verify when the header names an unknown kid, so a
	// newly published signing key can appear without waiting for cache TTL.
	if kid := peekKID(v.parser, bearer); kid != "" && !v.hasKey(kid) {
		v.refetchUnknownKID(ctx)
	}

	claims := &jwt.RegisteredClaims{}
	token, err := v.parser.ParseWithClaims(bearer, claims, v.keyFunc)
	if err != nil {
		v.reject(claims.Subject, err.Error())
		return "", errUnauthorized
	}
	if !token.Valid {
		v.reject(claims.Subject, "token not valid")
		return "", errUnauthorized
	}
	if claims.Subject == "" {
		v.reject("", "empty or missing sub")
		return "", errUnauthorized
	}
	return claims.Subject, nil
}

// reject logs why a token failed. The HTTP layer must not forward reason.
func (v *Verifier) reject(sub, reason string) {
	attrs := []any{"reason", reason}
	if sub != "" {
		attrs = append(attrs, "sub", sub)
	}
	v.log.Info("token rejected", attrs...)
}

// keyFunc supplies the public key (or dev HS256 secret) for jwt.ParseWithClaims.
func (v *Verifier) keyFunc(token *jwt.Token) (any, error) {
	alg, _ := token.Header["alg"].(string)
	// HS256 must never be verified with a key from the JWKS: that is the
	// algorithm-confusion attack (RSA public key used as HMAC secret).
	if alg == jwt.SigningMethodHS256.Alg() {
		if v.cfg.DevHS256Secret == "" {
			return nil, errUnauthorized
		}
		return []byte(v.cfg.DevHS256Secret), nil
	}

	kid, _ := token.Header["kid"].(string)
	if kid == "" {
		return nil, errUnauthorized
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	key, ok := v.keys[kid]
	if !ok {
		return nil, errUnauthorized
	}
	return key, nil
}

// hasKey reports whether kid is present in the in-memory key set.
func (v *Verifier) hasKey(kid string) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	_, ok := v.keys[kid]
	return ok
}

// peekKID reads kid from an unverified header so Verify can refetch before
// the signature check. The returned token is not trusted.
func peekKID(parser *jwt.Parser, bearer string) string {
	tok, _, err := parser.ParseUnverified(bearer, &jwt.RegisteredClaims{})
	if err != nil {
		return ""
	}
	kid, _ := tok.Header["kid"].(string)
	return kid
}

// refetchUnknownKID reloads JWKS when the token names a kid we do not have,
// at most once per unknownKIDRefetchInterval process-wide.
func (v *Verifier) refetchUnknownKID(ctx context.Context) {
	v.mu.Lock()
	if !v.lastUnknownKIDFetch.IsZero() && time.Since(v.lastUnknownKIDFetch) < unknownKIDRefetchInterval {
		v.mu.Unlock()
		return
	}
	v.lastUnknownKIDFetch = time.Now()
	v.mu.Unlock()

	if err := v.reload(ctx); err != nil {
		v.log.Warn("JWKS refetch on unknown kid failed; keeping last key set", "error", err)
	}
}

// refreshIfStale reloads JWKS when jwks_cache_ttl has elapsed. On failure the
// last good key set is kept and loadedAt is bumped so a dead URL is not hit
// on every request.
func (v *Verifier) refreshIfStale(ctx context.Context) {
	ttl := v.cfg.JWKSCacheTTL.Std()
	if ttl <= 0 {
		return
	}
	v.mu.RLock()
	stale := !v.loadedAt.IsZero() && time.Since(v.loadedAt) >= ttl
	v.mu.RUnlock()
	if !stale {
		return
	}
	if err := v.reload(ctx); err != nil {
		v.log.Warn("JWKS refresh failed; keeping last key set", "error", err)
		// Shift loadedAt so a dead URL is not fetched on every subsequent request.
		v.mu.Lock()
		v.loadedAt = time.Now()
		v.mu.Unlock()
	}
}

// reload fetches keys from jwks_file or jwks_url depending on configuration.
func (v *Verifier) reload(ctx context.Context) error {
	if strings.TrimSpace(v.cfg.JWKSFile) != "" {
		return v.loadFile()
	}
	if strings.TrimSpace(v.cfg.JWKSURL) != "" {
		return v.fetchURL(ctx)
	}
	return nil
}

// loadFile reads and parses auth.jwks_file into the in-memory key set.
func (v *Verifier) loadFile() error {
	path := v.cfg.JWKSFile
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read JWKS file: %w", err)
	}
	keys, err := parseJWKS(data, v.log)
	if err != nil {
		return fmt.Errorf("parse JWKS file: %w", err)
	}
	v.mu.Lock()
	v.keys = keys
	v.loadedAt = time.Now()
	v.mu.Unlock()
	return nil
}

// fetchURL downloads auth.jwks_url with a size cap and replaces the key set.
func (v *Verifier) fetchURL(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return fmt.Errorf("JWKS request: %w", err)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("JWKS fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS fetch: unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return fmt.Errorf("JWKS fetch: read body: %w", err)
	}
	if len(data) > maxJWKSBytes {
		return fmt.Errorf("JWKS fetch: response exceeds %d bytes", maxJWKSBytes)
	}
	keys, err := parseJWKS(data, v.log)
	if err != nil {
		return fmt.Errorf("parse JWKS: %w", err)
	}
	v.mu.Lock()
	v.keys = keys
	v.loadedAt = time.Now()
	v.mu.Unlock()
	return nil
}
