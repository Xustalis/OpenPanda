// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/version"
	"github.com/gorilla/websocket"
	"net"
	"strconv"
	"time"
)

// helloCaps is the capability list this node advertises. sessaead rides
// only when a shared secret exists — without one there is no key material
// to seal frames with, so advertising it would be a lie. e2et rides only
// when the X25519 half of this node's identity materialized: transit
// sealing needs the peer's x_pub, and the cap says "frames sealed to mine
// open here". trnz tracks network.transit — relaying a transit frame for an
// authenticated member is baseline mesh citizenship, but an operator who
// switched the plane off must not be handed frames this node will drop.
// trust.relay then narrows WHOSE frames are served and at what rate.
func (c *Core) helloCaps() []string {
	caps := []string{bus.CapBinaryData, bus.CapObjectives, bus.CapActivities, bus.CapCoordination}
	// trnz rides only when this node is willing to serve the plane: a node
	// with network.transit:false must not be selected as a hop, or every
	// frame routed through it dies at the unhandled-type gate.
	if c.netPolicy().transit {
		caps = append(caps, bus.CapTransitRelay)
	}
	if c.sharedSecretValue() != "" {
		caps = append(caps, bus.CapSessionAEAD)
	}
	if _, _, ok := c.nodeXKey(); ok {
		caps = append(caps, bus.CapTransitE2E)
	}
	return caps
}

// wireProvenance is this build's provenance stamp as it goes on the wire —
// version.ProvenanceClass keeps "unsigned" explicit so a dev build is
// distinguishable from a pre-provenance peer that sends nothing, and a
// stale "self" stamp never masks a swap back to a local build.
func wireProvenance() string { return version.ProvenanceClass() }

// sanitizePeerProv whitelists an advertised prov before it is stored or
// weighed: the field is peer-supplied, so anything outside the defined
// classes folds to "" (unknown) rather than accumulating free-form labels.
func sanitizePeerProv(prov string) string {
	switch prov {
	case version.ProvenanceRelease, version.ProvenanceSelf, version.ProvenanceUnsigned:
		return prov
	}
	return ""
}

// signHello attaches this node's Ed25519 identity to an outgoing hello when a
// node key exists: PubKey lets the receiver record a resolvable directory
// entry, EdSig proves the sender controls it. Peers that predate the fields
// ignore both; a node without a key sends neither and stays on the HMAC path.
func (c *Core) signHello(p *bus.HelloPayload, ts int64, nonce string) {
	pub, priv, ok := c.nodeKeyPair()
	if !ok {
		return
	}
	p.PubKey = hex.EncodeToString(pub)
	p.EdSig = bus.SignHelloEd(priv, p.NodeID, ts, nonce)
	if p.To != "" {
		p.EdSigB = bus.SignHelloEdB(priv, p.NodeID, p.To, ts, nonce)
	}
	if _, xpub, ok := c.nodeXKey(); ok {
		p.XPub = hex.EncodeToString(xpub.Bytes())
	}
}

// ErrAuthRejected wraps the reason a peer sent in hello_reject: the link
// itself worked but our credential did not. maintainPeerLoop treats it like
// any dial failure (exponential backoff, sparse logging) — the distinct
// error keeps the log line saying "rejected", not "unreachable".
var ErrAuthRejected = errors.New("peer rejected our hello")

// ErrHelloBinding is the reject reason "binding": the peer has verified a
// bound hello from us before and now refuses the unbound form. That is not a
// secret mismatch — the remedy is to run a build that binds, or to clear the
// peer (`nodes remove`, re-pair) if it was deliberately rolled back. It wraps
// ErrAuthRejected so the backoff and custody behavior stay identical.
var ErrHelloBinding = fmt.Errorf("%w: binding", ErrAuthRejected)

// rejectHello answers a failed hello with hello_reject so the dialer learns
// it was refused — not merely disconnected — then drops the conn. The frame
// rides the still-unauthenticated leg deliberately: it carries a fixed reason
// code only, nothing derived from the secret. A peer old enough not to know
// the type still sees the close, which is all it ever saw; and the close
// frame carries the same reason for operators watching at the WS layer.
func (c *Core) rejectHello(conn *bus.Conn, reason string) {
	msgID, err := newUUID()
	if err == nil {
		if env, err := bus.NewEnvelope(bus.MsgHelloReject, c.nodeID, msgID,
			bus.HelloRejectPayload{Reason: reason}); err == nil {
			// Send reports the real socket write (the QoS writer is
			// synchronous), so the reason is on the wire before Close.
			_ = conn.Send(env)
		}
	}
	conn.CloseReason(4001, "hello rejected: "+reason)
}

