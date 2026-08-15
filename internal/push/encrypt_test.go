package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"
)

func TestEncryptRoundTrip(t *testing.T) {
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uaPublic := uaPriv.PublicKey().Bytes()
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"title":"hi","body":"needs review","id":"abc"}`)

	body, err := encrypt(uaPublic, authSecret, payload)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptForTest(body, uaPriv, uaPublic, authSecret)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("round trip = %q, want %q", got, payload)
	}
}

func TestEncryptBodyHeader(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	uaPublic := uaPriv.PublicKey().Bytes()
	authSecret := make([]byte, 16)

	body, err := encrypt(uaPublic, authSecret, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) < 21 {
		t.Fatalf("body too short: %d bytes", len(body))
	}
	rs := binary.BigEndian.Uint32(body[16:20])
	if rs != recordSize {
		t.Fatalf("rs = %d, want %d", rs, recordSize)
	}
	idlen := body[20]
	if idlen != 65 {
		t.Fatalf("idlen = %d, want 65", idlen)
	}
	// header (21 + 65) + ciphertext(1 payload + 1 delimiter) + 16 tag.
	if want := 21 + 65 + 1 + 1 + 16; len(body) != want {
		t.Fatalf("body length = %d, want %d", len(body), want)
	}
}

func TestEncryptRejectsBadAuth(t *testing.T) {
	if _, err := encrypt(make([]byte, 65), make([]byte, 8), []byte("x")); err == nil {
		t.Fatal("expected error for 8-byte auth secret")
	}
}

// decryptForTest is an independent reference decryption written directly from
// RFC 8291/8188, deliberately not sharing the encrypt path under test.
func decryptForTest(body []byte, uaPriv *ecdh.PrivateKey, uaPublic, authSecret []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("body too short")
	}
	salt := body[:16]
	idlen := int(body[20])
	if len(body) < 21+idlen {
		return nil, errors.New("truncated header")
	}
	asPublic := body[21 : 21+idlen]
	ct := body[21+idlen:]

	asKey, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	shared, err := uaPriv.ECDH(asKey)
	if err != nil {
		return nil, err
	}

	keyInfo := append(append([]byte("WebPush: info\x00"), uaPublic...), asPublic...)
	secret, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
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
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, err
	}
	if len(pt) == 0 || pt[len(pt)-1] != 0x02 {
		return nil, errors.New("missing 0x02 padding delimiter")
	}
	return pt[:len(pt)-1], nil
}
