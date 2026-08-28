package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ValeriusGC/ulsync-server/internal/auth"
	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

func TestGenerateTokensVerifyWithServerVerifier(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const n = 3
	if err := generate(n, dir); err != nil {
		t.Fatalf("generate() error = %v", err)
	}

	jwksPath := filepath.Join(dir, "jwks.json")
	tokensPath := filepath.Join(dir, "tokens.json")

	var jwks struct {
		Keys []struct {
			KID string `json:"kid"`
		} `json:"keys"`
	}
	raw, err := os.ReadFile(jwksPath)
	if err != nil {
		t.Fatalf("ReadFile(jwks) error = %v", err)
	}
	if err := json.Unmarshal(raw, &jwks); err != nil {
		t.Fatalf("Unmarshal(jwks) error = %v", err)
	}
	if len(jwks.Keys) != 1 || jwks.Keys[0].KID == "" {
		t.Fatalf("JWKS kid missing or empty: %+v", jwks.Keys)
	}

	v, err := auth.NewVerifier(config.Auth{
		JWKSFile:    jwksPath,
		AllowedAlgs: []string{"ES256"},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}

	var tokens []string
	raw, err = os.ReadFile(tokensPath)
	if err != nil {
		t.Fatalf("ReadFile(tokens) error = %v", err)
	}
	if err := json.Unmarshal(raw, &tokens); err != nil {
		t.Fatalf("Unmarshal(tokens) error = %v", err)
	}
	if len(tokens) != n {
		t.Fatalf("len(tokens) = %d, want %d", len(tokens), n)
	}

	ctx := context.Background()
	for i, bearer := range tokens {
		wantSub := fmt.Sprintf("load-user-%04d", i)
		got, err := v.Verify(ctx, bearer)
		if err != nil {
			t.Fatalf("Verify(token[%d]) error = %v", i, err)
		}
		if got != wantSub {
			t.Fatalf("Verify(token[%d]) = %q, want %q", i, got, wantSub)
		}
	}

	// Truncated signature must fail.
	bad := tokens[0][:len(tokens[0])-8]
	if _, err := v.Verify(ctx, bad); err == nil {
		t.Fatal("Verify(truncated) error = nil, want unauthorized")
	}

	// HS256 token must fail when only ES256 is allowed.
	hsTok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "load-user-0000",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	hsStr, err := hsTok.SignedString([]byte("wrong-secret"))
	if err != nil {
		t.Fatalf("SignedString(HS256) error = %v", err)
	}
	if _, err := v.Verify(ctx, hsStr); err == nil {
		t.Fatal("Verify(HS256) error = nil, want unauthorized")
	}
}
