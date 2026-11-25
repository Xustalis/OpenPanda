// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// Pair-over-transit ("pair over transit"; see docs/protocol.md).
//
// The problem: two nodes that have never met sit behind NATs, so neither can
// dial the other and neither holds the shared secret that would let a hello
// authenticate. The design's answer is a contact point — any online member
// whose address the join URI names — that forwards the pairing handshake.
//
//	joiner ──plain pair_hello(target=k:<I>)──▶ contact point
//	contact point ──transit(seal=none, dest=k:<I>)──▶ introducer
//	introducer ──transit(seal=none, dest=k:<J>)──▶ contact point
//	contact point ──plain pair_ready/…──▶ joiner
//
// Two deliberate simplifications keep both ends speaking the ordinary
// ceremony, so no client code changes and no new wire dialect appears:
//
//   - The joiner's frames stay PLAIN on its own conn. The contact point does
//     the wrapping, which is also what lets it learn the return route: the
//     pair_hello it forwards already carries the joiner's Ed25519 pub, and
//     that pub is what the att_clients binding records.
//   - The introducer answers SEALED-NONE, and the contact point unwraps and
//     re-emits the inner frame verbatim. This is the design's "no x_pub →
//     degraded" branch; an x25519 upgrade is possible later precisely because
//     a joining node could advertise x_pub in pair_hello, but it would need a
//     transit-aware client, which this phase deliberately does not add.
//
// What the contact point can and cannot see: it reads the pair handshake —
// whose confidentiality was never claimed on a hostile channel, since the DH
// box protects the secret and the SAS authenticates the peers — and it can
// therefore also refuse, delay or corrupt it. Every one of those shows up as
// a code mismatch or a failed box open at the two humans comparing screens.
// That is the pre-existing MITM model, not a new exposure.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// errPairNoRoute means a transit pair frame had nowhere to go — the reply hop
// is gone or the forward found no path to the target.
var errPairNoRoute = errors.New("pair: no transit route")

// pairSend delivers one pairing frame on whichever channel the session was
// created on: the direct conn for the LAN ceremony, a transit envelope back
// through the introducing hop for the cross-NAT one.
func (c *Core) pairSend(ctx context.Context, sess *pairSession, env bus.Envelope) error {
	if sess.conn != nil {
		return sess.conn.Send(env)
	}
	if sess.replyHop == nil || sess.replyDest == "" {
		return errPairNoRoute
	}
	pub, priv, ok := c.nodeKeyPair()
	if !ok {
		return errTransitUnattested
	}
	innerEnv, err := json.Marshal(env)
	if err != nil {
		return err
	}
	inner := transitInner{
		Env: innerEnv,
		Pub: hex.EncodeToString(pub),
		Sig: bus.SignTransitInner(priv, innerEnv),
	}
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		return err
	}
	tenv, err := bus.NewEnvelope(bus.MsgTransit, c.nodeID, mustUUID(env.MsgID), bus.TransitPayload{
		Dest: sess.replyDest, TTL: bus.TransitMaxTTL, Seal: bus.SealNone, Inner: innerJSON,
	})
	if err != nil {
		return err
	}
	tenv.To = sess.replyDest
	// Degradation is audited where it happens: the reply is readable by the
	// contact point. The secret inside is still DH-boxed, so this is a
	// metadata note, not a leak.
	c.audit(ctx, "", "transit_degraded", sess.replyDest, "pair-none-seal",
		"pair reply crosses an unsealed transit hop")
	return sess.replyHop.Send(tenv)
}