// helloRejectHint keeps the operator's next action tied to the actual
// verdict. Only "auth" says anything about the shared secret: pointing at a
// secret mismatch for an identity or policy refusal sends two nodes that
// already agree straight into a re-pair, while the real cause (a self-dial,
// a duplicate node id, a stale directory row) stays hidden.
func helloRejectHint(reason string) string {
	if reason == "auth" {
		return "the peer's network.shared_secret differs (or its clock is more than 5m off); compare both nodes' secret_fp, then re-pair with `panda pair --secret <copied from the inviter's config.yaml> --peer <inviter host:port>`. Also verify both nodes run the same panda version: a much older build can fail the handshake for protocol reasons even with matching secrets"
	}
	return "not a credential problem; check for a duplicate node id, a self-dial or a stale directory row"
}

// handleHelloReject records a peer's refusal verdict on our conn and closes
// it. The conn carries the reason for MaintainPeer; the core-level marker
// gives one-shot probes (ProbePeer) the same signal without a conn handle.
func (c *Core) handleHelloReject(ctx context.Context, conn *bus.Conn, env bus.Envelope) {
	var p bus.HelloRejectPayload
	if err := env.PayloadInto(&p); err != nil || p.Reason == "" {
		c.logger.Debug("hello_reject without reason", "from", env.From)
	}
	reason := p.Reason
	if reason == "" {
		reason = "unknown"
	}
	// The marker and the log are conn-independent; only the per-conn verdict
	// needs a conn. A nil conn should be unreachable (the UDP plane drops
	// hello frames before dispatch), but a handler that panics on transport
	// shape is a remote kill switch, so fail soft.
	if conn != nil {
		conn.SetRejectReason(reason)
	}
	c.lastHelloReject.Store(reason)
	c.logger.Warn("peer rejected our hello", "peer", env.From, "reason", reason,
		"local_secret_fp", c.SecretFingerprint(), "hint", helloRejectHint(reason))
	if conn != nil {
		conn.Close()
	}
}

