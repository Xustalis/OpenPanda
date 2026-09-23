package bus

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// bundle.go implements the far-track DTN Bundle (whitepaper §8.2/§8.3): a
// self-contained, CBOR-encoded unit carrying an EID-addressed, TTL-bounded,
// HMAC-signed task payload for store-and-forward transport. Where the live
// track is a JSON envelope over an authenticated WebSocket, a bundle must
// survive being parked, relayed, and replayed across links that never
// authenticate — so the header carries its own addressing, lifetime, and
// signature.
//
// The CBOR codec is deliberately minimal (uint/nint/bytes/text/array only —
// exactly the majors a fixed-schema bundle needs): no external dependency,
// no reflection, deterministic output, and therefore stable signatures.

// Bundle is the DTN wire unit: Header (EID/TTL) + kind + payload + signature.
// Fields marshal as a fixed-length CBOR array (toarray semantics) so the
// layout is schema-pinned — a field cannot drift into a different slot.
type Bundle struct {
	BundleID     string // unique id for dedup at relays
	Version      int    // bundle format version: 1 = plaintext payload, 2 = sealed
	SourceEID    string // "panda://<node-id>"
	DestEID      string
	CreatedUnix  int64
	DeadlineUnix int64  // absolute TTL (unix seconds); 0 = no expiry
	Kind         string // the envelope type this bundle carries
	Payload      []byte // wire form: plaintext on v1, nonce||AEAD-ciphertext on v2
	Signature    []byte // HMAC-SHA256 over fields 1..8, keyed by the mesh secret
}

// bundleVersion is the current wire version. Version 2 (farsky) seals the
// payload under AES-256-GCM — the HMAC alone proved integrity but left every
// relay and every listener on a broadcast path holding the task in
// cleartext. Version 1 bundles remain verifiable so a mixed-version mesh
// still delivers.
const bundleVersion = 2
const bundleFields = 9

// NewBundle wraps kind+payload in a signed, TTL-bound bundle addressed from
// srcEID to dstEID. secret is the mesh shared key: the signature makes a
// parked or relayed bundle tamper-evident on media that never authenticates,
// and the payload is sealed first (encrypt-then-sign) so the signed bytes —
// not the plaintext — are what custody carries.
func NewBundle(id, srcEID, dstEID, kind string, deadline int64, payload, secret []byte) (*Bundle, error) {
	b := &Bundle{
		BundleID: id, Version: bundleVersion, SourceEID: srcEID, DestEID: dstEID,
		CreatedUnix: nowUnix(), DeadlineUnix: deadline, Kind: kind,
	}
	sealed, err := b.seal(payload, secret)
	if err != nil {
		return nil, err
	}
	b.Payload = sealed
	b.Signature = b.sign(secret)
	return b, nil
}

