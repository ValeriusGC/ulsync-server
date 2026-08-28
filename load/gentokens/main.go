// Command gentokens writes a static ES256 JWKS file and bearer tokens for k6
// load runs. k6 cannot sign JWTs, and calling a real identity provider for
// one thousand users would measure the IdP, not this server.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// loadKeyID is the kid placed in every generated JWT and in the JWKS file.
	// The verifier requires kid on ES256 tokens.
	loadKeyID = "load-es256-1"

	// tokenLifetime is how long each generated bearer token remains valid.
	tokenLifetime = 24 * time.Hour
)

func main() {
	n := flag.Int("n", 1000, "number of bearer tokens to generate")
	outDir := flag.String("out", "load", "output directory for jwks.json and tokens.json")
	flag.Parse()

	if err := generate(*n, *outDir); err != nil {
		fmt.Fprintf(os.Stderr, "gentokens: %v\n", err)
		os.Exit(1)
	}
}

// generate creates one P-256 key pair, writes JWKS and n JWT strings, and
// leaves the private key only in memory.
func generate(n int, outDir string) error {
	if n < 1 {
		return fmt.Errorf("-n must be at least 1, got %d", n)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate P-256 key: %w", err)
	}

	jwksPath := filepath.Join(outDir, "jwks.json")
	if err := writeJWKS(jwksPath, &priv.PublicKey); err != nil {
		return err
	}

	tokensPath := filepath.Join(outDir, "tokens.json")
	if err := writeTokens(tokensPath, priv, n); err != nil {
		return err
	}

	fmt.Printf("wrote %d tokens to %s and JWKS to %s\n", n, tokensPath, jwksPath)
	return nil
}

// writeJWKS serializes the public key in the same field shape as auth tests.
func writeJWKS(path string, pub *ecdsa.PublicKey) error {
	doc := map[string]any{
		"keys": []map[string]string{ecJWK(loadKeyID, pub)},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JWKS: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write JWKS: %w", err)
	}
	return nil
}

// writeTokens signs n JWTs with distinct sub claims load-user-NNNN.
func writeTokens(path string, priv *ecdsa.PrivateKey, n int) error {
	now := time.Now()
	tokens := make([]string, n)
	for i := range n {
		sub := fmt.Sprintf("load-user-%04d", i)
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.RegisteredClaims{
			Subject:   sub,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(tokenLifetime)),
		})
		tok.Header["kid"] = loadKeyID
		signed, err := tok.SignedString(priv)
		if err != nil {
			return fmt.Errorf("sign token %d: %w", i, err)
		}
		tokens[i] = signed
	}
	raw, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tokens: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write tokens: %w", err)
	}
	return nil
}

// ecJWK serializes a P-256 public key into JWKS JSON field map form.
func ecJWK(kid string, pub *ecdsa.PublicKey) map[string]string {
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
