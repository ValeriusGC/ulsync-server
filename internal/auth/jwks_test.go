package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"
)

func TestParseJWKS_RSA(t *testing.T) {
	t.Parallel()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	kid := "rsa-1"
	raw, err := json.Marshal(map[string]any{
		"keys": []map[string]string{rsaJWK(kid, &priv.PublicKey)},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	keys, err := parseJWKS(raw, nil)
	if err != nil {
		t.Fatalf("parseJWKS() error = %v", err)
	}
	got, ok := keys[kid]
	if !ok {
		t.Fatalf("key %q missing", kid)
	}
	pub, ok := got.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("type = %T, want *rsa.PublicKey", got)
	}
	if pub.N.Cmp(priv.N) != 0 || pub.E != priv.E {
		t.Fatal("parsed RSA key does not match generated key")
	}
}

func TestParseJWKS_P256(t *testing.T) {
	t.Parallel()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	kid := "ec-1"
	raw, err := json.Marshal(map[string]any{
		"keys": []map[string]string{ecJWK(kid, &priv.PublicKey)},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	keys, err := parseJWKS(raw, nil)
	if err != nil {
		t.Fatalf("parseJWKS() error = %v", err)
	}
	got, ok := keys[kid]
	if !ok {
		t.Fatalf("key %q missing", kid)
	}
	pub, ok := got.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("type = %T, want *ecdsa.PublicKey", got)
	}
	if pub.X.Cmp(priv.X) != 0 || pub.Y.Cmp(priv.Y) != 0 {
		t.Fatal("parsed P-256 key does not match generated key")
	}
}

func TestParseJWKS_UnknownKTYLeavesSetIntact(t *testing.T) {
	t.Parallel()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	kid := "rsa-keep"
	raw, err := json.Marshal(map[string]any{
		"keys": []map[string]string{
			{
				"kty": "OKP",
				"kid": "ed25519-1",
				"crv": "Ed25519",
				"x":   "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo",
			},
			{
				"kty": "oct",
				"kid": "oct-1",
				"k":   "AyM1SysPpbyDfgZld3umj1qzKObwVMkoqQ-EstJQLr_T-1qS0gZH75aKtMN3Yj0iPS4hcgUuTwjAzZr1Z9CAow",
			},
			{
				"kty": "EC",
				"kid": "enc-1",
				"use": "enc",
				"crv": "P-256",
				"x":   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				"y":   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			},
			rsaJWK(kid, &priv.PublicKey),
		},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	keys, err := parseJWKS(raw, nil)
	if err != nil {
		t.Fatalf("parseJWKS() error = %v, want the set to survive unknown kty", err)
	}
	if _, ok := keys[kid]; !ok {
		t.Fatal("RSA key dropped because a foreign kty was present")
	}
	if len(keys) != 1 {
		t.Fatalf("len(keys) = %d, want 1 (only the RSA signing key)", len(keys))
	}
}

// rsaJWK serializes an RSA public key into JWKS JSON field map form.
func rsaJWK(kid string, pub *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA",
		"kid": kid,
		"use": "sig",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
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