// bundleAEAD derives the payload cipher from the mesh secret under its own
// domain separator, so a bundle key is never the same bytes as the hello
// HMAC key or the datagram plane's AEAD key.
func bundleAEAD(secret []byte) (cipher.AEAD, error) {
	key := sha256.Sum256(append([]byte("panda-bundle-aead-v1\x00"), secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// signAAD is the additional-authenticated-data input to the payload seal:
// the canonical encoding of the header fields (id, version, EIDs, times,
// kind) so the ciphertext is bound to the exact addressing and lifetime the
// signature also covers. A relay that swapped the destination could not
// produce a bundle that opens anywhere.
func (b *Bundle) signAAD() []byte {
	var aad []byte
	aad = cborHead(aad, 4, 7)
	aad = cborText(aad, b.BundleID)
	aad = cborUint(aad, uint64(b.Version))
	aad = cborText(aad, b.SourceEID)
	aad = cborText(aad, b.DestEID)
	aad = cborUint(aad, uint64(b.CreatedUnix))
	aad = cborUint(aad, uint64(b.DeadlineUnix))
	aad = cborText(aad, b.Kind)
	return aad
}

// seal encrypts the plaintext payload for a version-2 wire form: random
// nonce || AES-256-GCM ciphertext, with the header AAD above.
func (b *Bundle) seal(plaintext, secret []byte) ([]byte, error) {
	aead, err := bundleAEAD(secret)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(nonce)+len(plaintext)+aead.Overhead())
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, b.signAAD()), nil
}

// Open returns the bundle's plaintext payload. Version-1 bundles carry it
// directly; version-2 unseals with the header AAD, so a bundle that fails
// Open is tampered — never delivered. Call after Verify: Open authenticates
// the payload but says nothing about the signature or TTL.
func (b *Bundle) Open(secret []byte) ([]byte, error) {
	if b.Version == 1 {
		return b.Payload, nil
	}
	aead, err := bundleAEAD(secret)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(b.Payload) < ns+aead.Overhead() {
		return nil, errors.New("bundle: short sealed payload")
	}
	return aead.Open(nil, b.Payload[:ns], b.Payload[ns:], b.signAAD())
}

// EID builds the endpoint id form used on the DTN plane ("panda://node-id").
func EID(nodeID string) string { return "panda://" + nodeID }

// sign produces the bundle's HMAC over fields 1..8, encoded as their own
// 8-element array — the canonical signed form, independent of the signature
// slot's content so verify recomputes the same bytes.
func (b *Bundle) sign(secret []byte) []byte {
	var body []byte
	body = cborHead(body, 4, 8)
	body = cborText(body, b.BundleID)
	body = cborUint(body, uint64(b.Version))
	body = cborText(body, b.SourceEID)
	body = cborText(body, b.DestEID)
	body = cborUint(body, uint64(b.CreatedUnix))
	body = cborUint(body, uint64(b.DeadlineUnix))
	body = cborText(body, b.Kind)
	body = cborBytes(body, b.Payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return mac.Sum(nil)
}

// Verify checks the signature against secret and reports whether the bundle
// is still within its TTL. A bundle that fails either check must not be
// delivered — it is either tampered or dead. Versions 1 and 2 verify
// identically (the signature covers the wire payload either way); Open is
// what differs.
func (b *Bundle) Verify(secret []byte, now int64) error {
	if b.Version != 1 && b.Version != bundleVersion {
		return fmt.Errorf("bundle: unsupported version %d", b.Version)
	}
	if !hmac.Equal(b.Signature, b.sign(secret)) {
		return errors.New("bundle: bad signature")
	}
	if b.DeadlineUnix > 0 && now > b.DeadlineUnix {
		return errors.New("bundle: TTL expired")
	}
	return nil
}

// Marshal serializes the bundle to canonical CBOR (including the signature).
func (b *Bundle) Marshal() []byte {
	var out []byte
	out = cborHead(out, 4, bundleFields)
	out = cborText(out, b.BundleID)
	out = cborUint(out, uint64(b.Version))
	out = cborText(out, b.SourceEID)
	out = cborText(out, b.DestEID)
	out = cborUint(out, uint64(b.CreatedUnix))
	out = cborUint(out, uint64(b.DeadlineUnix))
	out = cborText(out, b.Kind)
	out = cborBytes(out, b.Payload)
	out = cborBytes(out, b.Signature)
	return out
}

// UnmarshalBundle decodes a CBOR bundle produced by Marshal. It checks
// structure only — Verify performs the trust checks.
func UnmarshalBundle(data []byte) (*Bundle, error) {
	r := cborReader{data: data}
	maj, n, err := r.head()
	if err != nil || maj != 4 || n != bundleFields {
		return nil, fmt.Errorf("bundle: bad array head (maj=%d n=%d)", maj, n)
	}
	b := &Bundle{}
	if b.BundleID, err = r.text(); err != nil {
		return nil, err
	}
	if v, err := r.uint(); err != nil {
		return nil, err
	} else {
		b.Version = int(v)
	}
	if b.SourceEID, err = r.text(); err != nil {
		return nil, err
	}
	if b.DestEID, err = r.text(); err != nil {
		return nil, err
	}
	if v, err := r.uint(); err != nil {
		return nil, err
	} else {
		b.CreatedUnix = int64(v)
	}
	if v, err := r.uint(); err != nil {
		return nil, err
	} else {
		b.DeadlineUnix = int64(v)
	}
	if b.Kind, err = r.text(); err != nil {
		return nil, err
	}
	if b.Payload, err = r.bytes(); err != nil {
		return nil, err
	}
	if b.Signature, err = r.bytes(); err != nil {
		return nil, err
	}
	if len(r.data) != 0 {
		return nil, errors.New("bundle: trailing bytes")
	}
	return b, nil
}

// ———— minimal CBOR codec (RFC 8949 majors 0,2,3,4) ————

func cborHead(out []byte, major, val uint64) []byte {
	mt := byte(major << 5)
	switch {
	case val < 24:
		return append(out, mt|byte(val))
	case val <= 0xFF:
		return append(out, mt|24, byte(val))
	case val <= 0xFFFF:
		return append(out, mt|25, byte(val>>8), byte(val))
	case val <= 0xFFFFFFFF:
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(val))
		return append(append(out, mt|26), b[:]...)
	default:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], val)
		return append(append(out, mt|27), b[:]...)
	}
}

