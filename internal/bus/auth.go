package bus

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// MaxHelloAge bounds how old a hello may be. It is the replay window: a
// captured hello's timestamp ages out of it, so the hello cannot be replayed
// indefinitely. It is generous enough to tolerate P2P clock skew while still
// expiring stale captures. Exported so the receiver can bound its replay cache
// by the same window that governs freshness.
const MaxHelloAge = 5 * time.Minute

// HelloSig computes the HMAC-SHA256 (hex) of nodeID and a unix timestamp under
// the shared secret. Binding the timestamp into the signature means a captured
// hello is only valid within the receiver's tolerance window, so it cannot be
// replayed after that window (design §16 / P0-1).
//
// Deprecated: use HelloSigN. Ts has second granularity, so two dials inside the
// same second mint the same signature — and the receiver's single-use replay
// cache cannot tell an honest same-second reconnect from a replay. HelloSigN
// binds a per-dial nonce so every hello is unique; the old form is still
// accepted by VerifyHello for peers that predate the nonce field.
func HelloSig(secret, nodeID string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(nodeID + ":" + strconv.FormatInt(ts, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// HelloSigN is HelloSig with a nonce bound into the signature. The nonce is a
// per-dial random value the caller carries in HelloPayload.Nonce; two hellos
// that share a timestamp differ in the nonce, so each gets a unique signature
// and the replay cache can reject one without collateral on the other.
func HelloSigN(secret, nodeID string, ts int64, nonce string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(nodeID + ":" + strconv.FormatInt(ts, 10) + ":" + nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyHello reports whether sig is the valid HMAC for nodeID and ts under
// secret, and that ts is within MaxHelloAge of now. An empty secret or empty
// signature always fails (fail-closed): a node without a shared secret must not
// authenticate any peer. A stale or future timestamp also fails, so a replayed
// hello ages out rather than remaining valid forever.
//
// Deprecated: use VerifyHelloP, which also verifies the nonce-bound form.
// Kept for callers that only know the two-field signature (old peers).
func VerifyHello(secret, nodeID string, ts int64, sig string, now time.Time) bool {
	if secret == "" || sig == "" || !helloFresh(ts, now) {
		return false
	}
	return hmac.Equal([]byte(HelloSig(secret, nodeID, ts)), []byte(sig))
}

// VerifyHelloP verifies a hello the way the receiver does: freshness first,
// then the signature against whichever form the payload claims. A payload with
// a Nonce is checked against HelloSigN; one without falls back to HelloSig so
// old peers still authenticate. The nonce is part of what is verified, never
// trusted on its own — an attacker replaying a captured hello cannot dodge the
// replay cache by rewriting it, because a changed nonce changes the signature
// the HMAC must match.
func VerifyHelloP(secret string, p HelloPayload, now time.Time) bool {
	if secret == "" || p.Sig == "" || !helloFresh(p.Ts, now) {
		return false
	}
	if p.Nonce != "" {
		return hmac.Equal([]byte(HelloSigN(secret, p.NodeID, p.Ts, p.Nonce)), []byte(p.Sig))
	}
	return hmac.Equal([]byte(HelloSig(secret, p.NodeID, p.Ts)), []byte(p.Sig))
}

// helloFresh reports whether ts sits within MaxHelloAge of now.
func helloFresh(ts int64, now time.Time) bool {
	age := now.Sub(time.Unix(ts, 0))
	return age <= MaxHelloAge && age >= -MaxHelloAge
}
