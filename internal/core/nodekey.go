package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// Node identity (design §16, P2-8 follow-up). The shared-secret HMAC proves a
// peer holds the mesh key; it cannot prove WHICH node sent a message or minted
// a consent. The Ed25519 keypair in this file closes that gap: each node keeps
// a persistent private key in the settings table, signs its hellos with it,
// and every peer records the advertised key in employee_cache.pub_key. Signed
// artifacts (tier-2 consent grants, stage handoff grants) are then verified
// against the directory's record of the claimed signer, not against whatever
// key the payload itself presents.
//
// The scheme is deliberately additive: peers that predate keys send no
// PubKey/EdSig and verify exactly as before, so a mixed-version mesh keeps
// working — signed content simply cannot be minted or verified for them.
const nodeKeySetting = "node_ed25519_priv"

// nodeKeyPair returns this node's Ed25519 identity, lazily generating and
// persisting it on first use. The private key lives in the settings table so
// it survives restarts without landing in config.yaml. A node whose database
// cannot hold it (read-only fixture, missing settings table) reports
// !ok and every signing site degrades to the legacy unsigned wire form.
func (c *Core) nodeKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, bool) {
	c.keyOnce.Do(func() {
		var stored string
		err := c.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, nodeKeySetting).Scan(&stored)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			pub, priv, gerr := bus.GenerateNodeKey()
			if gerr != nil {
				c.logger.Warn("node key generate", "err", gerr)
				return
			}
			if _, werr := c.db.Exec(
				`INSERT INTO settings (key, value) VALUES (?, ?)`, nodeKeySetting,
				hex.EncodeToString(priv)); werr != nil {
				c.logger.Warn("node key persist", "err", werr)
				return
			}
			c.nodePub, c.nodePriv = pub, priv
			c.logger.Info("node identity generated", "pubkey", hex.EncodeToString(pub)[:16]+"…")
		case err != nil:
			c.logger.Warn("node key load", "err", err)
		default:
			raw, derr := hex.DecodeString(stored)
			if derr != nil || len(raw) != ed25519.PrivateKeySize {
				c.logger.Warn("node key corrupt in settings")
				return
			}
			c.nodePriv = ed25519.PrivateKey(raw)
			c.nodePub = c.nodePriv.Public().(ed25519.PublicKey)
		}
	})
	if c.nodePriv == nil {
		return nil, nil, false
	}
	return c.nodePub, c.nodePriv, true
}

// peerPubKey resolves a peer's recorded Ed25519 key from the directory. The
// directory is populated from signed hellos (handleHello), so the returned key
// is the one the peer proved it controls — never a key pulled from the message
// being verified, which would let a signer attest with its own key under a
// victim's name.
func (c *Core) peerPubKey(nodeID string) (ed25519.PublicKey, bool) {
	var hexKey string
	if err := c.db.QueryRow(
		`SELECT pub_key FROM employee_cache WHERE id = ?`, nodeID).Scan(&hexKey); err != nil || hexKey == "" {
		return nil, false
	}
	raw, err := hex.DecodeString(hexKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, false
	}
	return ed25519.PublicKey(raw), true
}

// recordPeerPubKey stores the key a peer proved in its signed hello. The row
// is upserted rather than inserted-blind so a card-less peer still leaves a
// resolvable identity behind (UpsertRemote only runs when a card arrives).
// A bare-HMAC hello carries no key and updates nothing: the stored key keeps
// attesting what the last signed hello proved.
func (c *Core) recordPeerPubKey(ctx context.Context, nodeID, pubKeyHex string) {
	if pubKeyHex == "" {
		return
	}
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO employee_cache (id, pub_key, status, last_seen) VALUES (?, ?, 'offline', 0)
		 ON CONFLICT(id) DO UPDATE SET pub_key = excluded.pub_key`,
		nodeID, pubKeyHex); err != nil {
		c.logger.Warn("record peer pubkey", "peer", nodeID, "err", err)
	}
}

// signConsentGrant attaches this node's Ed25519 signature over
// (taskID, authorized, ts) to an outgoing delegate — the tamper-proof form of
// the Authorized flag (P2-8). Called only where consent is freshly minted:
// relayed payloads already carry the origin's grant and must forward it
// verbatim rather than re-signing under a key the origin never used.
func (c *Core) signConsentGrant(p *bus.TaskDelegatePayload) {
	if !p.Authorized {
		return
	}
	pub, priv, ok := c.nodeKeyPair()
	if !ok {
		return
	}
	p.AuthTs = time.Now().Unix()
	p.AuthSig = bus.SignAuthorization(priv, p.TaskID, p.Authorized, p.AuthTs)
	p.AuthPub = hex.EncodeToString(pub)
}

// consentGrantValid decides whether the Authorized flag on an inbound delegate
// may be adopted. Three shapes:
//
//   - Unsigned (no AuthSig/AuthPub): a legacy peer — adopt as before; the
//     authenticated bus remains the trust boundary for them.
//   - Signed by the task's origin (chain[0]) with a key matching the hello-
//     recorded directory entry and a valid signature: adopt and persist the
//     grant so later hops re-emit it.
//   - Signed but wrong: the claimed origin's recorded key does not match, or
//     the signature does not verify. Fail closed — consent is dropped, never
//     laundered: a relay that could mint "authorized" for another node's task
//     is exactly the hole this exists to close (P2-8).
//
// An origin whose key is not in the directory (never helloed this node, or a
// pre-key peer relayed across hops) cannot be verified; those degrade to the
// legacy flag path rather than breaking relayed consents the mesh cannot
// prove.
func (c *Core) consentGrantValid(p bus.TaskDelegatePayload, sender string) bool {
	if p.AuthSig == "" || p.AuthPub == "" || p.AuthTs == 0 {
		return true // legacy unsigned consent
	}
	origin := sender
	if len(p.Chain) > 0 {
		origin = p.Chain[0]
	}
	if origin == "" {
		return false
	}
	stored, ok := c.peerPubKey(origin)
	if !ok {
		return true // origin key unknown: unverifiable, degrade to legacy
	}
	presented, err := hex.DecodeString(p.AuthPub)
	if err != nil || !bytes.Equal(presented, stored) {
		return false
	}
	return bus.VerifyAuthorization(stored, p.TaskID, p.Authorized, p.AuthTs, p.AuthSig)
}
