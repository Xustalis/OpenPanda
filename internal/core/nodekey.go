// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/security"
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

// nodeXKeySetting is the X25519 half of the dual-key identity (cross-network
// mesh design §3): the signing key above attests WHO a node is, this
// agreement key is what transit frames seal TO. Separate key, separate job —
// a signing key never does ECDH, and this key never signs.
const nodeXKeySetting = "node_x25519_priv"

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
			c.nodePub, c.nodePriv = loadNodeKey(stored)
			if c.nodePriv == nil {
				c.logger.Warn("node key corrupt in settings")
			}
		}
		// X25519 half: same load-or-generate discipline as the Ed25519
		// half. It materializes inside the same once so an injected Ed25519
		// identity (ProbePeer) leaves BOTH fields frozen — a probe must not
		// advertise a throwaway x_pub the real daemon never agreed to.
		var storedX string
		xErr := c.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, nodeXKeySetting).Scan(&storedX)
		switch {
		case errors.Is(xErr, sql.ErrNoRows):
			priv, gerr := bus.GenerateX25519()
			if gerr != nil {
				c.logger.Warn("node x25519 key generate", "err", gerr)
				break
			}
			if _, werr := c.db.Exec(
				`INSERT INTO settings (key, value) VALUES (?, ?)`, nodeXKeySetting,
				hex.EncodeToString(priv.Bytes())); werr != nil {
				c.logger.Warn("node x25519 key persist", "err", werr)
				break
			}
			c.nodeXPriv = priv
		case xErr != nil:
			c.logger.Warn("node x25519 key load", "err", xErr)
		default:
			raw, derr := hex.DecodeString(storedX)
			if derr == nil {
				c.nodeXPriv, derr = bus.LoadX25519(raw)
			}
			if derr != nil {
				c.logger.Warn("node x25519 key corrupt in settings")
			}
		}
		if c.nodePub != nil {
			c.recordSelfPubKey(c.nodePub)
		}
		if c.nodeXPriv != nil {
			c.recordSelfXPub(c.nodeXPriv.PublicKey())
		}
	})
	if c.nodePriv == nil {
		return nil, nil, false
	}
	return c.nodePub, c.nodePriv, true
}

// nodeXKey returns this node's X25519 identity — the key transit frames seal
// to. It materializes inside nodeKeyPair's once, so callers trigger the same
// lazy path; a keyless node (no writable store) reports !ok and every
// transit send degrades to the member-level mesh seal.
func (c *Core) nodeXKey() (*ecdh.PrivateKey, *ecdh.PublicKey, bool) {
	if _, _, ok := c.nodeKeyPair(); !ok {
		return nil, nil, false
	}
	if c.nodeXPriv == nil {
		return nil, nil, false
	}
	return c.nodeXPriv, c.nodeXPriv.PublicKey(), true
}

// peerXPub resolves a peer's advertised X25519 key from the directory —
// recorded only after the hello's EdSig proved the claimed Ed25519 identity,
// so the key transit seals to is the one the peer's proven identity carried.
// A session id resolves through its host node's row, exactly like
// peerPubKey. Stable "k:<pub>" destinations resolve via the newest row
// holding that signing key. Returns !ok when the peer never published one
// (old version or keyless): the caller falls back to the mesh seal.
func (c *Core) peerXPub(ctx context.Context, nodeID string) (*ecdh.PublicKey, bool) {
	if c.db == nil || nodeID == "" {
		return nil, false
	}
	var hexKey string
	var err error
	if strings.HasPrefix(nodeID, stableIDPrefix) {
		err = c.db.QueryRowContext(ctx,
			`SELECT x_pub FROM employee_cache WHERE pub_key = ? AND x_pub != ''
			 ORDER BY (status = 'online') DESC, last_seen DESC LIMIT 1`,
			nodeID[len(stableIDPrefix):]).Scan(&hexKey)
	} else {
		err = c.db.QueryRowContext(ctx,
			`SELECT x_pub FROM employee_cache WHERE id = ?`, nodeID).Scan(&hexKey)
		if err != nil || hexKey == "" {
			// A session id seals to its host node's x key — the session
			// shares the host's keypair, so the host row's x_pub is the
			// right destination key (peerPubKey's base fallback).
			if base, ok := scheduler.EphemeralBase(nodeID); ok && base != nodeID {
				err = c.db.QueryRowContext(ctx,
					`SELECT x_pub FROM employee_cache WHERE id = ?`, base).Scan(&hexKey)
			}
		}
	}
	if err != nil || hexKey == "" {
		return nil, false
	}
	raw, derr := hex.DecodeString(hexKey)
	if derr != nil {
		return nil, false
	}
	pub, derr := bus.LoadX25519Pub(raw)
	if derr != nil {
		return nil, false
	}
	return pub, true
}

