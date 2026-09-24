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

	// VerifyHelloP with Ed25519
	p := HelloPayload{
		NodeID: nodeID,
		Ts:     ts,
		Nonce:  nonce,
		PubKey: hex.EncodeToString(pub),
		EdSig:  sig,
	}
	if !VerifyHelloP("", p, time.Now()) {
		t.Fatalf("VerifyHelloP must pass with Ed25519 even with empty shared secret")
	}

	// Tier-2 Authorization signature
	taskID := "task-irreversible-001"
	authSig := SignAuthorization(priv, taskID, true, ts)
	if !VerifyAuthorization(pub, taskID, true, ts, authSig) {
		t.Fatalf("VerifyAuthorization failed for valid approval token")
	}
	if VerifyAuthorization(pub, taskID, false, ts, authSig) {
		t.Fatalf("VerifyAuthorization must fail for flipped authorized boolean")
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
