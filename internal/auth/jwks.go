package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
)

// jwksDocument is the JSON Web Key Set object defined by RFC 7517.
type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

// jwk is the subset of RFC 7517 / RFC 7518 fields this process understands.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// parseJWKS unpacks RSA (n, e) and P-256 (x, y) public keys from a JWKS
// document. It performs no signature checks; that stays in the verifier.
//
// Unknown key types (OKP, oct, …) and keys with use=enc are skipped so a
// foreign set that also contains keys we do not use cannot take the process
// down. A malformed key of a supported type is skipped for the same reason:
// one broken entry must not discard the rest of the set.
func parseJWKS(data []byte, log *slog.Logger) (map[string]crypto.PublicKey, error) {
	var doc jwksDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse JWKS: %w", err)
	}

	out := make(map[string]crypto.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		pub, skipReason, err := publicKeyFromJWK(k)
		if err != nil {
			return nil, err
		}
		if skipReason != "" {
			if log != nil {
				log.Info("skipping JWK", "kid", k.Kid, "kty", k.Kty, "reason", skipReason)
			}
			continue
		}
		out[k.Kid] = pub
	}
	return out, nil
}

func publicKeyFromJWK(k jwk) (crypto.PublicKey, string, error) {
	if k.Use == "enc" {
		return nil, "use=enc", nil
	}
	if k.Kid == "" {
		return nil, "missing kid", nil
	}

	switch k.Kty {
	case "RSA":
		pub, reason, err := rsaPublicKey(k)
		return pub, reason, err
	case "EC":
		pub, reason, err := ecdsaPublicKey(k)
		return pub, reason, err
	default:
		return nil, "unsupported kty", nil
	}
}

func rsaPublicKey(k jwk) (crypto.PublicKey, string, error) {
	n, err := decodeBase64URL(k.N)
	if err != nil {
		return nil, "malformed RSA modulus", nil
	}
	e, err := decodeBase64URL(k.E)
	if err != nil {
		return nil, "malformed RSA exponent", nil
	}
	if len(n) == 0 || len(e) == 0 {
		return nil, "empty RSA modulus or exponent", nil
	}
	exp := 0
	for _, b := range e {
		exp = (exp << 8) | int(b)
		if exp > 1<<24 {
			return nil, "RSA exponent too large", nil
		}
	}
	if exp < 2 {
		return nil, "RSA exponent too small", nil
	}
	mod := new(big.Int).SetBytes(n)
	if mod.Sign() <= 0 {
		return nil, "RSA modulus not positive", nil
	}
	return &rsa.PublicKey{N: mod, E: exp}, "", nil
}

func ecdsaPublicKey(k jwk) (crypto.PublicKey, string, error) {
	if k.Crv != "P-256" {
		return nil, "unsupported curve", nil
	}
	x, err := decodeBase64URL(k.X)
	if err != nil {
		return nil, "malformed EC x", nil
	}
	y, err := decodeBase64URL(k.Y)
	if err != nil {
		return nil, "malformed EC y", nil
	}
	if len(x) == 0 || len(y) == 0 {
		return nil, "empty EC coordinate", nil
	}
	curve := elliptic.P256()
	px := new(big.Int).SetBytes(x)
	py := new(big.Int).SetBytes(y)
	if !curve.IsOnCurve(px, py) {
		return nil, "point not on P-256", nil
	}
	return &ecdsa.PublicKey{Curve: curve, X: px, Y: py}, "", nil
}

// decodeBase64URL accepts both padded and unpadded base64url, which JWKS
// publishers mix in the wild even though RFC 7515 specifies no padding.
func decodeBase64URL(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("empty base64url")
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}
