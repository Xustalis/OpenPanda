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

// AuthMsgBytes constructs the canonical byte slice for a signed Tier-2 authorization grant.
func AuthMsgBytes(taskID string, authorized bool, ts int64) []byte {
	return []byte(taskID + ":" + strconv.FormatBool(authorized) + ":" + strconv.FormatInt(ts, 10))
}

// SignAuthorization signs an approval grant with the approver's private key,
// creating a tamper-proof cryptographic delegation token.
func SignAuthorization(privKey ed25519.PrivateKey, taskID string, authorized bool, ts int64) string {
	msg := AuthMsgBytes(taskID, authorized, ts)
	sig := ed25519.Sign(privKey, msg)
	return hex.EncodeToString(sig)
}

// VerifyAuthorization verifies that an authorization token was signed by the holder
// of the given public key.
func VerifyAuthorization(pubKey ed25519.PublicKey, taskID string, authorized bool, ts int64, sigHex string) bool {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg := AuthMsgBytes(taskID, authorized, ts)
	return ed25519.Verify(pubKey, msg, sig)
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