func (c *Core) handleHello(ctx context.Context, conn *bus.Conn, env bus.Envelope) {
	var p bus.HelloPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad hello", "err", err)
		return
	}
	// Verify the transport signature before trusting the claimed identity
	// (docs/design/routing-and-scheduling.md). Fail closed: an unauthenticated or stale hello
	// registers nothing and receives no reply — but it DOES receive a
	// hello_reject first, so the dialer can tell "my credential is wrong"
	// from "the network dropped us". Our own id goes in so a hello bound to
	// some other node — the shape a captured hello takes when a member
	// replays it here — fails on the address, not just on the signature.
	if secret := c.sharedSecretValue(); !bus.VerifyHelloP(secret, c.nodeID, p, time.Now()) {
		args := []any{
			"peer", p.NodeID,
			"reason", helloFailureReason(secret, c.nodeID, p, time.Now()),
			"local_secret_fp", MeshSecretFingerprint(secret),
			"peer_ver", p.Ver,
			"local_ver", version.Version,
			"peer_ts", p.Ts,
			"skew_seconds", int64(time.Since(time.Unix(p.Ts, 0)).Seconds()),
			"hint", "both nodes must hold the same network.shared_secret; a restart that found an empty secret mints a fresh in-memory one (see cmd/panda/netsetup.go)",
		}
		if vh := helloVersionHint(p.Ver, version.Version); vh != "" {
			args = append(args, "version_hint", vh)
		}
		c.logger.Warn("rejected hello: bad signature", args...)
		c.rejectHello(conn, "auth")
		return
	}
	if p.Nonce == "" {
		// Pre-nonce peer: the two-field HMAC verified, but a same-second
		// reconnect of theirs will collide in helloSeen. Worth knowing which
		// peers still run the old form before the fallback ever retires.
		c.logger.Debug("hello from legacy (nonce-less) peer", "peer", p.NodeID)
	}
	// The claimed identity must match the envelope's from field; both become the
	// identity bound to this conn, so later messages may only carry this id.
	// Checked before the replay claim below: a forged-from hello that burned a
	// valid sig into helloSeen would then keep the honest sender's real hello —
	// the same signature — out until the window closed.
	if env.From != p.NodeID {
		c.logger.Warn("rejected hello: from mismatch", "from", env.From, "peer", p.NodeID)
		c.rejectHello(conn, "auth")
		return
	}
	// A peer may not claim OUR identity. "nodeID == ours" is not another
	// member, it is this node: accepting it would bind a foreign conn under
	// our own id, and the registry would then route traffic "to us" out of
	// the building (and overwrite the self rows the directory keeps).
	if p.NodeID == c.nodeID {
		c.logger.Warn("rejected hello: peer claims local node id", "peer", p.NodeID)
		c.rejectHello(conn, "identity")
		return
	}
	// Identity pinning. Once a node id has a key on record, a hello must prove
	// that key: presenting a different one, or omitting the Ed25519 block
	// entirely, is rejected. The omission half is load-bearing — VerifyHelloP
	// still accepts an identity-less hello as a legacy peer for a first
	// contact, but every peer holds the mesh secret that signs the HMAC, so a
	// member could otherwise claim a known node id with no key at all, inherit
	// that node's trust zone and pinned-key decisions, and mint work as it.
	// A genuine re-key (reinstall) is an operator action: clear the old row
	// with `nodes remove` / re-pair, which is also what a human-verified key
	// already required.
	if stored, ok := c.peerPubKey(p.NodeID); ok && len(stored) > 0 {
		want := hex.EncodeToString(stored)
		if p.PubKey != want {
			c.logger.Warn("rejected hello: identity key absent or changed for known node",
				"peer", p.NodeID, "presented", p.PubKey != "")
			c.rejectHello(conn, "identity")
			return
		}
	}
	// Replay check (M24): within MaxHelloAge a captured hello still verifies,
	// so a signature must be single-use — accept each one once, reject it
	// until its freshness window closes. The key is the signed fields only:
	// anything unsigned (msg_id, nonce-tampered variants) a replay can
	// rewrite, so keying on it would let the same signature authenticate
	// twice. Uniqueness for honest dials comes from HelloSigN's per-dial
	// nonce — two same-second reconnects mint different signatures, so the
	// second is admitted on its own key rather than rejected as a replay. A
	// peer old enough to send no nonce gets the two-field verification and a
	// same-second reconnect of theirs is rejected once, which its backoff
	// retry clears.
	c.mu.Lock()
	// The signature that actually authenticated this hello: the bound one for
	// a hello addressed to us, the legacy one otherwise. Keying on p.Sig alone
	// would let a bound hello, whose legacy Sig may be anything, share a key
	// with an unrelated legacy hello.
	authSig := p.Sig
	if p.To != "" {
		authSig = p.SigB
	}
	key := p.NodeID + "|" + strconv.FormatInt(p.Ts, 10) + "|" + authSig
	if _, dup := c.helloSeen[key]; dup {
		c.mu.Unlock()
		c.logger.Warn("rejected hello: replayed signature", "peer", p.NodeID)
		c.rejectHello(conn, "replay")
		return
	}
	now := time.Now()
	c.helloSeen[key] = now
	// Prune entries whose replay window has closed: a signature older than
	// MaxHelloAge would fail VerifyHello anyway, so remembering it buys nothing
	// and the map would otherwise grow one entry per reconnect forever.
	for k, t := range c.helloSeen {
		if now.Sub(t) > bus.MaxHelloAge {
			delete(c.helloSeen, k)
		}
	}
	c.mu.Unlock()
	// Downgrade guard: a node that has bound its hello before must keep
	// binding. Without this, a member could take a captured bound hello,
	// strip To/SigB/EdSigB, and replay the still-valid legacy signature at
	// any peer — the binding would only defeat an attacker too careless to
	// edit the payload. The record is written after a bound hello
	// authenticates (see below), so a peer that has never bound, including
	// one still running a pre-binding build, is unaffected.
	//
	// Only INBOUND conns count. The hello on an outbound conn is the peer's
	// reply, which this side always binds, so recording from it would claim
	// the peer binds its own dials too — and then refuse it the first time it
	// dials us from an address it has not learned yet (the mutual-dial storm
	// this package has tests for). What the record means is narrow and true:
	// this peer has dialed us with a binding before.
	if p.To == "" && c.db != nil && !conn.Outbound() && !ledger.SessionRowID(p.NodeID) {
		if bound, err := ledger.HelloBindingKnown(c.db, p.NodeID); err == nil && bound {
			c.logger.Warn("rejected hello: node that binds its hello sent an unbound one", "peer", p.NodeID)
			c.rejectHello(conn, "binding")
			return
		}
	}
	conn.SetPeerID(p.NodeID)
	// Wire-feature negotiation: the peer's advertised caps decide whether
	// data-plane envelopes may leave as binary frames. Old peers send no
	// list and get the JSON/base64 form forever — capability probing via an
	// explicit bit, never a version parse.
	conn.SetCaps(p.Caps)
	conn.SetRemoteNonce(p.Nonce)

	// Send our hello reply BEFORE registering the conn, and directly on the
	// conn this hello arrived on — never via the registry (c.reply). The far
	// end of THIS conn is the read loop waiting to bind our identity, and
	// the registry is a moving target under mutual dials: if the tie-break
	// swaps this peer's registry entry between the greeted check and a
	// registry-routed send, the reply lands on the surviving conn while
	// this one dies hello-less — the losing dialer never learns the edge is
	// alive through its inbound conn, its MaintainPeer returns immediately
	// (PeerID was never bound), and the caller redials into the one-second
	// connect/disconnect flap the dedup exists to stop. Replying before
	// ensurePeer also means a concurrent replacement cannot close this conn
	// out from under the reply. Greeting is tracked per CONN, so every new
	// conn — a reconnect, a replacement, a tie-break loser — gets exactly
	// one identity-binding reply, while a repeat hello on an already-greeted
	// conn gets none (handshake termination).
	c.mu.Lock()
	already := c.greetedConns[conn]
	c.greetedConns[conn] = true
	c.mu.Unlock()
	if !already {
		c.sendHelloReply(conn, p.NodeID)
	}

	// Session encryption or fail-closed cleartext policy: with sessaead
	// negotiated the cipher arms here — before the conn is registered, so
	// no task frame can ever leave it in plaintext. Without it the link is
	// plaintext and must pass the same gate the pre-dial check used to be.
	if err := c.armSessionLink(conn, p); err != nil {
		conn.CloseReason(websocket.ClosePolicyViolation, err.Error())
		return
	}

	// Bind the configured dial address to the id it resolved to, BEFORE the
	// mutual-dial tie-break below. `panda status` runs in its own process and
	// cannot ask this daemon which configured peers are up; this row is how
	// the mesh line maps the operator's configured list onto live directory
	// ids. The binding must survive the dedup loser: when the surviving conn
	// is the PEER's inbound one, this side's outbound loses the tie-break
	// and closes — writing the binding only on the winner left a healthy
	// configured edge reading 0/1 forever. Only outbound conns carry a dial
	// address; an inbound hello names no address of ours to compare.
	if conn.Outbound() && !ledger.SessionRowID(p.NodeID) {
		if err := ledger.RecordPeerAddr(c.db, NormalizeDialAddr(conn.DialAddr()), p.NodeID); err != nil {
			c.logger.Debug("record peer addr binding", "peer", p.NodeID, "err", err)
		}
	}
	// Remember that this peer binds the hello it dials with: from here on an
	// unbound hello from it on an inbound conn is refused (the downgrade
	// guard above). Only a hello that authenticated and reached this point
	// counts, and only the inbound direction — an outbound conn's hello is
	// the peer's reply, which this side always binds, so it says nothing
	// about how the peer dials.
	if p.To != "" && !conn.Outbound() && !ledger.SessionRowID(p.NodeID) {
		if err := ledger.RecordHelloBinding(c.db, p.NodeID); err != nil {
			c.logger.Debug("record hello binding", "peer", p.NodeID, "err", err)
		}
	}
	// The verified reply is itself the reachability proof a probe reports on:
	// record it before the tie-break below, which may close this conn right
	// after (the probe's registry entry then gets cleaned up with it).
	c.lastVerifiedHello.Store(probeHello{id: p.NodeID, encrypted: conn.Encrypted()})

	// The authority that ranks competing conns is the one of the side whose
	// process they belong to. An inbound conn was dialed by the REMOTE
	// process, so its hello's claim (p.Authoritative) is the ranking input.
	// An outbound conn is our own dial — the competing conns on this side are
	// our own, so the input is our own claim, and what was stored at each
	// registration is our claim then. Both ends then agree: an authoritative
	// process that redials while its own older, non-authoritative conn is
	// still live promotes the new conn on BOTH sides, instead of the peer
	// promoting it while this side rejects-and-closes it as a sibling loser
	// and kills the edge it just won.
	auth := p.Authoritative
	if conn.Outbound() {
		auth = c.edgeAuthority.Load()
	}
	accepted := c.ensurePeer(p.NodeID, conn, auth)
	if !accepted {
		// Lost the mutual-dial tie-break: the reply above already left on
		// this conn, so the losing dialer bound our identity and its
		// MaintainPeer will hold the edge through its inbound conn instead
		// of redialing. The surviving registration already ingested the
		// card; drop the loser.
		conn.Close()
		return
	}
	// Ingest the peer's advertised capability card so routing can consider it.
	// A session-shaped id (name-8hex) is a client seat dialing in, not a fleet
	// node: writing its card would accumulate one ghost row per session.
	if len(p.Card) > 0 && !ledger.SessionRowID(p.NodeID) {
		var sum ledger.CapabilitySummary
		if err := json.Unmarshal(p.Card, &sum); err != nil {
			c.logger.Warn("bad capability card in hello", "peer", p.NodeID, "err", err)
		} else if err := ledger.UpsertRemote(c.db, p.NodeID, sanitizeSummary(sum)); err != nil {
			c.logger.Warn("upsert remote card", "peer", p.NodeID, "err", err)
		}
	}
	// The hello is the first version stamp — heartbeats then keep it fresh.
	if err := ledger.SetNodeVerIfChanged(c.db, p.NodeID, p.Ver); err != nil {
		c.logger.Warn("stamp peer version", "peer", p.NodeID, "err", err)
	}
	// Same stamp for build provenance — self-asserted, so it is whitelisted
	// to the defined classes before it lands on the row.
	if err := ledger.SetNodeProvIfChanged(c.db, p.NodeID, sanitizePeerProv(p.Prov)); err != nil {
		c.logger.Warn("stamp peer provenance", "peer", p.NodeID, "err", err)
	}
	// Record PubKey only when the EdSig actually verified it — a hello that
	// passed on the HMAC fallback never proved control of the key it claims,
	// and recording it anyway would let any peer poison this node's entry in
	// the directory (then mint grants "from" it). Signed content downstream —
	// consent grants, artifact handoffs — verifies against this entry rather
	// than a key the payload presents, so what lands here must be proven.
	if p.PubKey != "" && p.EdSig != "" {
		if pub, err := hex.DecodeString(p.PubKey); err == nil &&
			bus.VerifyHelloEd(pub, p.NodeID, p.Ts, p.Nonce, p.EdSig) {
			// x_pub rides inside the same EdSig-verified hello: the sig
			// covers only NodeID:Ts:Nonce, but recording happens only after
			// the claimed Ed25519 identity proved itself — so the transit
			// seal's destination key inherits the same provenance. It is
			// stored inside the same upsert as pub_key (recordPeerPubKey)
			// because the binding between them is the security property:
			// two same-id hellos racing on separate conns must never tear
			// one hello's key into another's row.
			c.recordPeerPubKey(ctx, p.NodeID, p.PubKey, p.XPub)
		}
	}
	// Farsky reachability hints: the peer's datagram port plus the IP its
	// conn arrived from give us the punch candidate <observed>:<port>; the
	// reply's You teaches us our own public IP for the offers we send.
	observed := ""
	if host, _, err := net.SplitHostPort(conn.RemoteAddr()); err == nil {
		observed = host
	}
	c.learnPeerUDP(p.NodeID, p.UDPPort, nil, observed)
	if p.You != "" {
		c.udpMu.Lock()
		c.observedIP = p.You
		c.udpMu.Unlock()
	}
	c.logger.Info("peer hello", "peer", p.NodeID, "ver", p.Ver)
	// A return channel to this peer exists again: redeliver any terminal
	// results parked while it was disconnected (review P0-2). This is the
	// reconciliation point that keeps the two ends' task histories consistent
	// across a disconnect.
	c.outboxFlush(ctx, p.NodeID)
	c.goGuarded("activity reconnect catchup", func() { c.advertisePeerActivities(ctx, p.NodeID) })
}

