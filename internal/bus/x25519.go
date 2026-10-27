// SPDX-License-Identifier: AGPL-3.0-or-later

package bus

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// X25519 identity keys are the second half of the dual-key node identity
// (cross-network mesh design §3): Ed25519 signs, X25519 agrees keys — one
// key, one job. The private half persists in the settings table next to the
// Ed25519 key (core.nodeKeyPair); the public half rides the hello as x_pub
// and lands in the directory for transit sealing.
func GenerateX25519() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// LoadX25519 decodes a stored private key, failing on any byte-shape that is
// not a canonical X25519 scalar.
func LoadX25519(raw []byte) (*ecdh.PrivateKey, error) {
	return ecdh.X25519().NewPrivateKey(raw)
}

// LoadX25519Pub decodes a peer's advertised public key.
func LoadX25519Pub(raw []byte) (*ecdh.PublicKey, error) {
	return ecdh.X25519().NewPublicKey(raw)
}

// Hello capabilities (additive wire negotiation, same pattern as
// CapBinaryData/CapSessionAEAD): e2et = the node can be a transit
// DESTINATION sealed for with X25519 (it published x_pub and implements
// the open side); trnz = the node understands transit frames and will relay
// them for authenticated members. A peer that lists neither predates the
// feature and is skipped as both relay and destination.
const (
	CapTransitE2E   = "e2et"
	CapTransitRelay = "trnz"
)

// Transit seal levels (design §3). x25519 is end-to-end: the blob opens only
// under the destination's X25519 identity key, so relays see dest + size +
// TTL and nothing else. mesh is the honest-degradation level for a peer that
// never published x_pub: HKDF over the shared secret keeps the contents
// member-confidential on the path but readable by any member relay — the
// sender reports transit_degraded when it has to pick it.
const (
	SealX25519 = "x25519"
	SealMesh   = "mesh"
	// SealNone is the pairing exemption (cross-network mesh P4b): a joiner
	// holds no shared secret yet and does not know the introducer's X25519
	// key, so a transit-wrapped pair_hello cannot be sealed to anything. It
	// carries no secret — the DH box and the SAS do the protecting — and
	// every receiver accepts it ONLY for pair_* inner types. Anything else
	// sealed "none" is refused.
	SealNone = "none"
)

// transitNonceLen is the AES-GCM standard nonce size, same as sessenc.
const transitNonceLen = 12

// x25519PubLen is the wire length of an X25519 public key.
const x25519PubLen = 32

// TransitSealX25519 wraps plaintext for destPub with an ephemeral-key ECIES:
// blob = eph_pub(32) ‖ nonce(12) ‖ AEAD(plaintext). The AEAD key derives
// from ECDH(eph, destPub) through HKDF bound to BOTH public keys, so the
// ciphertext authenticates "sealed by someone, for exactly this identity" —
// and the relay carrying the blob learns nothing a size field and the dest
// envelope do not already say. An ephemeral sender key keeps sender static
// material out of the derivation entirely: identity proof for the receiver
// lives inside the plaintext (the transit inner's Ed25519 signature), not
// in this channel.
func TransitSealX25519(destPub *ecdh.PublicKey, plaintext []byte) ([]byte, error) {
	eph, err := GenerateX25519()
	if err != nil {
		return nil, fmt.Errorf("bus: transit eph key: %w", err)
	}
	shared, err := eph.ECDH(destPub)
	if err != nil {
		return nil, fmt.Errorf("bus: transit ecdh: %w", err)
	}
	ephPub := eph.PublicKey().Bytes()
	info := "panda-transit-v1\x00" + string(ephPub) + "\x00" + string(destPub.Bytes())
	aead, err := transitAEAD(shared, info)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, transitNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("bus: transit nonce: %w", err)
	}
	blob := make([]byte, 0, x25519PubLen+transitNonceLen+len(plaintext)+aead.Overhead())
	blob = append(blob, ephPub...)
	blob = append(blob, nonce...)
	return aead.Seal(blob, nonce, plaintext, nil), nil
}

