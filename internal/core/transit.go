// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// Blind transit relay — cross-network mesh design M3 / §3. When sendTo has
// no live track to a peer, the envelope is sealed end-to-end (the peer's
// X25519 identity, or the member-level mesh seal for a peer that published
// none) and carried inside a transit frame across ordinary member conns.
// Relays read only the outer shell — destination stable key, TTL, seal
// level, byte size — never the content. The destination opens the blob,
// verifies the sender's Ed25519 attestation of the inner envelope, and
// dispatches it as if it had arrived directly.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

// transitMaxInner bounds the sealed body a transit frame may carry. It must
// hold one artifact_chunk: 1 MiB of bytes is ~1.4 MiB of JSON, which the
// transitInner envelope base64s again to ~1.86 MiB before sealing. 2 MiB
// leaves room for the envelope while keeping the outer frame under the
// transport's 4 MiB read limit (the wire base64s the sealed blob once more).
// A relay buffers one frame of this size per session; larger single frames
// are refused rather than split here — the artifact plane already chunks at
// bus.ArtifactChunkBytes, so a bigger artifact simply takes more frames.
const transitMaxInner = 2 << 20

// transitInner is the sealed body: the original envelope verbatim plus the
// sender's mandatory attestation. Pub identifies the signing identity; Sig
// covers TransitInnerMsg(Env) so a relay cannot rewrite a single byte of the
// frame it ferries — From, MsgID, To and the payload are all inside the
// signature. Both fields are required: an inner that cannot prove who wrote
// it never reaches dispatch.
type transitInner struct {
	Env json.RawMessage `json:"env"`
	Pub string          `json:"pub,omitempty"` // sender Ed25519 public key hex
	Sig string          `json:"sig,omitempty"` // Ed25519 over TransitInnerMsg(Env)
}

// errTransitUnsupported marks frame types that cannot ride transit: anything
// whose handler binds to the conn it arrived on. These are rejected before
// sealing, never dispatched at the far end.
var errTransitUnsupported = errors.New("transit: frame type requires a live conn")

// errTransitTooLarge rejects a frame at the SENDER before it enters a relay.
// The receiver enforces the same transitMaxInner ceiling; returning success
// here for a blob the far end must drop would skip the caller's outbox
// parking and silently lose the message.
var errTransitTooLarge = errors.New("transit: frame exceeds the relay size budget")

// errTransitUnattested marks a sender without an Ed25519 identity. Transit
// has no conn-bound From to fall back on, so an unsigned inner is not
// dispatched at the destination; fail at the sender so the caller parks the
// frame instead of writing it into a relay that must discard it.
var errTransitUnattested = errors.New("transit: sender has no Ed25519 identity")

// errTransitDisabled is network.transit: false. The node still reads transit
// frames addressed to it (a peer may route through someone else to reach
// us), but it never originates one — the client half of the plane is the
// part the operator switched off.
var errTransitDisabled = errors.New("transit: disabled by network.transit")

// transitEligible reports whether a frame type may be sealed into a transit
// inner. Hello and the pair exchange are conn-bound by definition — a hello
// arriving over transit could not bind its peer id to any conn, and pair
// frames over transit need the unauthenticated-conn machinery of P4b, which
// this file deliberately does not implement. A nested transit frame is
// allowed: TTL bounds each layer independently.
func transitEligible(typ string) bool {
	switch typ {
	case bus.MsgHello, bus.MsgHelloReject:
		return false
	// Pair frames DO ride transit (P4b): a joiner behind a symmetric NAT can
	// only reach the introducer through a contact point, and the pairing
	// ceremony was designed for a hostile channel from the start — the SAS
	// is the authentication, the DH box carries the secret. They are sealed
	// "none" (a joiner holds no shared secret and does not know the
	// introducer's X25519 key) and that exemption is enforced type-by-type
	// in deliverTransit and deliverToAttached.
	// The artifact plane rides transit as request/response: the fetch names
	// its source, the chunk comes back to the fetch's From. Both inner
	// envelopes keep their own From/To, so the reply reaches the requester
	// even though the carrier hop was rewritten at every relay. The size
	// budget below is what makes a 1 MiB chunk fit (its JSON is base64'd
	// twice on the way out); a larger artifact simply takes more chunks.
	// Push is deliberately absent: it is unsolicited, so a relayed push
	// would have no authenticated fetch to bind its bytes to.
	case bus.MsgArtifactPush, bus.MsgArtifactPushStatus, bus.MsgArtifactPushDone:
		return false
	}
	return true
}