// recordSelfXPub stamps this node's X25519 key on its own directory row —
// the same write-once-at-materialize pattern as recordSelfPubKey, so local
// consumers (status, transit self-checks) read the self row the same way as
// a peer's.
func (c *Core) recordSelfXPub(pub *ecdh.PublicKey) {
	if _, err := c.db.Exec(
		`INSERT INTO employee_cache (id, x_pub, status, last_seen) VALUES (?, ?, 'offline', ?)
		 ON CONFLICT(id) DO UPDATE SET x_pub = excluded.x_pub`,
		c.nodeID, hex.EncodeToString(pub.Bytes()), time.Now().Unix()); err != nil {
		c.logger.Debug("record self x_pub", "err", err)
	}
}

// loadNodeKey decodes a stored settings value into the keypair, or nils on
// corruption. Split out so non-Core callers (the audit verifier, the CLI's
// task store) can reuse the exact format check.
func loadNodeKey(stored string) (ed25519.PublicKey, ed25519.PrivateKey) {
	raw, err := hex.DecodeString(stored)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, nil
	}
	priv := ed25519.PrivateKey(raw)
	return priv.Public().(ed25519.PublicKey), priv
}

// LoadNodeKey reads this node's persisted Ed25519 identity from an open
// store, without generating one (generation is the daemon's job). Callers
// that only verify — `panda audit verify`, the CLI task store — get !ok on a
// node that has never run a daemon rather than silently minting a key the
// mesh never advertised.
// NewSignedAudit returns an audit recorder bound to this node's Ed25519
// signing key when one already exists in settings — entries then land on the
// hash chain with the node's attestation and cannot be rewritten without
// breaking `panda audit verify`. Keyless installs record unsigned rows.
func NewSignedAudit(db *sql.DB) *security.Audit {
	a := security.NewAudit(db)
	if pub, priv, ok := LoadNodeKey(db); ok {
		a.SetSigner(pub, priv)
	}
	return a
}

func LoadNodeKey(db *sql.DB) (ed25519.PublicKey, ed25519.PrivateKey, bool) {
	var stored string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, nodeKeySetting).Scan(&stored); err != nil {
		return nil, nil, false
	}
	pub, priv := loadNodeKey(stored)
	return pub, priv, priv != nil
}

// peerPubKey resolves a peer's recorded Ed25519 key from the directory. The
// directory is populated from signed hellos (handleHello), so the returned key
// is the one the peer proved it controls — never a key pulled from the message
// being verified, which would let a signer attest with its own key under a
// victim's name.
func (c *Core) peerPubKey(nodeID string) (ed25519.PublicKey, bool) {
	var hexKey string
	err := c.db.QueryRow(
		`SELECT pub_key FROM employee_cache WHERE id = ?`, nodeID).Scan(&hexKey)
	if err != nil || hexKey == "" {
		// A session id signs with its host node's key, which lives on the
		// node's stable row — the session itself never gets a row.
		if base, ok := scheduler.EphemeralBase(nodeID); ok && base != nodeID {
			err = c.db.QueryRow(
				`SELECT pub_key FROM employee_cache WHERE id = ?`, base).Scan(&hexKey)
		}
	}
	if err != nil || hexKey == "" {
		return nil, false
	}
	raw, err := hex.DecodeString(hexKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, false
	}
	return ed25519.PublicKey(raw), true
}

