package bus

import "testing"

// TestHelloSigVerify exercises the HMAC transport signature (design §16 / P0-1):
// a valid signature passes, any mismatch fails, and an empty secret or empty
// signature always fails (fail-closed).
func TestHelloSigVerify(t *testing.T) {
	const secret = "s3cret"
	sig := HelloSig(secret, "node-a")
	if sig == "" {
		t.Fatal("HelloSig returned empty signature")
	}

	cases := []struct {
		name   string
		secret string
		nodeID string
		sig    string
		want   bool
	}{
		{"valid", secret, "node-a", sig, true},
		{"wrong node", secret, "node-b", sig, false},
		{"wrong secret", "other", "node-a", sig, false},
		{"empty secret", "", "node-a", sig, false},
		{"empty sig", secret, "node-a", "", false},
		{"both empty", "", "node-a", "", false},
	}
	for _, c := range cases {
		if got := VerifyHello(c.secret, c.nodeID, c.sig); got != c.want {
			t.Errorf("%s: VerifyHello(%q,%q,%q)=%v, want %v",
				c.name, c.secret, c.nodeID, c.sig, got, c.want)
		}
	}
}