// sendViaTransit is the reachability fallback behind sendTo: seal env for
// the destination's identity and hand it to the best relay hop this node
// can reach. The destination key prefers the stable "k:<pub>" form so the
// frame survives the peer's restart mid-flight; a keyless peer keeps its
// instance id and resolves the same way as an outbox row.
func (c *Core) sendViaTransit(ctx context.Context, id string, env bus.Envelope) error {
	if !transitEligible(env.Type) {
		return errTransitUnsupported
	}
	if !c.netPolicy().transit {
		return errTransitDisabled
	}
	if isPairMessage(env.Type) {
		// Pair frames reach transit only through the contact-point proxy
		// (pairtransit.go), which owns the return binding. The generic
		// fallback would seal them to an identity the joiner does not hold
		// and has no way to route the answer back.
		return errTransitUnsupported
	}
	target := env.To
	if target == "" {
		target = id
	}
	// A session id seals to its host: the session is not a dialable peer,
	// but its host opens the blob and dispatches the inner env, whose own
	// To still names the session.
	if base, ok := scheduler.EphemeralBase(target); ok {
		target = base
	}
	if scheduler.SameRuntimeIdentity(target, c.nodeID) {
		return errors.New("transit: destination is self")
	}
	dest := c.stablePeerID(ctx, target)
	if dest == "" {
		dest = target
	}
	// marshalEnvelope rather than json.Marshal: an artifact chunk's payload is
	// already compact JSON, and the stock marshal would scan and copy its
	// 1.4 MiB base64 body a second time. Transit is exactly where that cost
	// lands, because chunks are the one bulk payload it carries.
	envJSON, err := bus.MarshalEnvelope(env)
	if err != nil {
		return err
	}
	// Check the plaintext before sealing so an oversized result cannot make
	// the sender burn a full AEAD allocation only to reject it below. The
	// receiver holds Inner to the same ceiling.
	if len(envJSON) > transitMaxInner {
		return fmt.Errorf("%w: inner envelope is %d bytes, limit %d",
			errTransitTooLarge, len(envJSON), transitMaxInner)
	}
	pub, priv, ok := c.nodeKeyPair()
	if !ok {
		return errTransitUnattested
	}
	inner := transitInner{
		Env: envJSON,
		Pub: hex.EncodeToString(pub),
		Sig: bus.SignTransitInner(priv, envJSON),
	}
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		return err
	}
	// Prefer end-to-end: the destination's advertised X25519 key. A peer
	// that never published one falls back to the member-level mesh seal —
	// honest degradation, logged and audited as transit_degraded rather
	// than silently claimed as sealed.
	var blob []byte
	seal := bus.SealX25519
	if xpub, ok := c.peerXPub(ctx, target); ok {
		if blob, err = bus.TransitSealX25519(xpub, innerJSON); err != nil {
			return err
		}
	} else {
		seal = bus.SealMesh
		if blob, err = bus.TransitSealMesh(c.sharedSecretValue(), dest, innerJSON); err != nil {
			return err
		}
		c.audit(ctx, "", "transit_degraded", dest, "mesh-seal",
			"peer published no x_pub; inner sealed at member level")
	}
	// AEAD overhead/nonce/key prefix can push a borderline plaintext over
	// the receiver's ceiling. Enforce the limit on the exact blob that would
	// cross the wire, not just on its plaintext.
	if len(blob) > transitMaxInner {
		return fmt.Errorf("%w: sealed blob is %d bytes, limit %d",
			errTransitTooLarge, len(blob), transitMaxInner)
	}
	tenv, err := bus.NewEnvelope(bus.MsgTransit, c.nodeID, mustUUID(env.MsgID), bus.TransitPayload{
		Dest:  dest,
		TTL:   bus.TransitMaxTTL,
		Seal:  seal,
		Inner: blob,
	})
	if err != nil {
		return err
	}
	tenv.To = dest
	hop := c.transitNextHop(ctx, dest, nil)
	if hop == "" {
		return errNoRoute
	}
	conn := c.connFor(hop)
	if conn == nil {
		return errNoRoute
	}
	if err := conn.Send(tenv); err != nil {
		return err
	}
	c.mesh.transitTxBytes.Add(int64(len(blob)))
	return nil
}

