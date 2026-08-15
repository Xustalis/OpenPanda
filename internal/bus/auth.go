package bus

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// maxHelloAge bounds how old a hello may be. It is the replay window: a
// captured hello's timestamp ages out of it, so the hello cannot be replayed
// indefinitely. It is generous enough to tolerate P2P clock skew while still
// expiring stale captures.
const maxHelloAge = 5 * time.Minute

// HelloSig computes the HMAC-SHA256 (hex) of nodeID and a unix timestamp under
// the shared secret. Binding the timestamp into the signature means a captured
// hello is only valid within the receiver's tolerance window, so it cannot be
// replayed after that window (design §16 / P0-1).
func HelloSig(secret, nodeID string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(nodeID + ":" + strconv.FormatInt(ts, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyHello reports whether sig is the valid HMAC for nodeID and ts under
// secret, and that ts is within maxHelloAge of now. An empty secret or empty
// signature always fails (fail-closed): a node without a shared secret must not
// authenticate any peer. A stale or future timestamp also fails, so a replayed
// hello ages out rather than remaining valid forever.
func VerifyHello(secret, nodeID string, ts int64, sig string, now time.Time) bool {
	if secret == "" || sig == "" {
		return false
	}
	age := now.Sub(time.Unix(ts, 0))
	if age > maxHelloAge || age < -maxHelloAge {
		return false
	}
	return hmac.Equal([]byte(HelloSig(secret, nodeID, ts)), []byte(sig))
}
