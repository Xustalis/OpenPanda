package bus

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// HelloSig computes the HMAC-SHA256 (hex) of nodeID under the shared secret. It
// is the transport-level proof that a hello's claimed identity was minted by a
// node holding the secret (design §16 / P0-1).
func HelloSig(secret, nodeID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(nodeID))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyHello reports whether sig is the valid HMAC for nodeID under secret. An
// empty secret or empty signature always fails (fail-closed): a node without a
// shared secret must not authenticate any peer.
func VerifyHello(secret, nodeID, sig string) bool {
	if secret == "" || sig == "" {
		return false
	}
	return hmac.Equal([]byte(HelloSig(secret, nodeID)), []byte(sig))
}