// mustUUID returns a fresh message id, or — on the once-in-forever entropy
// failure — rides the inner message's own id. Reusing the inner id is safe:
// the dedup map keys on (from, msg_id) and both scopes claim the same
// origin, so a collision behaves exactly like a duplicate, not a forgery.
func mustUUID(fallback string) string {
	if id, err := newUUID(); err == nil {
		return id
	}
	return fallback
}

// clampTransitTTL bounds an incoming hop budget to the protocol maximum.
// Negative values are left alone: a relay drops them at the <=1 gate, and a
// destination may still deliver a frame that spent its final hop.
func clampTransitTTL(ttl int) int {
	if ttl > bus.TransitMaxTTL {
		return bus.TransitMaxTTL
	}
	return ttl
}

// handleTransit is the relay/destination side of the frame. It inspects only
// the outer shell — Dest, TTL, Seal, size — which is the whole point of the
// design: a relay learns who the frame is for and nothing else.
func (c *Core) handleTransit(ctx context.Context, conn *bus.Conn, env bus.Envelope) {
	var p bus.TransitPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		c.logger.Debug("transit: malformed payload", "from", env.From, "err", err)
		return
	}
	if p.Dest == "" || len(p.Inner) == 0 || len(p.Inner) > transitMaxInner {
		c.logger.Debug("transit: bad shell", "from", env.From, "dest", p.Dest, "inner", len(p.Inner))
		return
	}
	if p.Seal != bus.SealX25519 && p.Seal != bus.SealMesh && p.Seal != bus.SealNone {
		c.logger.Debug("transit: unknown seal level", "from", env.From, "seal", p.Seal)
		return
	}
	// A sender-supplied TTL is an assertion, not a budget. Clamp it on
	// intake before any relay decision, or a frame could spend an arbitrarily
	// long life looping through the graph; msg-id dedup only catches a
	// revisit of the same carrier identity, and every hop rewrites From.
	if clamped := clampTransitTTL(p.TTL); clamped != p.TTL {
		c.logger.Debug("transit: ttl clamped", "from", env.From, "ttl", p.TTL)
		p.TTL = clamped
	}
	// Ours by identity first: a stable key naming this node is decidable
	// without the directory, and the directory may not know our own key yet
	// (the self row carries one only once the key has been stamped).
	if c.isSelfStable(p.Dest) {
		c.deliverTransit(ctx, env, p)
		return
	}
	destInst := c.instanceForStable(ctx, p.Dest)
	if destInst != "" && scheduler.SameRuntimeIdentity(destInst, c.nodeID) {
		c.deliverTransit(ctx, env, p)
		return
	}
	// Return path for pair-over-transit (P4b): the destination is a joiner
	// bound to this contact point on an unauthenticated conn. The frame is
	// handed over still sealed — or, for the pairing exemption, unsealed and
	// re-emitted as a plain pair frame, since the joiner speaks the ordinary
	// ceremony on its own conn.
	if ac := c.attClientFor(p.Dest); ac != nil {
		c.deliverToAttached(ctx, env, p, ac)
		return
	}
	// Not ours: relay. TTL is the loop bound under msg-id dedup — a frame
	// that reaches its last hop dies here rather than echoing forever.
	if p.TTL <= 1 {
		c.logger.Debug("transit: ttl exhausted", "from", env.From, "dest", p.Dest)
		return
	}
	// Serving gate (§4.2): trust.relay.transit decides whose frames this node
	// forwards. It is checked only on the relaying path — a frame addressed
	// to this node is delivery, not service, and must open regardless of how
	// the operator fenced the sender.
	if !c.relayAcceptAllowed(ctx, env.From) {
		c.logger.Debug("transit: relay refused by trust policy", "from", env.From, "dest", p.Dest)
		return
	}
	release, ok := c.relayLim.acquire()
	if !ok {
		c.logger.Debug("transit: relay session cap reached", "from", env.From, "dest", p.Dest)
		return
	}
	defer release()
	// Charge what actually leaves this node. The sealed blob travels as
	// base64 inside the marshalled payload, so the egress is a third larger
	// than the blob's byte length — budgeting the blob would let a relay
	// spend a third more bandwidth than the operator allowed.
	p.TTL--
	raw, err := json.Marshal(p)
	if err != nil {
		return
	}
	if !c.relayLim.allow(len(raw)) {
		c.logger.Debug("transit: relay bandwidth cap reached", "from", env.From, "bytes", len(raw))
		return
	}
	// No-echo: never hand a frame straight back to the conn it arrived on.
	// env.From is the origin, conn.PeerID() the inbound relay — excluding
	// both covers the two obvious loop arms.
	exclude := map[string]bool{env.From: true}
	if conn != nil && conn.PeerID() != "" {
		exclude[conn.PeerID()] = true
	}
	hop := c.transitNextHop(ctx, p.Dest, exclude)
	if hop == "" {
		c.logger.Debug("transit: no onward hop", "from", env.From, "dest", p.Dest)
		return
	}
	env.Payload = raw
	// The outer From is rewritten hop by hop — the same discipline
	// forwardPunch runs under: the receiver's conn binding requires
	// env.From == the bound peer id, and origin provenance lives inside the
	// sealed inner's attestation, not in the carrier field.
	env.From = c.nodeID
	next := c.connFor(hop)
	if next == nil {
		c.logger.Debug("transit: hop lost before forward", "hop", hop, "dest", p.Dest)
		return
	}
	if err := next.Send(env); err != nil {
		c.logger.Debug("transit: forward failed", "hop", hop, "dest", p.Dest, "err", err)
	}
}

