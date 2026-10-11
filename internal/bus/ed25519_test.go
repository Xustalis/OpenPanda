// SPDX-License-Identifier: AGPL-3.0-or-later

package bus

import (
	"encoding/hex"
	"testing"
	"time"
)

func TestEd25519NodeIdentityAndAuth(t *testing.T) {
	pub, priv, err := GenerateNodeKey()
	if err != nil {
		t.Fatalf("GenerateNodeKey failed: %v", err)
	}

	nodeID := "node-sat-1"
	ts := time.Now().Unix()
	nonce := "random-nonce-1234"

	sig := SignHelloEd(priv, nodeID, ts, nonce)
	if !VerifyHelloEd(pub, nodeID, ts, nonce, sig) {
		t.Fatalf("VerifyHelloEd failed for valid signature")
	}

	// Tampered nodeID must fail
	if VerifyHelloEd(pub, "node-sat-2", ts, nonce, sig) {
		t.Fatalf("VerifyHelloEd must fail for tampered nodeID")
	}

	// Tampered signature must fail
	badSig := sig[:len(sig)-2] + "ff"
	if VerifyHelloEd(pub, nodeID, ts, nonce, badSig) {
		t.Fatalf("VerifyHelloEd must fail for corrupted signature")
	}

	// VerifyHelloP: Ed25519 attests identity INSIDE the mesh — it must NOT
	// authenticate on its own. Membership is proven by the shared-secret HMAC,
	// which is always required; a self-asserted keypair proves nothing about
	// membership, so a secret-less (or wrong-secret) hello must be rejected
	// even when its EdSig is cryptographically perfect.
	p := HelloPayload{
		NodeID: nodeID,
		Ts:     ts,
		Nonce:  nonce,
		PubKey: hex.EncodeToString(pub),
		EdSig:  sig,
		Sig:    HelloSigN("mesh-secret", nodeID, ts, nonce),
	}
	if !VerifyHelloP("mesh-secret", p, time.Now()) {
		t.Fatalf("VerifyHelloP must pass with HMAC membership + valid Ed25519 identity")
	}
	if VerifyHelloP("", p, time.Now()) {
		t.Fatalf("VerifyHelloP must not pass on Ed25519 identity alone (no shared secret)")
	}
	if VerifyHelloP("other-secret", p, time.Now()) {
		t.Fatalf("VerifyHelloP must not pass under a wrong shared secret")
	}
	// Half an identity is a malformed hello, not a legacy peer.
	pNoEdSig := p
	pNoEdSig.EdSig = ""
	if VerifyHelloP("mesh-secret", pNoEdSig, time.Now()) {
		t.Fatalf("VerifyHelloP must reject a PubKey with no EdSig")
	}
	// A bad EdSig under a valid HMAC is tampering — fail closed.
	pBadEd := p
	pBadEd.EdSig = sig[:len(sig)-2] + "ff"
	if VerifyHelloP("mesh-secret", pBadEd, time.Now()) {
		t.Fatalf("VerifyHelloP must reject a corrupt EdSig even with a valid HMAC")
	}

	// Tier-2 Authorization signature — the v2 form binds the consent digest,
	// so verification needs the same digest the signer attested over.
	taskID := "task-irreversible-001"
	const digest = "consent-digest"
	authSig := SignAuthorization(priv, taskID, true, ts, digest)
	if !VerifyAuthorization(pub, taskID, true, ts, digest, authSig) {
		t.Fatalf("VerifyAuthorization failed for valid approval token")
	}
	if VerifyAuthorization(pub, taskID, false, ts, digest, authSig) {
		t.Fatalf("VerifyAuthorization must fail for flipped authorized boolean")
	}
	if VerifyAuthorization(pub, taskID, true, ts, "other-digest", authSig) {
		t.Fatalf("VerifyAuthorization must fail under a different digest")
	}
}

