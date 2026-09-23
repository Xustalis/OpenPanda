package bus

import (
	"testing"
	"time"
)

func TestBundleRoundTrip(t *testing.T) {
	secret := []byte("mesh-secret")
	b, err := NewBundle("bundle-1", EID("node-a"), EID("node-b"),
		MsgTaskDelegate, time.Now().Add(time.Hour).Unix(), []byte(`{"task_id":"t1"}`), secret)
	if err != nil {
		t.Fatal(err)
	}
	data := b.Marshal()
	got, err := UnmarshalBundle(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.BundleID != b.BundleID || got.DestEID != "panda://node-b" ||
		got.Kind != MsgTaskDelegate {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	// Version 2: the wire payload is sealed ciphertext, not the plaintext —
	// Open is what returns it, bound to the signed header as AAD.
	if got.Version != 2 {
		t.Fatalf("version = %d, want 2", got.Version)
	}
	if string(got.Payload) == `{"task_id":"t1"}` {
		t.Fatal("wire payload is plaintext — not sealed")
	}
	opened, err := got.Open(secret)
	if err != nil {
		t.Fatal("open failed:", err)
	}
	if string(opened) != `{"task_id":"t1"}` {
		t.Fatalf("opened payload = %q", opened)
	}
	if err := got.Verify(secret, time.Now().Unix()); err != nil {
		t.Fatal("verify failed on fresh bundle:", err)
	}
}

func TestBundleVerifyTamper(t *testing.T) {
	secret := []byte("mesh-secret")
	b, _ := NewBundle("b1", EID("a"), EID("b"), MsgTaskDelegate, 0, []byte("payload"), secret)
	data := b.Marshal()
	// Flip a payload byte inside the CBOR body.
	data[len(data)/2] ^= 0xFF
	got, err := UnmarshalBundle(data)
	if err == nil {
		if verr := got.Verify(secret, time.Now().Unix()); verr == nil {
			t.Fatal("tampered bundle verified")
		}
	}
	// Wrong secret fails.
	fresh, _ := UnmarshalBundle(b.Marshal())
	if err := fresh.Verify([]byte("wrong"), time.Now().Unix()); err == nil {
		t.Fatal("bundle verified under wrong secret")
	}
}

func TestBundleTTLExpiry(t *testing.T) {
	secret := []byte("s")
	b, _ := NewBundle("b", EID("a"), EID("b"), MsgTaskDelegate,
		time.Now().Add(-time.Hour).Unix(), []byte("x"), secret)
	got, err := UnmarshalBundle(b.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Verify(secret, time.Now().Unix()); err == nil {
		t.Fatal("expired bundle verified")
	}
}

func TestBundleRejectsGarbage(t *testing.T) {
	for _, data := range [][]byte{nil, {}, {0x9F, 0x61}, []byte("not cbor")} {
		if _, err := UnmarshalBundle(data); err == nil {
			t.Fatalf("decoded garbage %x", data)
		}
	}
}

// TestBundleV2Seal covers the farsky confidentiality layer: tampering with
// the sealed payload fails Verify (the signature covers the ciphertext) AND
// Open under the wrong secret refuses — defense in depth so a relay that
// somehow produced a valid-looking frame still cannot open it.
func TestBundleV2Seal(t *testing.T) {
	secret := []byte("mesh-secret")
	b, err := NewBundle("b2", EID("a"), EID("b"), MsgTaskDelegate, 0, []byte("secret-task"), secret)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalBundle(b.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := got.Open([]byte("other-secret")); err == nil {
		t.Fatal("opened under wrong secret")
	}
	// Corrupt one ciphertext byte: Verify must still fail closed, and Open
	// must not silently hand back plaintext.
	got.Payload[len(got.Payload)-1] ^= 0xFF
	if err := got.Verify(secret, time.Now().Unix()); err == nil {
		t.Fatal("tampered ciphertext verified")
	}
	if _, err := got.Open(secret); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
}

// TestBundleV1Compat verifies a legacy plaintext-payload bundle still
// verifies and opens — a mixed-version mesh keeps delivering while nodes
// upgrade. Constructed by hand since NewBundle only mints v2.
func TestBundleV1Compat(t *testing.T) {
	secret := []byte("mesh-secret")
	b := &Bundle{
		BundleID: "b1", Version: 1, SourceEID: EID("a"), DestEID: EID("b"),
		CreatedUnix: time.Now().Unix(), Kind: MsgTaskDelegate,
		Payload: []byte(`{"task_id":"t1"}`),
	}
	b.Signature = b.sign(secret)
	got, err := UnmarshalBundle(b.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Verify(secret, time.Now().Unix()); err != nil {
		t.Fatal("v1 bundle failed verify:", err)
	}
	opened, err := got.Open(secret)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != `{"task_id":"t1"}` {
		t.Fatalf("v1 opened payload = %q", opened)
	}
}