// transitNextHop picks the conn a transit frame leaves on. Order: a conn
// straight to the destination instance (cheapest — the frame's last hop),
// then the link-state graph's first hop toward it, then any connected peer
// that advertised trnz and therefore knows what to do with the frame.
// Every candidate must advertise trnz: an old peer that lacks the capability
// would log the frame as unhandled and drop it, silently burning the hop and
// making the sender believe the frame was routed. WS only, by design: a
// punched datagram route tops out at 1400-byte datagrams and cannot carry a
// transit frame.
func (c *Core) transitNextHop(ctx context.Context, destKey string, exclude map[string]bool) string {
	excluded := func(id string) bool { return exclude[id] }
	// Candidates are collected under the peer lock and filtered outside it:
	// the serve gate resolves each candidate's trust tier, which is a store
	// read, and holding c.mu across a query would stall every heartbeat and
	// conn registration behind it.
	var candidates []string
	if inst := c.instanceForStable(ctx, destKey); inst != "" && !excluded(inst) {
		if conn := c.connFor(inst); conn != nil && conn.Supports(bus.CapTransitRelay) {
			candidates = append(candidates, inst)
		} else if hop, _ := c.dtnNextHop(ctx, inst, exclude, 0, 0); hop != "" && !excluded(hop) {
			if conn := c.connFor(hop); conn != nil && conn.Supports(bus.CapTransitRelay) {
				candidates = append(candidates, hop)
			}
		}
	}
	c.mu.RLock()
	for id, p := range c.peers {
		if excluded(id) || p.conn == nil {
			continue
		}
		if p.conn.Supports(bus.CapTransitRelay) {
			candidates = append(candidates, id)
		}
	}
	c.mu.RUnlock()
	for _, id := range candidates {
		if c.relayServeAllowed(ctx, id) {
			return id
		}
	}
	return ""
}

