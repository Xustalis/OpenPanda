// SPDX-License-Identifier: AGPL-3.0-or-later

package bus

import (
	"encoding/hex"
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
	// HelloSig is the deprecated compatibility entry point. Verification and
	// the handshake diagnostics use the non-deprecated form, so the two must
	// stay byte-identical or legacy peers would be misdiagnosed.
	if got := HelloSigLegacy(secret, "node-a", now.Unix()); got != sig {
		t.Fatalf("HelloSigLegacy = %q, want the deprecated form's bytes %q", got, sig)
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
	if VerifyHello(secret, "node-a", ts, sig, now.Add(MaxHelloAge+time.Second)) {
		t.Fatalf("stale hello must be rejected")
	}
	if VerifyHello(secret, "node-a", ts, sig, now.Add(-MaxHelloAge-time.Second)) {
		t.Fatalf("future-dated hello must be rejected")
	}
}

func TestVerifyHelloPWithNonce(t *testing.T) {
	const secret = "s3cret"
	now := time.Unix(1_700_000_000, 0)
	ts := now.Unix()
	const nonce = "nonce-123"
	sigN := HelloSigN(secret, "node-a", ts, nonce)
	sigLegacy := HelloSig(secret, "node-a", ts)

	// Nonce-bound hello verifies.
	pValid := HelloPayload{NodeID: "node-a", Ts: ts, Nonce: nonce, Sig: sigN}
	if !VerifyHelloP(secret, "node-b", pValid, now) {
		t.Fatalf("valid nonce-bound hello must verify")
	}

	// Tampered nonce fails.
	pBadNonce := HelloPayload{NodeID: "node-a", Ts: ts, Nonce: "other-nonce", Sig: sigN}
	if VerifyHelloP(secret, "node-b", pBadNonce, now) {
		t.Fatalf("tampered nonce must be rejected")
	}

	// Legacy hello without nonce falls back to HelloSig.
	pLegacy := HelloPayload{NodeID: "node-a", Ts: ts, Sig: sigLegacy}
	if !VerifyHelloP(secret, "node-b", pLegacy, now) {
		t.Fatalf("legacy hello without nonce must verify against HelloSig")
	}

	// Legacy hello with tampered sig fails.
	pLegacyBad := HelloPayload{NodeID: "node-a", Ts: ts, Sig: "badsig"}
	if VerifyHelloP(secret, "node-b", pLegacyBad, now) {
		t.Fatalf("bad signature must be rejected")
	}

	// Stale nonce-bound hello fails.
	if VerifyHelloP(secret, "node-b", pValid, now.Add(MaxHelloAge+time.Second)) {
		t.Fatalf("stale nonce-bound hello must be rejected")
	}
}

// TestVerifyHelloPBoundToReceiver covers the cross-node replay fix: a hello
// signed for one receiver verifies there and nowhere else, a bound hello
// without its bound signature is refused, and the legacy form keeps working
// for peers that send no binding at all.
func TestVerifyHelloPBoundToReceiver(t *testing.T) {
	const secret = "s3cret"
	now := time.Unix(1_700_000_000, 0)
	ts := now.Unix()
	const nonce = "nonce-123"
	const dialer, receiver, other = "node-a", "node-b", "node-c"

	bound := HelloPayload{
		NodeID: dialer,
		To:     receiver,
		Ts:     ts,
		Nonce:  nonce,
		Sig:    HelloSigN(secret, dialer, ts, nonce),
		SigB:   HelloSigB(secret, dialer, receiver, ts, nonce),
	}
	if !VerifyHelloP(secret, receiver, bound, now) {
		t.Fatalf("hello bound to %q must verify at %q", receiver, receiver)
	}
	if VerifyHelloP(secret, other, bound, now) {
		t.Fatalf("hello bound to %q must not verify at %q — this is the replay", receiver, other)
	}

	// The receiver id is part of the signed bytes: a hello re-addressed by
	// editing only the field keeps a signature that no longer matches.
	readdressed := bound
	readdressed.To = other
	if VerifyHelloP(secret, other, readdressed, now) {
		t.Fatalf("re-addressed bound hello must be rejected")
	}

	// A hello that names a receiver but carries only the legacy signature is
	// the downgraded shape: refuse rather than fall back.
	stripped := bound
	stripped.SigB = ""
	if VerifyHelloP(secret, receiver, stripped, now) {
		t.Fatalf("bound hello with the bound signature stripped must be rejected")
	}
	if VerifyHelloP(secret, "", bound, now) {
		t.Fatalf("bound hello must be rejected by a receiver with no id")
	}

	// Empty secret and stale timestamps still fail on the bound path.
	if VerifyHelloP("", receiver, bound, now) {
		t.Fatalf("empty secret must fail")
	}
	if VerifyHelloP(secret, receiver, bound, now.Add(MaxHelloAge+time.Second)) {
		t.Fatalf("stale bound hello must fail")
	}
}

// TestVerifyHelloPBoundIdentity covers the Ed25519 half of the binding: a
// bound hello carrying identity must prove that identity over the same bytes,
// so an attacker holding the shared secret cannot mint a bound hello for a
// node whose private key it does not have.
func TestVerifyHelloPBoundIdentity(t *testing.T) {
	const secret = "s3cret"
	now := time.Unix(1_700_000_000, 0)
	ts := now.Unix()
	const nonce = "nonce-123"
	const dialer, receiver, other = "node-a", "node-b", "node-c"

	pub, priv, err := GenerateNodeKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	keyHex := hex.EncodeToString(pub)
	bound := HelloPayload{
		NodeID: dialer,
		To:     receiver,
		Ts:     ts,
		Nonce:  nonce,
		Sig:    HelloSigN(secret, dialer, ts, nonce),
		SigB:   HelloSigB(secret, dialer, receiver, ts, nonce),
		PubKey: keyHex,
		EdSig:  SignHelloEd(priv, dialer, ts, nonce),
		EdSigB: SignHelloEdB(priv, dialer, receiver, ts, nonce),
	}
	if !VerifyHelloP(secret, receiver, bound, now) {
		t.Fatalf("bound hello with identity must verify")
	}
	if VerifyHelloP(secret, other, bound, now) {
		t.Fatalf("bound hello must not verify at another receiver")
	}

	// Identity present but bound signature missing: fail closed.
	noEdB := bound
	noEdB.EdSigB = ""
	if VerifyHelloP(secret, receiver, noEdB, now) {
		t.Fatalf("bound hello without its bound identity signature must be rejected")
	}

	// Identity bound to a different receiver does not verify here.
	wrongEdB := bound
	wrongEdB.EdSigB = SignHelloEdB(priv, dialer, other, ts, nonce)
	if VerifyHelloP(secret, receiver, wrongEdB, now) {
		t.Fatalf("identity signature for %q must not verify at %q", other, receiver)
	}
}