// recordPeerPubKey stores the key a peer proved in its signed hello, and the
// X25519 transit key the same hello advertised — in ONE statement, because
// two hellos for the same node id can process concurrently on separate conn
// read loops (a mutual dial's loser, a stale listener answering first): two
// independent upserts could interleave into a torn row carrying one hello's
// pub_key beside another's x_pub, and the transit seal would then wrap to a
// key the pinned identity never advertised. A torn read fails closed (the
// blob just never opens), but atomicity keeps the row honest by
// construction. xPubHex is validated here and stored verbatim — a verified
// hello that advertises none clears the column, which is the honest
// degradation signal, not an error.
//
// The row is upserted rather than inserted-blind so a card-less peer still
// leaves a resolvable identity behind (UpsertRemote only runs when a card
// arrives). A bare-HMAC hello carries no key and updates nothing: the stored
// key keeps attesting what the last signed hello proved.
//
// TOFU rule: a key that CHANGES under an existing row clears key_verified —
// a reinstall, a key rotation and a MITM all look identical from here, and
// none of them may inherit the human's earlier check. The change is logged
// loudly: it is exactly the signal a silent overwrite would hide.
func (c *Core) recordPeerPubKey(ctx context.Context, nodeID, pubKeyHex, xPubHex string) {
	if pubKeyHex == "" || ledger.SessionRowID(nodeID) {
		// An ephemeral-suffix id is a client session, not a fleet member:
		// recording a row per session is exactly how ghost nodes pile up.
		// The session shares its node's keypair, so the node row's key —
		// reached via peerPubKey's ephemeral-base fallback — still verifies
		// anything the session signs.
		return
	}
	// An x_pub that cannot decode is treated as absent: the verified hello
	// carries it unsigned, so a malformed claim must not stick in the column
	// transit reads.
	if raw, err := hex.DecodeString(xPubHex); err != nil || len(raw) == 0 {
		xPubHex = ""
	} else if _, err := bus.LoadX25519Pub(raw); err != nil {
		xPubHex = ""
	}
	var existing string
	_ = c.db.QueryRowContext(ctx, `SELECT COALESCE(pub_key,'') FROM employee_cache WHERE id=?`, nodeID).Scan(&existing)
	if existing != "" && existing != pubKeyHex {
		c.logger.Warn("peer key changed — verification reset", "peer", nodeID,
			"old", existing[:min(16, len(existing))], "new", pubKeyHex[:min(16, len(pubKeyHex))])
	}
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO employee_cache (id, pub_key, x_pub, key_verified, status, last_seen) VALUES (?, ?, ?, 0, 'offline', 0)
		 ON CONFLICT(id) DO UPDATE SET pub_key = excluded.pub_key,
		     x_pub = excluded.x_pub,
		     key_verified = CASE WHEN employee_cache.pub_key != excluded.pub_key THEN 0
		         ELSE employee_cache.key_verified END,
		     trust = CASE WHEN employee_cache.pub_key != excluded.pub_key
		         AND employee_cache.trust = 'mesh' THEN 'guarded'
		         ELSE employee_cache.trust END`,
		nodeID, pubKeyHex, xPubHex); err != nil {
		c.logger.Warn("record peer pubkey", "peer", nodeID, "err", err)
		return
	}
	// The signature proving this key already passed (handleHello gates the
	// call), so sibling rows advertising the same key under another id are
	// provably stale — a rename, a reinstall, or an old session's residue.
	if stale, err := ledger.ReapKeySiblings(c.db, nodeID, pubKeyHex, c.nodeID); err != nil {
		c.logger.Warn("reap key siblings", "peer", nodeID, "err", err)
	} else if len(stale) > 0 {
		c.logger.Info("reaped stale rows sharing peer key", "peer", nodeID, "ids", stale)
	}
	// The datagram pair key is derived from this row, so a key that just
	// changed must not keep sealing to the old one: drop the cache entry and
	// let the next datagram re-derive (or fall back, if x_pub went empty).
	if k := c.udpKeySource(); k != nil {
		k.forget(nodeID)
	}
}

// udpKeySource returns the installed datagram pair-key cache, or nil while the
// plane is off.
func (c *Core) udpKeySource() *udpKeys {
	c.udpMu.Lock()
	defer c.udpMu.Unlock()
	return c.udpKeys
}

// recordSelfPubKey stamps this node's own key on its directory row, verified
// by construction: it is the keypair in this process's settings — no remote
// attestation to doubt. Called once when the keypair materializes so the
// fleet listing can show the local fingerprint the way `panda nodes` shows a
// peer's.
func (c *Core) recordSelfPubKey(pub ed25519.PublicKey) {
	hexKey := hex.EncodeToString(pub)
	now := time.Now().Unix()
	if _, err := c.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, key_verified, status, last_seen) VALUES (?, ?, ?, 'offline', ?)
		 ON CONFLICT(id) DO UPDATE SET pub_key = excluded.pub_key,
		     key_verified = excluded.key_verified`,
		c.nodeID, hexKey, now, now); err != nil {
		c.logger.Warn("record self pubkey", "err", err)
	}
}

// EnsureNodeKey materializes the identity keypair eagerly — daemon startup
// calls it so a node that has never dialed anyone still carries its own
// fingerprint in the directory. Without it, `panda nodes` would show "—" on
// the self row of a peerless node, which is exactly the row the operator
// compares a discovery fingerprint against.
func (c *Core) EnsureNodeKey() {
	c.nodeKeyPair()
}

