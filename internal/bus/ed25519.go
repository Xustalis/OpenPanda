// SPDX-License-Identifier: AGPL-3.0-or-later

package bus

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strconv"
)

// GenerateNodeKey creates a new Ed25519 keypair for cryptographic per-node identity.
func GenerateNodeKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// HelloMsgBytes constructs the canonical byte slice signed for a hello handshake.
func HelloMsgBytes(nodeID string, ts int64, nonce string) []byte {
	return []byte(nodeID + ":" + strconv.FormatInt(ts, 10) + ":" + nonce)
}

// SignHelloEd signs the hello handshake message using the node's Ed25519 private key.
func SignHelloEd(privKey ed25519.PrivateKey, nodeID string, ts int64, nonce string) string {
	msg := HelloMsgBytes(nodeID, ts, nonce)
	sig := ed25519.Sign(privKey, msg)
	return hex.EncodeToString(sig)
}

// VerifyHelloEd checks the Ed25519 signature on a hello handshake against the sender's public key.
func VerifyHelloEd(pubKey ed25519.PublicKey, nodeID string, ts int64, nonce string, sigHex string) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg := HelloMsgBytes(nodeID, ts, nonce)
	return ed25519.Verify(pubKey, msg, sig)
}

// AuthMsgBytes constructs the canonical byte slice for a signed Tier-2
// authorization grant. This v1 form binds only the task id — it approves a
// NAME, not a task, and a relay that rewrites the payload between origin and
// executor keeps the grant valid. New signatures use AuthMsgBytesV2; v1 is
// retained only to verify grants minted by pre-digest peers.
func AuthMsgBytes(taskID string, authorized bool, ts int64) []byte {
	return []byte(taskID + ":" + strconv.FormatBool(authorized) + ":" + strconv.FormatInt(ts, 10))
}

// AuthMsgBytesV2 extends the grant with the payload's ConsentDigest, so the
// signature attests "this task id, in this exact content form, approved at
// this time". A relay that edits any intent-bearing field in transit
// invalidates the grant; edits the consent does not cover (chain, budgets)
// stay unsigned by design because relays may legitimately rewrite them.
func AuthMsgBytesV2(taskID string, authorized bool, ts int64, digest string) []byte {
	return []byte(taskID + ":" + strconv.FormatBool(authorized) + ":" + strconv.FormatInt(ts, 10) + ":" + digest)
}

// SignAuthorization signs an approval grant with the approver's private key,
// creating a tamper-proof cryptographic delegation token. digest is the
// payload's ConsentDigest at signing time; "" mints the legacy v1 form and
// should only be used to sign for pre-digest receivers.
func SignAuthorization(privKey ed25519.PrivateKey, taskID string, authorized bool, ts int64, digest string) string {
	msg := AuthMsgBytesV2(taskID, authorized, ts, digest)
	if digest == "" {
		msg = AuthMsgBytes(taskID, authorized, ts)
	}
	sig := ed25519.Sign(privKey, msg)
	return hex.EncodeToString(sig)
}

// VerifyAuthorization verifies that an authorization token was signed by the
// holder of the given public key. A v2 (digest-bound) grant verifies only
// against the digest the EXECUTOR computed over the payload it received —
// transit edits fail closed. A v1 grant has no digest to compare against, so
// it verifies on the bare tuple (the best a legacy mint can prove).
func VerifyAuthorization(pubKey ed25519.PublicKey, taskID string, authorized bool, ts int64, digest, sigHex string) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	if digest != "" && ed25519.Verify(pubKey, AuthMsgBytesV2(taskID, authorized, ts, digest), sig) {
		return true
	}
	return ed25519.Verify(pubKey, AuthMsgBytes(taskID, authorized, ts), sig)
}

// BeaconMsgBytes constructs the canonical byte slice a LAN-discovery beacon
// signs: everything the datagram asserts, so a verified signature proves the
// broadcaster controls the advertised key AND chose this id/address itself.
// The beacon is still only a hint — the signature's job is narrower: stop a
// LAN peer from broadcasting a victim's fingerprint next to its own address.
func BeaconMsgBytes(id, addr, ver, pub string, ts int64) []byte {
	return []byte("panda-beacon:" + id + ":" + addr + ":" + ver + ":" + pub + ":" + strconv.FormatInt(ts, 10))
}

// SignBeacon mints the beacon's Ed25519 signature over its asserted fields.
func SignBeacon(privKey ed25519.PrivateKey, id, addr, ver, pub string, ts int64) string {
	return hex.EncodeToString(ed25519.Sign(privKey, BeaconMsgBytes(id, addr, ver, pub, ts)))
}

// VerifyBeaconSig checks a beacon signature against the key the beacon itself
// advertises — self-signed by design, since discovery predates pairing and no
// trusted key directory exists yet. Verification proves key control, not mesh
// membership: what it buys is an unfakeable fingerprint, not admission.
func VerifyBeaconSig(pubKey ed25519.PublicKey, id, addr, ver, pub string, ts int64, sigHex string) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pubKey, BeaconMsgBytes(id, addr, ver, pub, ts), sig)
}

// ArtifactGrantMsg constructs the canonical byte slice the plan orchestrator
// signs to let a stage's executor pull an input artifact straight from the
// node that produced it (direct stage handoff). Binding the plan, the
// consuming task, the producing task and the content hash means a grant
// cannot be transplanted to another fetch: it attests exactly "in this plan,
// this consumer may pull this hash from this producer's task".
func ArtifactGrantMsg(planID, consumerTaskID, producerTaskID, hash string) []byte {
	return []byte("panda-artifact:" + planID + ":" + consumerTaskID + ":" + producerTaskID + ":" + hash)
}

// SignArtifactGrant mints the orchestrator's Ed25519 grant for a direct
// stage-to-stage artifact pull.
func SignArtifactGrant(privKey ed25519.PrivateKey, planID, consumerTaskID, producerTaskID, hash string) string {
	sig := ed25519.Sign(privKey, ArtifactGrantMsg(planID, consumerTaskID, producerTaskID, hash))
	return hex.EncodeToString(sig)
}

// VerifyArtifactGrant checks an artifact grant against the public key of the
// node that claims to have orchestrated the plan.
func VerifyArtifactGrant(pubKey ed25519.PublicKey, planID, consumerTaskID, producerTaskID, hash, sigHex string) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pubKey, ArtifactGrantMsg(planID, consumerTaskID, producerTaskID, hash), sig)
}