// TransitOpenX25519 reverses TransitSealX25519 under the destination's
// private key. A malformed blob — short, wrong curve point, failed GCM —
// fails closed: a relay that tampered learns nothing, and the receiver
// drops rather than dispatching a guessed plaintext.
func TransitOpenX25519(priv *ecdh.PrivateKey, blob []byte) ([]byte, error) {
	if len(blob) < x25519PubLen+transitNonceLen {
		return nil, errors.New("bus: short transit blob")
	}
	ephPub, err := LoadX25519Pub(blob[:x25519PubLen])
	if err != nil {
		return nil, fmt.Errorf("bus: transit eph pub: %w", err)
	}
	shared, err := priv.ECDH(ephPub)
	if err != nil {
		return nil, fmt.Errorf("bus: transit ecdh: %w", err)
	}
	info := "panda-transit-v1\x00" + string(blob[:x25519PubLen]) + "\x00" + string(priv.PublicKey().Bytes())
	aead, err := transitAEAD(shared, info)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, blob[x25519PubLen:x25519PubLen+transitNonceLen], blob[x25519PubLen+transitNonceLen:], nil)
}

// TransitSealMesh is the member-level fallback seal: AEAD keyed by HKDF over
// the mesh shared secret with the destination identity bound into info, so
// a blob minted for one peer cannot be opened under a different dest claim.
// Every member holds the secret — this is path confidentiality, not
// end-to-end, and the caller marks it degraded.
func TransitSealMesh(secret, destKey string, plaintext []byte) ([]byte, error) {
	if secret == "" {
		return nil, errors.New("bus: mesh seal needs the shared secret")
	}
	aead, err := transitAEAD([]byte(secret), "panda-transit-mesh-v1\x00"+destKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, transitNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("bus: transit nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

// TransitOpenMesh reverses TransitSealMesh.
func TransitOpenMesh(secret, destKey string, blob []byte) ([]byte, error) {
	if secret == "" {
		return nil, errors.New("bus: mesh seal needs the shared secret")
	}
	if len(blob) < transitNonceLen {
		return nil, errors.New("bus: short transit blob")
	}
	aead, err := transitAEAD([]byte(secret), "panda-transit-mesh-v1\x00"+destKey)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, blob[:transitNonceLen], blob[transitNonceLen:], nil)
}

// UDPPairAEAD derives the datagram-plane AEAD for one peer pair: a static-
// static X25519 DH between the two identity keys, run through HKDF with both
// node ids bound in sorted order so the two sides derive the same key
// regardless of who speaks first.
//
// This is what makes a datagram's From provable. The mesh-wide key every
// member holds can only prove membership — any member can mint a frame
// claiming to be any other — so the datagram plane seals to the pair instead.
// No handshake is needed: each side already publishes its X25519 public key in
// an Ed25519-signed hello, and a third member watching the exchange learns
// nothing, because the derivation needs a private key it does not have.
func UDPPairAEAD(priv *ecdh.PrivateKey, peerPub *ecdh.PublicKey, selfID, peerID string) (cipher.AEAD, error) {
	if priv == nil || peerPub == nil || selfID == "" || peerID == "" {
		return nil, errors.New("bus: udp pair key needs both identities")
	}
	shared, err := priv.ECDH(peerPub)
	if err != nil {
		return nil, fmt.Errorf("bus: udp pair ecdh: %w", err)
	}
	a, b := selfID, peerID
	if b < a {
		a, b = b, a
	}
	return transitAEAD(shared, "panda-udp-aead-v2\x00"+a+"\x00"+b)
}

// transitAEAD turns raw key material into AES-256-GCM through HKDF-SHA256 —
// the same derivation discipline SessionAEAD uses.
func transitAEAD(keyMaterial []byte, info string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, keyMaterial, nil, info, 32)
	if err != nil {
		return nil, fmt.Errorf("bus: transit kdf: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("bus: transit cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// TransitInnerMsg is the canonical byte string the sealed inner's Ed25519
// attestation covers: the marshalled inner envelope verbatim. Binding the
// signature to the exact bytes means a relay cannot rewrite From, MsgID or
// the payload without breaking it — env.From inside a transit is verified
// content, not a conn-bound claim.
func TransitInnerMsg(envJSON []byte) []byte {
	return append([]byte("panda-transit-inner-v1\x00"), envJSON...)
}

// SignTransitInner attests the marshalled inner envelope under the sender's
// Ed25519 identity. The signature is what makes env.From inside a transit
// mean something: a direct frame's From is bound to the conn it arrived on,
// but a transit inner arrives on a relay's conn — this signature is the only
// provenance it carries. Returns hex, matching the hello EdSig form.
func SignTransitInner(priv ed25519.PrivateKey, envJSON []byte) string {
	return hex.EncodeToString(ed25519.Sign(priv, TransitInnerMsg(envJSON)))
}

// VerifyTransitInner checks a transit inner attestation. A short or malformed
// hex signature fails closed, the same as a bad GCM tag.
func VerifyTransitInner(pub ed25519.PublicKey, envJSON []byte, sigHex string) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, TransitInnerMsg(envJSON), sig)
}