func cborUint(out []byte, v uint64) []byte { return cborHead(out, 0, v) }
func cborText(out []byte, s string) []byte {
	return append(cborHead(out, 3, uint64(len(s))), s...)
}
func cborBytes(out []byte, b []byte) []byte {
	return append(cborHead(out, 2, uint64(len(b))), b...)
}

type cborReader struct{ data []byte }

// head reads one item header: returns major type and the argument value.
func (r *cborReader) head() (major byte, val uint64, err error) {
	if len(r.data) == 0 {
		return 0, 0, errors.New("cbor: truncated")
	}
	ib := r.data[0]
	r.data = r.data[1:]
	major, ai := ib>>5, ib&0x1F
	switch {
	case ai < 24:
		return major, uint64(ai), nil
	case ai == 24:
		if len(r.data) < 1 {
			break
		}
		v := uint64(r.data[0])
		r.data = r.data[1:]
		return major, v, nil
	case ai == 25:
		if len(r.data) < 2 {
			break
		}
		v := uint64(binary.BigEndian.Uint16(r.data))
		r.data = r.data[2:]
		return major, v, nil
	case ai == 26:
		if len(r.data) < 4 {
			break
		}
		v := uint64(binary.BigEndian.Uint32(r.data))
		r.data = r.data[4:]
		return major, v, nil
	case ai == 27:
		if len(r.data) < 8 {
			break
		}
		v := binary.BigEndian.Uint64(r.data)
		r.data = r.data[8:]
		return major, v, nil
	}
	return 0, 0, errors.New("cbor: truncated or unsupported head")
}

func (r *cborReader) uint() (uint64, error) {
	maj, v, err := r.head()
	if err != nil || maj != 0 {
		return 0, fmt.Errorf("cbor: expected uint, got major %d err %v", maj, err)
	}
	return v, nil
}

func (r *cborReader) body(expect byte) ([]byte, error) {
	maj, n, err := r.head()
	if err != nil || maj != expect {
		return nil, fmt.Errorf("cbor: expected major %d, got %d err %v", expect, maj, err)
	}
	if n > uint64(len(r.data)) {
		return nil, errors.New("cbor: truncated body")
	}
	b := r.data[:n]
	r.data = r.data[n:]
	return b, nil
}

func (r *cborReader) bytes() ([]byte, error) { return r.body(2) }

func (r *cborReader) text() (string, error) {
	b, err := r.body(3)
	return string(b), err
}
