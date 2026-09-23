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
		got.Kind != MsgTaskDelegate || string(got.Payload) != `{"task_id":"t1"}` {
		t.Fatalf("round trip mismatch: %+v", got)
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