// signConsentGrant attaches this node's Ed25519 signature over
// (taskID, authorized, ts, ConsentDigest) to an outgoing delegate — the
// tamper-proof form of the Authorized flag, bound to what the task actually
// is rather than just that it was consented (P2-8). Called only where consent
// is freshly minted: relayed payloads already carry the origin's grant and
// must forward it verbatim rather than re-signing under a key the origin
// never used.
func (c *Core) signConsentGrant(p *bus.TaskDelegatePayload) {
	if !p.Authorized {
		return
	}
	pub, priv, ok := c.nodeKeyPair()
	if !ok {
		return
	}
	p.AuthTs = time.Now().Unix()
	p.AuthSig = bus.SignAuthorization(priv, p.TaskID, p.Authorized, p.AuthTs, p.ConsentDigest())
	p.AuthPub = hex.EncodeToString(pub)
}

// grantConsentFor mints or re-emits the consent grant on an outbound
// delegate. The v2 digest binds the FINAL payload — target node, inputs,
// project pack — so the origin must sign after everything is attached: a
// signature minted before the attaches (or a grant persisted across a
// payload rebuild) is one the receiver must reject, which is exactly how
// authorized tasks with a project silently degraded to restricted runs. A
// relay never re-signs: it forwards the origin's stored grant verbatim,
// because only the origin's key can mint consent for its task (P2-8).
func (c *Core) grantConsentFor(t Task, p *bus.TaskDelegatePayload) {
	if !p.Authorized {
		return
	}
	if len(t.Chain) == 0 || scheduler.SameRuntimeIdentity(t.Chain[0], c.nodeID) {
		c.signConsentGrant(p)
		return
	}
	p.AuthSig, p.AuthPub, p.AuthTs = t.AuthSig, t.AuthPub, t.AuthTs
}

// consentGrantValid decides whether the Authorized flag on an inbound delegate
// may be adopted. The grant is a single object: a payload that carries some
// but not all of (AuthSig, AuthPub, AuthTs) is a torn grant — fields stripped
// in transit — and fails closed rather than degrading to "legacy". The
// surviving shapes:
//
//   - Fully unsigned AND the origin has no key in the directory: a genuinely
//     legacy peer — adopt; the authenticated bus is still their boundary.
//   - Fully unsigned but the origin's key IS recorded: the origin is
//     key-capable, so a bare flag is a stripped grant — refuse. Otherwise a
//     relay could launder any signed consent back to unsigned by deleting
//     three fields.
//   - Signed by the task's origin (chain[0]) with a key matching the hello-
//     recorded directory entry and a signature that verifies over the task's
//     ConsentDigest: adopt and persist the grant so later hops re-emit it.
//   - Signed but wrong: claimed origin's recorded key does not match, or the
//     signature does not verify over the received content. Fail closed —
//     consent is dropped, never laundered: a relay that could mint
//     "authorized" for another node's task is exactly the hole this exists
//     to close (P2-8).
//
// A signed grant whose origin is unknown to the directory (never helloed this
// node, or a pre-key peer relayed across hops) cannot be verified; it degrades
// to the legacy flag path rather than breaking relayed consents the mesh
// cannot prove.
func (c *Core) consentGrantValid(p bus.TaskDelegatePayload, sender string) bool {
	anyAuth := p.AuthSig != "" || p.AuthPub != "" || p.AuthTs != 0
	signed := p.AuthSig != "" && p.AuthPub != "" && p.AuthTs != 0
	if anyAuth && !signed {
		return false // torn grant: the block was written, then partly erased
	}
	origin := sender
	if len(p.Chain) > 0 {
		origin = p.Chain[0]
	}
	if origin == "" {
		return false
	}
	stored, ok := c.peerPubKey(origin)
	if !anyAuth {
		return !ok // bare flag is legacy only from a genuinely key-less origin
	}
	if !ok {
		// Signed, but the claimed origin's key is not in the directory —
		// typically a multi-hop relay whose origin this node never helloed.
		// Adoption keeps relayed consents working (a compatibility choice:
		// refusing would stall every relayed tier-2 task), but it is also
		// the one path where a mesh member can assert another node's
		// consent unverified, so surface it loudly for the operator.
		c.logger.Warn("adopting consent grant with unverifiable origin key",
			"task", p.TaskID, "origin", origin, "sender", sender)
		return true
	}
	presented, err := hex.DecodeString(p.AuthPub)
	if err != nil || !bytes.Equal(presented, stored) {
		return false
	}
	return bus.VerifyAuthorization(stored, p.TaskID, p.Authorized, p.AuthTs, p.ConsentDigest(), p.AuthSig)
}