// deliverTransit opens a frame whose destination resolved to this node and
// feeds the inner envelope back into dispatch. Everything fails closed:
// a blob that does not open, an attestation that does not verify, an inner
// frame type that needed a conn — all drop with a debug line, never a guess.
func (c *Core) deliverTransit(ctx context.Context, env bus.Envelope, p bus.TransitPayload) {
	var plain []byte
	var err error
	switch p.Seal {
	case bus.SealX25519:
		priv, _, ok := c.nodeXKey()
		if !ok {
			c.logger.Warn("transit: x25519-sealed frame but no x key", "from", env.From)
			return
		}
		plain, err = bus.TransitOpenX25519(priv, p.Inner)
	case bus.SealMesh:
		plain, err = bus.TransitOpenMesh(c.sharedSecretValue(), p.Dest, p.Inner)
	case bus.SealNone:
		// The pairing exemption. Only the type check below makes this safe:
		// an unsealed frame is accepted solely when its inner is a pair
		// handshake, which carries no secret and is authenticated by the
		// SAS rather than by the transport.
		plain = p.Inner
	}
	if err != nil {
		c.mesh.transitRefused.Add(1)
		c.logger.Debug("transit: open failed (tamper or wrong dest)", "from", env.From, "err", err)
		return
	}
	c.mesh.transitRxBytes.Add(int64(len(p.Inner)))
	var inner transitInner
	if err := json.Unmarshal(plain, &inner); err != nil {
		c.logger.Debug("transit: malformed inner", "from", env.From)
		return
	}
	var innerEnv bus.Envelope
	if err := json.Unmarshal(inner.Env, &innerEnv); err != nil {
		c.logger.Debug("transit: malformed inner envelope", "from", env.From)
		return
	}
	if !transitEligible(innerEnv.Type) {
		c.logger.Debug("transit: ineligible inner type", "type", innerEnv.Type, "from", env.From)
		return
	}
	if p.Seal == bus.SealNone && !isPairMessage(innerEnv.Type) {
		// SealNone is the pair handshake's exemption and nothing else's. A
		// delegate or result riding unsealed would make every relay a
		// legitimate reader, which the whole transit design forbids.
		c.logger.Warn("transit: unsealed non-pair frame rejected", "type", innerEnv.Type, "from", env.From)
		return
	}
	// Attestation gate: transit has no conn-bound From, so the signature is
	// the ONLY proof of who wrote the inner frame. An inner without one is
	// rejected — unlike a direct conn, there is no authenticated peer whose
	// conn binding could stand in for it. A signature that verifies still
	// has to name an identity this node can tie to the signing key (see
	// transitFromOK); an arbitrary instance id does not inherit mesh trust.
	if inner.Pub == "" || inner.Sig == "" {
		c.logger.Warn("transit: unsigned inner rejected",
			"from", env.From, "inner-from", innerEnv.From)
		return
	}
	pub, derr := hex.DecodeString(inner.Pub)
	if derr != nil || len(pub) != 32 || !bus.VerifyTransitInner(pub, inner.Env, inner.Sig) {
		c.logger.Warn("transit: inner attestation failed", "from", env.From, "inner-from", innerEnv.From)
		return
	}
	bound, unknown := c.transitFromOK(ctx, innerEnv.From, inner.Pub)
	if !bound && !(unknown && transitFirstContact(innerEnv.Type)) {
		c.logger.Warn("transit: inner From not bound to signing key",
			"inner-from", innerEnv.From, "signer", inner.Pub, "type", innerEnv.Type)
		return
	}
	if isPairMessage(innerEnv.Type) {
		// Pair frames keep the relay hop: the joiner is not in the directory,
		// so the answer has to go back the way the question came.
		c.handlePairViaTransit(ctx, innerEnv, env.From)
		return
	}
	// The inner frame must be addressed to THIS node. A member can seal a
	// frame for somebody else and route it here; executing it would apply
	// this node's admission policy to a task meant for another node — and
	// its result would come back here instead of there.
	if !c.transitAddressedToUs(ctx, innerEnv.To) {
		c.logger.Warn("transit: inner frame is not addressed to this node",
			"to", innerEnv.To, "type", innerEnv.Type, "inner-from", innerEnv.From)
		return
	}
	c.dispatch(ctx, nil, innerEnv)
}

