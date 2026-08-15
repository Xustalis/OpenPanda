package push

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateVAPIDKeysPublicPoint(t *testing.T) {
	k, err := GenerateVAPIDKeys("mailto:test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.RawURLEncoding.DecodeString(k.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != 65 || pub[0] != 0x04 {
		t.Fatalf("public key = %d bytes, want 65-byte uncompressed point", len(pub))
	}
}

func TestLoadOrCreateVAPIDKeysPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vapid.pem")
	k1, err := LoadOrCreateVAPIDKeys(path, "mailto:a@b.c")
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateVAPIDKeys(path, "mailto:a@b.c")
	if err != nil {
		t.Fatal(err)
	}
	if k1.PublicKey() != k2.PublicKey() {
		t.Fatalf("reloaded key differs from original")
	}
}

func TestVAPIDAuthorizationJWT(t *testing.T) {
	k, err := GenerateVAPIDKeys("mailto:test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	authz, err := k.authorization("https://fcm.googleapis.com", 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	// "vapid t=<jwt>, k=<key>"
	if !strings.HasPrefix(authz, "vapid t=") {
		t.Fatalf("authorization = %q", authz)
	}
	rest := strings.TrimPrefix(authz, "vapid t=")
	token, keyPart, ok := strings.Cut(rest, ", k=")
	if !ok {
		t.Fatalf("authorization missing k=: %q", authz)
	}
	if keyPart != k.PublicKey() {
		t.Fatalf("k = %q, want %q", keyPart, k.PublicKey())
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d, want 3", len(parts))
	}
	var hdr struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	decodePart(t, parts[0], &hdr)
	if hdr.Alg != "ES256" || hdr.Typ != "JWT" {
		t.Fatalf("header = %+v", hdr)
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	decodePart(t, parts[1], &claims)
	if claims.Aud != "https://fcm.googleapis.com" {
		t.Fatalf("aud = %q", claims.Aud)
	}
	if claims.Sub != "mailto:test@example.com" {
		t.Fatalf("sub = %q", claims.Sub)
	}
	if claims.Exp != 1700000000+vapidLifetime {
		t.Fatalf("exp = %d, want %d", claims.Exp, 1700000000+vapidLifetime)
	}

	// Verify the ES256 signature against the public key.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature = %d bytes, want 64", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&k.priv.PublicKey, digest[:], r, s) {
		t.Fatal("invalid ES256 signature")
	}
}

func decodePart(t *testing.T, s string, v any) {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal %q: %v", string(b), err)
	}
}
