package bus

import (
	"testing"
	"time"
)

// TestHelloSigVerify exercises the HMAC transport signature (design §16 / P0-1):
// a valid signature passes, any mismatch fails, and an empty secret or empty
// signature always fails (fail-closed).
func TestHelloSigVerify(t *testing.T) {
	const secret = "s3cret"
	now := time.Unix(1_700_000_000, 0)
	sig := HelloSig(secret, "node-a", now.Unix())
	if sig == "" {
		t.Fatal("HelloSig returned empty signature")
	}

	cases := []struct {
		name   string
		secret string
		nodeID string
		ts     int64
		sig    string
		want   bool
	}{
		{"valid", secret, "node-a", now.Unix(), sig, true},
		{"wrong node", secret, "node-b", now.Unix(), sig, false},
		{"wrong secret", "other", "node-a", now.Unix(), sig, false},
		{"empty secret", "", "node-a", now.Unix(), sig, false},
		{"empty sig", secret, "node-a", now.Unix(), "", false},
		{"both empty", "", "node-a", now.Unix(), "", false},
		{"tampered ts", secret, "node-a", now.Unix() + 1, sig, false},
	}
	for _, c := range cases {
		if got := VerifyHello(c.secret, c.nodeID, c.ts, c.sig, now); got != c.want {
			t.Errorf("%s: VerifyHello(%q,%q,%d,%q)=%v, want %v",
				c.name, c.secret, c.nodeID, c.ts, c.sig, got, c.want)
		}
	}
}

// TestVerifyHelloRejectsStale confirms a captured hello ages out of the replay
// window rather than staying valid forever (D5).
func TestVerifyHelloRejectsStale(t *testing.T) {
	const secret = "s3cret"
	now := time.Unix(1_700_000_000, 0)
	ts := now.Unix()
	sig := HelloSig(secret, "node-a", ts)

	if !VerifyHello(secret, "node-a", ts, sig, now) {
		t.Fatalf("fresh hello must verify")
	}
	if VerifyHello(secret, "node-a", ts, sig, now.Add(maxHelloAge+time.Second)) {
		t.Fatalf("stale hello must be rejected")
	}
	if VerifyHello(secret, "node-a", ts, sig, now.Add(-maxHelloAge-time.Second)) {
		t.Fatalf("future-dated hello must be rejected")
	}
}