// proxyPairHello is the contact point's half: bind the joiner's return route
// and forward the handshake to the introducer it named.
func (c *Core) proxyPairHello(ctx context.Context, conn *bus.Conn, env bus.Envelope, p bus.PairHelloPayload) {
	if conn == nil || conn.PeerID() != "" {
		// Already-authenticated peers pair over their own conn; only an
		// unauthenticated joiner needs a proxy.
		c.sendPairReject(conn, p.Session, "already_joined")
		return
	}
	if !strings.HasPrefix(p.Target, stableIDPrefix) {
		c.sendPairReject(conn, p.Session, "bad_target")
		return
	}
	pub, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(p.Pub), stableIDPrefix))
	if err != nil || len(pub) != 32 {
		c.sendPairReject(conn, p.Session, "malformed")
		return
	}
	srcIP, _, _ := strings.Cut(conn.RemoteAddr(), ":")
	c.pairing.mu.Lock()
	if c.pairing.rate == nil {
		c.pairing.rate = make(map[string][]time.Time)
	}
	allowed := c.pairRateAllow(srcIP)
	c.pairing.mu.Unlock()
	if !allowed {
		c.sendPairReject(conn, p.Session, "rate_limited")
		return
	}

	stable := stableIDPrefix + hex.EncodeToString(pub)
	c.registerAttClient(stable, conn)

	// The Target is consumed HERE — capture it first, then strip it from the
	// forwarded frame: leaving it in would make the introducer treat itself
	// as a second contact point and bounce the request onward.
	target := p.Target
	p.Target = ""
	if env.Payload, err = json.Marshal(p); err != nil {
		c.dropAttClient(stable, conn)
		c.sendPairReject(conn, p.Session, "malformed")
		return
	}
	innerEnv, err := json.Marshal(env)
	if err != nil {
		c.sendPairReject(conn, p.Session, "malformed")
		return
	}
	pubSelf, priv, ok := c.nodeKeyPair()
	if !ok {
		c.sendPairReject(conn, p.Session, "no_identity")
		return
	}
	inner := transitInner{
		Env: innerEnv,
		Pub: hex.EncodeToString(pubSelf),
		Sig: bus.SignTransitInner(priv, innerEnv),
	}
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		c.sendPairReject(conn, p.Session, "malformed")
		return
	}
	hop := c.transitNextHop(ctx, target, nil)
	if hop == "" {
		c.dropAttClient(stable, conn)
		c.sendPairReject(conn, p.Session, "introducer_unreachable")
		return
	}
	next := c.connFor(hop)
	if next == nil {
		c.dropAttClient(stable, conn)
		c.sendPairReject(conn, p.Session, "introducer_unreachable")
		return
	}
	tenv, err := bus.NewEnvelope(bus.MsgTransit, c.nodeID, mustUUID(env.MsgID), bus.TransitPayload{
		Dest: target, TTL: bus.TransitMaxTTL, Seal: bus.SealNone, Inner: innerJSON,
	})
	if err != nil {
		c.dropAttClient(stable, conn)
		c.sendPairReject(conn, p.Session, "malformed")
		return
	}
	tenv.To = target
	if err := next.Send(tenv); err != nil {
		c.dropAttClient(stable, conn)
		c.sendPairReject(conn, p.Session, "introducer_unreachable")
		return
	}
	c.logger.Info("pair: proxied join request", "session", p.Session, "target", target, "hop", hop)
}

// handlePairViaTransit receives a pairing frame that arrived inside a transit
// envelope. Only pair_hello is actionable here: it names a joiner, so the
// session records where to answer.
func (c *Core) handlePairViaTransit(ctx context.Context, env bus.Envelope, hop string) {
	if env.Type != bus.MsgPairHello {
		// ready/secret/reject address a joiner, and joiners speak plain
		// frames to their contact point — nothing legitimate is addressed
		// here.
		return
	}
	hopConn := c.connFor(hop)
	if hopConn == nil {
		c.logger.Debug("pair: transit handshake from a lost hop", "hop", hop)
		return
	}
	c.pairHelloVia(ctx, hopConn, env)
}

// deliverToAttached hands a transit frame to the joiner bound to this contact
// point. The frame must be the pairing exemption — unsealed, pair-typed — so
// it can be re-emitted as the plain frame the joiner's client understands.
func (c *Core) deliverToAttached(ctx context.Context, env bus.Envelope, p bus.TransitPayload, target *bus.Conn) {
	if p.Seal != bus.SealNone {
		// A sealed reply would need a transit-aware client; the introducer
		// only seals x25519 when the joiner advertised an x_pub, which this
		// phase's joiners do not. Drop loudly rather than pretend.
		c.logger.Warn("transit: sealed frame for an attached client dropped", "dest", p.Dest, "seal", p.Seal)
		return
	}
	var inner transitInner
	if err := json.Unmarshal(p.Inner, &inner); err != nil {
		return
	}
	var innerEnv bus.Envelope
	if err := json.Unmarshal(inner.Env, &innerEnv); err != nil {
		return
	}
	if !isPairMessage(innerEnv.Type) {
		c.logger.Warn("transit: non-pair frame aimed at an attached client", "type", innerEnv.Type, "dest", p.Dest)
		return
	}
	if err := target.Send(innerEnv); err != nil {
		c.logger.Debug("transit: attached client write failed", "dest", p.Dest, "err", err)
		c.dropAttClient(p.Dest, target)
		return
	}
	// The pairing is over once the secret is handed over; the binding has no
	// further use and must not outlive it.
	if innerEnv.Type == bus.MsgPairSecret || innerEnv.Type == bus.MsgPairReject {
		c.dropAttClient(p.Dest, target)
	}
	c.logger.Debug("pair: relayed frame to attached client", "dest", p.Dest, "type", innerEnv.Type)
}
