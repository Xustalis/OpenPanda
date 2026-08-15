// Package push implements the daemon side of Web Push notifications
// (design P3-26): the VAPID identity (RFC 8292), message encryption
// (RFC 8291 over the aes128gcm content coding from RFC 8188), subscription
// storage, and delivery to the browser's push service. The browser side lives
// in web/pwa (service worker + subscription).
package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// vapidLifetime bounds how long a VAPID token stays valid. RFC 8292 recommends
// no more than 24 hours; 12 is conventional and tolerates modest clock skew.
const vapidLifetime = 12 * 3600

// VAPIDKeys is the application-server identity used to sign push requests. The
// ECDSA P-256 keypair doubles as the browser's applicationServerKey: its public
// point is what the browser subscribes with, and the same key signs each
// request so the push service can verify the sender.
type VAPIDKeys struct {
	priv *ecdsa.PrivateKey
	sub  string // VAPID subject: a mailto: or https: URI identifying the sender
}

// GenerateVAPIDKeys creates a fresh P-256 identity for subject.
func GenerateVAPIDKeys(subject string) (*VAPIDKeys, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate vapid key: %w", err)
	}
	return &VAPIDKeys{priv: priv, sub: subject}, nil
}

// LoadOrCreateVAPIDKeys reads the PEM-encoded key at path (the subject is not
// persisted), or generates and writes a new one. Keys must be stable across
// restarts: rotating the applicationServerKey invalidates every browser
// subscription, so a fresh key per boot would silently break delivery.
func LoadOrCreateVAPIDKeys(path, subject string) (*VAPIDKeys, error) {
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			return parseVAPIDKeys(data, subject)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read vapid key %s: %w", path, err)
		}
	}
	keys, err := GenerateVAPIDKeys(subject)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("vapid key dir: %w", err)
		}
		der, err := x509.MarshalECPrivateKey(keys.priv)
		if err != nil {
			return nil, fmt.Errorf("marshal vapid key: %w", err)
		}
		data := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, fmt.Errorf("write vapid key %s: %w", path, err)
		}
	}
	return keys, nil
}

func parseVAPIDKeys(data []byte, subject string) (*VAPIDKeys, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("vapid key: no PEM block")
	}
	priv, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse vapid key: %w", err)
	}
	return &VAPIDKeys{priv: priv, sub: subject}, nil
}

// PublicKey returns the uncompressed P-256 point, base64url-encoded, for use as
// the browser's applicationServerKey.
func (k *VAPIDKeys) PublicKey() string {
	pub := elliptic.Marshal(k.priv.PublicKey.Curve, k.priv.PublicKey.X, k.priv.PublicKey.Y)
	return base64.RawURLEncoding.EncodeToString(pub)
}

// authorization returns the Authorization header value for a push to aud (the
// push service origin), minting a short-lived ES256 JWT per RFC 8292.
func (k *VAPIDKeys) authorization(aud string, now int64) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}{aud, now + vapidLifetime, k.sub})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign vapid jwt: %w", err)
	}
	// ES256 packs r and s as fixed-width 32-octet big-endian integers.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	jwt := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	return "vapid t=" + jwt + ", k=" + k.PublicKey(), nil
}