// sendHelloReply transmits this node's hello on conn so the far end can bind
// our identity. Direct send on the given conn, never registry-routed — see
// handleHello for why the handshake reply must follow the conn it answers.
func (c *Core) sendHelloReply(conn *bus.Conn, to string) {
	card, err := c.helloCard()
	if err != nil {
		return
	}
	ts := time.Now().Unix()
	msgID, err := newUUID()
	if err != nil {
		return
	}
	nonce, err := newUUID()
	if err != nil {
		return
	}
	// The reply carries what the far end needs to reach us without TCP:
	// our datagram port for punching, and You — the IP its own conn arrived
	// from, which for a NAT-bound peer is its public address (free reflexive
	// discovery, no STUN needed). You is transport-observed, not signed: a
	// forged one only buys the forger a wrong candidate list.
	observed := ""
	if host, _, err := net.SplitHostPort(conn.RemoteAddr()); err == nil {
		observed = host
	}
	// The reply is addressed back to the node whose hello we just verified,
	// so it can carry the bound signature unconditionally: the receiver is
	// known here even when the dialer could not know us by address.
	reply := bus.HelloPayload{
		NodeID:        c.nodeID,
		Ver:           version.Version,
		Prov:          wireProvenance(),
		Card:          card,
		Ts:            ts,
		Nonce:         nonce,
		Sig:           bus.HelloSigN(c.sharedSecretValue(), c.nodeID, ts, nonce),
		To:            to,
		UDPPort:       c.UDPPort(),
		You:           observed,
		Caps:          c.helloCaps(),
		Authoritative: c.edgeAuthority.Load(),
	}
	if to != "" {
		reply.SigB = bus.HelloSigB(c.sharedSecretValue(), c.nodeID, to, ts, nonce)
	}
	conn.SetLocalNonce(nonce)
	c.signHello(&reply, ts, nonce)
	envOut, err := bus.NewEnvelope(bus.MsgHello, c.nodeID, msgID, reply)
	if err != nil {
		return
	}
	envOut.To = to
	if err := conn.Send(envOut); err != nil {
		c.logger.Debug("hello reply failed", "peer", to, "err", err)
	}
}

