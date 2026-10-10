// SPDX-License-Identifier: AGPL-3.0-or-later

package bus

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// CapSessionAEAD is the hello capability that negotiates per-connection
// frame encryption (design §16). When both sides advertise it and the mesh
// has a shared secret, every application frame after the hello exchange is
// AEAD-sealed under a key derived from the secret and BOTH hello nonces —
// so an on-path observer who only relays the handshake never learns the
// session key, and replaying a captured hello does not reproduce it either
// (the nonces are fresh per dial).
//
// Negotiation is capability-based, never a version parse: a peer without
// this cap keeps the legacy plaintext path, which the cleartext policy
// still gates. A relay that strips the cap to force plaintext downgrade
// only steers the link back into that policy — on non-safe networks the
// connection is refused rather than silently downgraded (fail closed).
const CapSessionAEAD = "sessaead"

// Plaintext tags prefix the sealed payload so the receiver can tell a
// text/JSON frame from a data frame once it has opened the ciphertext —
// the WS opcode is not available inside the encrypted body (all sealed
// frames ride BinaryMessage).
const (
	sessTagText byte = 0x01 // payload is a JSON text frame
	sessTagData byte = 0x02 // payload is a data frame: [u16 hlen][header][body]
)

// sessNonceLen is the AES-GCM standard nonce size.
const sessNonceLen = 12

// SessionAEAD derives the per-connection cipher for a negotiated sessaead
// link. The key is HKDF-SHA256 over the shared secret, bound to the hello
// exchange by info = domain || dialerID || listenerID || dialerNonce ||
// listenerNonce — direction-aware so both ends derive the identical key
// regardless of which hello they saw first, and so a key minted for one
// conn cannot be replayed onto another (nonces are per-dial) or between
// two different node pairs.
func SessionAEAD(secret, dialerID, listenerID, dialerNonce, listenerNonce string) (cipher.AEAD, error) {
	if secret == "" || dialerNonce == "" || listenerNonce == "" {
		return nil, errors.New("bus: session cipher needs secret and both hello nonces")
	}
	info := "panda-ws-sess-v1\x00" + dialerID + "\x00" + listenerID +
		"\x00" + dialerNonce + "\x00" + listenerNonce
	key, err := hkdf.Key(sha256.New, []byte(secret), nil, info, 32)
	if err != nil {
		return nil, fmt.Errorf("bus: session kdf: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("bus: session cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// sealSessFrame encrypts one application frame for the wire:
// nonce || AEAD(tag || payload). The tag survives decryption and tells the
// reader which plaintext framing the payload carries. Random nonces are
// safe here — each conn has its own key, so nonce reuse across conns is
// impossible and within a conn the 96-bit random space makes collision
// negligible; a captured frame cannot be replayed inside the stream anyway
// because TCP delivers bytes once.
func sealSessFrame(aead cipher.AEAD, tag byte, payload []byte) ([]byte, error) {
	nonce := make([]byte, sessNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	plain := make([]byte, 1+len(payload))
	plain[0] = tag
	copy(plain[1:], payload)
	return aead.Seal(nonce, nonce, plain, nil), nil
}

// openSessFrame reverses sealSessFrame and returns the payload with its
// plaintext tag. Any tampering fails the GCM open; a plaintext frame
// smuggled onto an armed conn fails identically — fail closed either way.
func openSessFrame(aead cipher.AEAD, frame []byte) (tag byte, payload []byte, err error) {
	if len(frame) < sessNonceLen+aead.Overhead()+1 {
		return 0, nil, errors.New("bus: short sealed frame")
	}
	plain, err := aead.Open(nil, frame[:sessNonceLen], frame[sessNonceLen:], nil)
	if err != nil {
		return 0, nil, fmt.Errorf("bus: sealed frame auth failed: %w", err)
	}
	tag, payload = plain[0], plain[1:]
	if tag != sessTagText && tag != sessTagData {
		return 0, nil, fmt.Errorf("bus: unknown sealed frame tag 0x%02x", tag)
	}
	return tag, payload, nil
}