// transitAddressedToUs reports whether an inner envelope's addressee names
// this node. An empty addressee is a broadcast; a stable id must be this
// node's own key; an instance id must be this node's runtime identity (a
// restarted node's session sibling included).
func (c *Core) transitAddressedToUs(ctx context.Context, to string) bool {
	if to == "" {
		return true
	}
	if to == c.nodeID {
		return true
	}
	if strings.HasPrefix(to, stableIDPrefix) {
		return c.isSelfStable(to)
	}
	return scheduler.SameRuntimeIdentity(c.instanceForStable(ctx, to), c.nodeID)
}

// transitFirstContact lists the frames an unpinned peer may still send over
// transit: liveness/discovery and punch coordination are how two nodes meet
// before either has helloed the other. Everything else — delegates, results,
// accepts, bundles — requires a From whose key this node already pinned, or
// a ghost instance id would resolve to the default mesh tier and act with
// trust it never earned.
func transitFirstContact(typ string) bool {
	switch typ {
	case bus.MsgHeartbeat, bus.MsgPunchOffer, bus.MsgPunchReady:
		return true
	// A joiner is by definition unpinned: the whole point of the ceremony is
	// that the two sides have never met. Pair frames therefore pass the
	// first-contact gate and rely on the SAS for authentication.
	case bus.MsgPairHello, bus.MsgPairReady, bus.MsgPairSecret, bus.MsgPairReject:
		return true
	}
	return false
}

// transitFromOK checks the attested From against the identity this node's
// directory already pins. A stable "k:<pub>" From is self-proving: it must
// equal the signing key verbatim, and the key itself is the identity (the
// trust layer resolves it through PeerTrustByKey). An instance or session id
// must have a directory row whose pub_key is exactly the signing key —
// checked through the host row for a session id, matching peerPubKey's
// resolution. Rows with no proven key, or a key that contradicts the claim,
// fail outright. A missing row returns unknown=true: only transitFirstContact
// frame types may use that first-contact grace, so a self-chosen instance id
// cannot mint a ghost peer that pushes delegates at the default mesh tier.
// First contact for task-bearing frames must happen over a signed direct
// hello; thereafter transit may carry the instance id it pinned.
func (c *Core) transitFromOK(ctx context.Context, from, pubHex string) (bound, unknown bool) {
	if from == "" || pubHex == "" {
		return false, false
	}
	if strings.HasPrefix(from, stableIDPrefix) {
		return from == stableIDPrefix+pubHex, false
	}
	if c.db == nil {
		return false, false
	}
	row := from
	if base, ok := scheduler.EphemeralBase(from); ok {
		row = base
	}
	var pinned string
	if err := c.db.QueryRowContext(ctx,
		`SELECT COALESCE(pub_key,'') FROM employee_cache WHERE id = ?`, row).Scan(&pinned); err != nil {
		return false, true // no row: eligible only for first-contact frame types
	}
	return pinned != "" && pinned == pubHex, false
}

// isSelfStable reports whether key is this node's own stable identity. It is
// the transit equivalent of scheduler.SameRuntimeIdentity for a key that has
// never been through the directory.
func (c *Core) isSelfStable(key string) bool {
	if !strings.HasPrefix(key, stableIDPrefix) {
		return false
	}
	pub, _, ok := c.nodeKeyPair()
	if !ok {
		return false
	}
	return strings.EqualFold(key[len(stableIDPrefix):], hex.EncodeToString(pub))
}