// armSessionLink decides the conn's wire security after the verified hello
// exchange. When both hellos advertised sessaead and a shared secret
// exists, the two exchanged nonces key a per-conn AEAD and the link is
// sealed from this point on. Otherwise the conn stays plaintext and must
// pass the cleartext policy — the same gate that used to run pre-dial,
// now evaluated with knowledge of what the peer can actually do.
func (c *Core) armSessionLink(conn *bus.Conn, p bus.HelloPayload) error {
	if conn.Encrypted() {
		// A repeat hello on an armed conn never re-keys: the cipher guards
		// in-flight frames from a mid-stream key swap.
		return nil
	}
	secret := c.sharedSecretValue()
	if secret != "" && conn.RemoteNonce() != "" &&
		conn.LocalNonce() != "" && conn.Supports(bus.CapSessionAEAD) {
		var dialerNonce, listenerNonce, dialerID, listenerID string
		if conn.Outbound() {
			dialerNonce, listenerNonce = conn.LocalNonce(), conn.RemoteNonce()
			dialerID, listenerID = c.nodeID, p.NodeID
		} else {
			dialerNonce, listenerNonce = conn.RemoteNonce(), conn.LocalNonce()
			dialerID, listenerID = p.NodeID, c.nodeID
		}
		aead, err := bus.SessionAEAD(secret, dialerID, listenerID, dialerNonce, listenerNonce)
		if err != nil {
			return err
		}
		conn.ArmSession(aead)
		c.logger.Debug("session cipher armed", "peer", p.NodeID)
		return nil
	}
	return c.enforceLinkPolicy(conn, p.NodeID)
}
