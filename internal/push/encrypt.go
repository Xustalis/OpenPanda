package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// recordSize is the aes128gcm record size written into the body header. It only
// needs to exceed payload + delimiter + tag; 4096 is the conventional value.
const recordSize = 4096

// encrypt seals payload for the subscription per RFC 8291 (Web Push message
// encryption) using the aes128gcm content coding of RFC 8188. It returns the
// complete request body: an 86-octet header (salt || rs || idlen || ephemeral
// public key) followed by the AES-128-GCM ciphertext of payload || 0x02.
//
// The key schedule matches the reference implementation (http_ece):
//
//	ikm    = ECDH(as_private, ua_public)
//	secret = HKDF(salt=auth_secret, ikm, "WebPush: info" || 0x00 || ua_public || as_public, 32)
//	prk    = HKDF-Extract(salt=random16, secret)
//	cek    = HKDF-Expand(prk, "Content-Encoding: aes128gcm" || 0x00, 16)
//	nonce  = HKDF-Expand(prk, "Content-Encoding: nonce" || 0x00, 12)
func encrypt(uaPublic, authSecret, payload []byte) ([]byte, error) {
	if len(authSecret) != 16 {
		return nil, fmt.Errorf("subscription auth must be 16 bytes, got %d", len(authSecret))
	}

	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ephemeral key: %w", err)
	}
	asPublic := asPriv.PublicKey().Bytes()

	uaKey, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("subscription p256dh: %w", err)
	}
	shared, err := asPriv.ECDH(uaKey)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}

	// key_info = "WebPush: info" || 0x00 || ua_public || as_public.
	keyInfo := make([]byte, 0, len("WebPush: info")+1+len(uaPublic)+len(asPublic))
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaPublic...)
	keyInfo = append(keyInfo, asPublic...)
	secret, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("salt: %w", err)
	}
	prk, err := hkdf.Extract(sha256.New, secret, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// A push message is a single record, so the plaintext is the payload with
	// the 0x02 padding delimiter appended and no zero padding (RFC 8188 §2
	// last-record rule).
	pt := make([]byte, 0, len(payload)+1)
	pt = append(pt, payload...)
	pt = append(pt, 0x02)
	ct := gcm.Seal(nil, nonce, pt, nil)

	body := make([]byte, 0, 16+4+1+len(asPublic)+len(ct))
	body = append(body, salt...)
	body = append(body, byte(recordSize>>24), byte(recordSize>>16), byte(recordSize>>8), byte(recordSize&0xff))
	body = append(body, byte(len(asPublic)))
	body = append(body, asPublic...)
	body = append(body, ct...)
	return body, nil
}