func TestVerifyHelloEdRejectsWrongKey(t *testing.T) {
	_, priv1, _ := GenerateNodeKey()
	pub2, _, _ := GenerateNodeKey()

	nodeID := "node-a"
	ts := time.Now().Unix()
	nonce := "nonce-abc"
	sig := SignHelloEd(priv1, nodeID, ts, nonce)

	if VerifyHelloEd(pub2, nodeID, ts, nonce, sig) {
		t.Fatalf("VerifyHelloEd must fail when verified against different public key")
	}
}

func TestArtifactGrantRoundTrip(t *testing.T) {
	pub, priv, err := GenerateNodeKey()
	if err != nil {
		t.Fatalf("GenerateNodeKey: %v", err)
	}
	const planID, consumer, producer, hash = "plan-1", "t-consumer", "t-producer", "deadbeef"

	grant := SignArtifactGrant(priv, planID, consumer, producer, hash)
	if !VerifyArtifactGrant(pub, planID, consumer, producer, hash, grant) {
		t.Fatal("valid artifact grant rejected")
	}
	// Every bound field must hold: a grant transplanted to another consumer,
	// producer, plan or artifact must not verify.
	for _, tc := range []struct{ plan, cons, prod, h string }{
		{"plan-2", consumer, producer, hash},
		{planID, "t-other", producer, hash},
		{planID, consumer, "t-other", hash},
		{planID, consumer, producer, "cafe"},
	} {
		if VerifyArtifactGrant(pub, tc.plan, tc.cons, tc.prod, tc.h, grant) {
			t.Fatalf("grant verified under transplanted tuple %+v", tc)
		}
	}
	// A different signer must not mint for the orchestrator.
	_, priv2, _ := GenerateNodeKey()
	forged := SignArtifactGrant(priv2, planID, consumer, producer, hash)
	if VerifyArtifactGrant(pub, planID, consumer, producer, hash, forged) {
		t.Fatal("forged grant verified under orchestrator key")
	}
}

func TestAcceptanceRoundTrip(t *testing.T) {
	pub, priv, err := GenerateNodeKey()
	if err != nil {
		t.Fatalf("GenerateNodeKey: %v", err)
	}
	const taskID, attemptID = "task-1", "att-1"
	nodeID := "k:" + hex.EncodeToString(pub)
	ts := time.Now().Unix()

	sig := SignAcceptance(priv, taskID, attemptID, nodeID, ts)
	if !VerifyAcceptance(pub, taskID, attemptID, nodeID, ts, sig) {
		t.Fatal("valid acceptance rejected")
	}
	// Every bound field is load-bearing: a signature transplanted to another
	// task, attempt, signer id or timestamp must not verify.
	for _, tc := range []struct {
		task, attempt, node string
		ts                  int64
	}{
		{"task-2", attemptID, nodeID, ts},
		{taskID, "att-2", nodeID, ts},
		{taskID, attemptID, "k:other", ts},
		{taskID, attemptID, nodeID, ts + 1},
	} {
		if VerifyAcceptance(pub, tc.task, tc.attempt, tc.node, tc.ts, sig) {
			t.Fatalf("acceptance verified under transplanted tuple %+v", tc)
		}
	}
	// A different signer cannot mint for the executor.
	_, priv2, _ := GenerateNodeKey()
	forged := SignAcceptance(priv2, taskID, attemptID, nodeID, ts)
	if VerifyAcceptance(pub, taskID, attemptID, nodeID, ts, forged) {
		t.Fatal("forged acceptance verified under executor key")
	}
	// Malformed signature encodings fail closed, not into legacy acceptance.
	if VerifyAcceptance(pub, taskID, attemptID, nodeID, ts, "zz") {
		t.Fatal("non-hex signature verified")
	}
	if VerifyAcceptance(pub, taskID, attemptID, nodeID, ts, sig[:len(sig)/2]) {
		t.Fatal("truncated signature verified")
	}
}
