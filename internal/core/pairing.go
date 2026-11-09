// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// Bluetooth-style LAN pairing. The problem this solves: joining a new node
// used to mean hand-copying network.shared_secret between machines — the
// one value that must never travel in plaintext, so no in-band channel
// could carry it. Pairing replaces the manual copy with the same ceremony
// Bluetooth uses: an ephemeral X25519 exchange on an unauthenticated conn,
// a short authentication string (SAS) derived from both ephemeral keys,
// both Ed25519 identity pubs, and the DH secret — shown on both screens;
// the humans confirm on the responder side, and only then does the
// responder hand the mesh secret over the DH-sealed channel.
//
// Trust model: the 6-digit code binds (initX, respX, initPub, respPub,
// initNonce, respNonce, dh). A MITM relaying the exchange ends up with two
// different DH secrets, hence two different codes — the visual compare is
// the authentication. The secret only ever flows responder→initiator,
// inside AES-256-GCM keyed by HKDF over the DH secret plus both nonces.
//
// Process split: the responder daemon owns the session (ephemeral private
// key, the conn, the wait-for-confirm goroutine) and publishes a
// pair_sessions row so `panda pair`/`panda pair confirm` — a borrowed
// engine on the same host — can see and answer the request. The row is
// IPC only; no secret material is ever persisted. The initiator is the
// synchronous `panda pair <target>` CLI flow and keeps no state at all.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/guard"
)

const (
	// pairSessionTTL bounds the confirm window: the operator must compare
	// the code and answer within it, the same way a Bluetooth pairing
	// prompt expires. Short enough that an abandoned session cannot leak
	// a secret long after anyone looked at the screen.
	pairSessionTTL = 4 * time.Minute
	// pairMaxSessions caps concurrent inbound sessions — each holds a conn
	// and a goroutine, and the pair_sessions table should never grow
	// unboundedly on a hostile LAN.
	pairMaxSessions = 8
	// pairRateWindow/pairRatePerIP bound inbound pair_hello per source IP.
	// Each attempt is a conn plus a row; a flood is litter at worst, but
	// bounded litter is still better.
	pairRateWindow = time.Hour
	pairRatePerIP  = 12
)

// Pair session row states (pair_sessions.state).
const (
	PairStateReady     = "ready"     // keys exchanged, SAS displayed, waiting for the human
	PairStateConfirmed = "confirmed" // operator confirmed the code — daemon may send the secret
	PairStateDone      = "done"      // secret delivered; session complete
	PairStateRejected  = "rejected"  // operator said no
	PairStateExpired   = "expired"   // confirm window elapsed / conn died
)

// pairSession is the responder's in-memory half of a pairing: everything
// needed to finish that is too secret or too live for the DB row.
type pairSession struct {
	id        string
	dh        *ecdh.PrivateKey
	respX     []byte
	respNonce []byte
	initX     []byte
	initPub   []byte
	initNonce []byte
	initAddr  string
	initName  string
	conn      *bus.Conn
	// replyHop/replyDest route the answers for a session created from a
	// transit-delivered pair_hello (P4b): the joiner is not in the directory,
	// so the only way back is the hop the question came from. Both empty on
	// the direct path, where conn carries the replies.
	replyHop  *bus.Conn
	replyDest string
	expires   time.Time
}

// closeConn drops the session's direct conn, if it has one. A transit session
// owns no conn — the contact point does — so there is nothing to close.
func (s *pairSession) closeConn() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// PairJoinHook is called by the responder when a pairing completes: it is
// where "join the network" lands in configuration — persist the peer (and,
// if this node had no secret yet, the generated one) and hot-apply the
// change. Implemented by the daemon's config writer; embedded engines may
// leave it nil, in which case the peer is added in memory only.
type PairJoinHook func(peerAddr string) error

// pairingState groups the responder's pairing bookkeeping on Core.
type pairingState struct {
	mu       sync.Mutex
	sessions map[string]*pairSession
	rate     map[string][]time.Time // pair_hello timestamps per source IP
	joinHook PairJoinHook
}

// SetPairJoinHook installs the responder-side "write the join into config"
// callback (see PairJoinHook).
func (c *Core) SetPairJoinHook(h PairJoinHook) {
	c.pairing.mu.Lock()
	defer c.pairing.mu.Unlock()
	c.pairing.joinHook = h
}

// isPairMessage reports whether env.Type belongs to the pairing sub-protocol
// — the only message family besides hello legal on an unauthenticated conn.
func isPairMessage(typ string) bool {
	switch typ {
	case bus.MsgPairHello, bus.MsgPairReady, bus.MsgPairSecret, bus.MsgPairReject:
		return true
	}
	return false
}

// handlePairHello answers an inbound pair_hello: rate-limit, cap sessions,
// exchange ephemeral keys, publish the session row + SAS, and start the
// goroutine that waits out the human confirm. The conn it arrives on is
// unauthenticated and stays so — pairing never binds a PeerID; the sealed
// secret is the only thing it is allowed to carry away.
func (c *Core) handlePairHello(ctx context.Context, conn *bus.Conn, env bus.Envelope) {
	c.pairHello(ctx, conn, nil, env)
}

// pairHelloVia is the transit-delivered entry point (P4b): the frame arrived
// inside a transit envelope relayed by hop, so the answers go back through
// that hop instead of a conn.
func (c *Core) pairHelloVia(ctx context.Context, hop *bus.Conn, env bus.Envelope) {
	c.pairHello(ctx, nil, hop, env)
}

// pairHello is the shared body: conn is the direct unauthenticated conn (LAN
// ceremony) and hop the relayed route (cross-NAT), exactly one of which is
// set.
func (c *Core) pairHello(ctx context.Context, conn *bus.Conn, hop *bus.Conn, env bus.Envelope) {
	var p bus.PairHelloPayload
	if err := env.PayloadInto(&p); err != nil || p.Session == "" {
		c.logger.Warn("pair_hello: malformed", "err", err)
		return
	}
	// A Target asks this node to act as a contact point rather than as the
	// responder — the joiner behind NAT naming the introducer it cannot dial.
	// Only a DIRECT conn can be asking: a transit-delivered hello has already
	// been through a proxy, and honoring its Target would loop it onward.
	//
	// The target may name THIS node: `panda nodes invite --uri` emits a
	// bootstrap that is the introducer itself and pins k= to its own key, so
	// the joiner dials us directly with Target=k:<self>. Answering locally is
	// the whole ceremony; proxying would ask the mesh to route the handshake
	// back to us (and on a two-node mesh fail with introducer_unreachable).
	if p.Target != "" && conn != nil {
		if c.isSelfStable(strings.ToLower(p.Target)) {
			p.Target = ""
		} else {
			c.proxyPairHello(ctx, conn, env, p)
			return
		}
	}
	if conn == nil && hop == nil {
		return
	}
	if conn != nil && conn.PeerID() != "" {
		// A peer that already hellos is already inside — pairing would only
		// re-send the secret it demonstrably holds.
		c.sendPairReject(conn, p.Session, "already_joined")
		return
	}
	// Initiator's view of its own listen address; when it advertises a
	// wildcard/loopback host the responder substitutes the source IP of
	// this conn — the same "dial me where you saw me" rule beacons use.
	srcIP := ""
	if conn != nil {
		srcIP, _, _ = strings.Cut(conn.RemoteAddr(), ":")
	}

	c.pairing.mu.Lock()
	if c.pairing.sessions == nil {
		c.pairing.sessions = make(map[string]*pairSession)
	}
	if c.pairing.rate == nil {
		c.pairing.rate = make(map[string][]time.Time)
	}
	if len(c.pairing.sessions) >= pairMaxSessions {
		c.pairing.mu.Unlock()
		c.sendPairReject(conn, p.Session, "busy")
		return
	}
	if !c.pairRateAllow(srcIP) {
		c.pairing.mu.Unlock()
		c.sendPairReject(conn, p.Session, "rate_limited")
		return
	}
	if _, dup := c.pairing.sessions[p.Session]; dup {
		// A retransmitted pair_hello must not mint a second session: replay
		// pair_ready from the existing one and let it continue.
		sess := c.pairing.sessions[p.Session]
		c.pairing.mu.Unlock()
		c.sendPairReady(ctx, sess)
		return
	}

	// Validate the advertised key material before trusting any of it —
	// hex decode failures are malformed, sizes are fixed by the curve.
	initX, err1 := hex.DecodeString(p.X)
	initPub, err2 := hex.DecodeString(p.Pub)
	initNonce, err3 := hex.DecodeString(p.Nonce)
	if err1 != nil || err2 != nil || err3 != nil ||
		len(initX) != 32 || len(initPub) != ed25519.PublicKeySize || len(initNonce) < 8 {
		c.pairing.mu.Unlock()
		c.sendPairReject(conn, p.Session, "malformed")
		return
	}

	respKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		c.pairing.mu.Unlock()
		c.logger.Warn("pair: x25519 keygen", "err", err)
		return
	}
	respNonce := make([]byte, 16)
	_, _ = rand.Read(respNonce)
	respX := respKey.PublicKey().Bytes()

	initXPub, err := ecdh.X25519().NewPublicKey(initX)
	if err != nil {
		c.pairing.mu.Unlock()
		c.sendPairReject(conn, p.Session, "key_exchange")
		return
	}
	dh, err := respKey.ECDH(initXPub)
	if err != nil {
		c.pairing.mu.Unlock()
		c.sendPairReject(conn, p.Session, "key_exchange")
		return
	}

	sess := &pairSession{
		id:        p.Session,
		dh:        respKey,
		respX:     respX,
		respNonce: respNonce,
		initX:     initX,
		initPub:   initPub,
		initNonce: initNonce,
		initAddr:  resolvePairAddr(p.Addr, srcIP),
		initName:  p.Name,
		conn:      conn,
		replyHop:  hop,
		expires:   time.Now().Add(pairSessionTTL),
	}
	if hop != nil && p.Pub != "" {
		// The joiner's own identity is the return address the contact point
		// bound its conn to; it is a destination, never a trust claim.
		sess.replyDest = stableIDPrefix + strings.ToLower(p.Pub)
	}
	sas := pairSAS(initX, respX, initPub, respPub(c), initNonce, respNonce, dh)
	sess.initAddr = normalizeDialableAddr(sess.initAddr)
	c.pairing.sessions[p.Session] = sess
	c.pairing.mu.Unlock()

	if err := c.pairSessionInsert(ctx, sess, sas); err != nil {
		c.logger.Warn("pair: persist session", "session", sess.id, "err", err)
	}
	c.logger.Info("pair: session offered", "session", sess.id, "peer", sess.initName, "addr", sess.initAddr)
	// The conn arrived under the short unauthenticated-hello deadline; the
	// confirm window is minutes, so hand it the keepalive deadline — the
	// ping/pong machinery refreshes it from here on.
	if conn != nil {
		_ = conn.ResetReadDeadline()
	}
	c.sendPairReady(ctx, sess)
	c.goGuarded("pair confirm "+sess.id, func() { c.waitPairConfirm(ctx, sess, dh) })
}

// handlePairCancel handles a pair_reject arriving on the responder conn —
// the initiator aborting (or its user closing) while we wait for our own
// operator's confirm.
func (c *Core) handlePairCancel(ctx context.Context, conn *bus.Conn, env bus.Envelope) {
	var p bus.PairRejectPayload
	if err := env.PayloadInto(&p); err != nil || p.Session == "" {
		return
	}
	c.pairing.mu.Lock()
	sess := c.pairing.sessions[p.Session]
	delete(c.pairing.sessions, p.Session)
	c.pairing.mu.Unlock()
	if sess != nil && (sess.conn == conn || (sess.conn == nil && conn == nil)) {
		_ = c.pairSessionSetState(ctx, sess.id, PairStateExpired)
	}
}

// waitPairConfirm owns the responder session until it resolves: poll the
// row for the operator's answer, then either seal the secret and close the
// join, or send a reject/expired and clean up. The conn stays held by this
// goroutine for the session's life — the inbound read loop that dispatched
// us is what keeps the socket serviced in the meantime (pair_reject from
// the initiator routes to handlePairCancel).
func (c *Core) waitPairConfirm(ctx context.Context, sess *pairSession, dh []byte) {
	defer func() {
		c.pairing.mu.Lock()
		delete(c.pairing.sessions, sess.id)
		c.pairing.mu.Unlock()
	}()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		if time.Now().After(sess.expires) {
			_ = c.pairSessionSetState(ctx, sess.id, PairStateExpired)
			c.sendPairRejectSess(ctx, sess, "expired")
			sess.closeConn()
			return
		}
		state, err := c.pairSessionState(ctx, sess.id)
		if err != nil {
			// Row vanished (operator cleanup, db close): treat as abort.
			c.pairing.mu.Lock()
			delete(c.pairing.sessions, sess.id)
			c.pairing.mu.Unlock()
			sess.closeConn()
			return
		}
		switch state {
		case PairStateConfirmed:
			if err := c.pairAccept(ctx, sess, dh); err != nil {
				c.logger.Warn("pair: accept failed", "session", sess.id, "err", err)
				c.sendPairRejectSess(ctx, sess, "accept_failed")
			} else {
				_ = c.pairSessionSetState(ctx, sess.id, PairStateDone)
				c.logger.Info("pair: joined", "peer", sess.initName, "addr", sess.initAddr)
			}
			sess.closeConn()
			return
		case PairStateRejected:
			c.sendPairRejectSess(ctx, sess, "rejected")
			sess.closeConn()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// pairAccept completes a confirmed session on the responder: seal the
// shared secret under the DH key, send it, then run the join hook so this
// node also learns the initiator as a peer.
func (c *Core) pairAccept(ctx context.Context, sess *pairSession, dh []byte) error {
	secret := c.sharedSecretValue()
	if secret == "" {
		return errors.New("no shared secret to share")
	}
	key, err := pairBoxKey(dh, sess.initNonce, sess.respNonce)
	if err != nil {
		return err
	}
	box, err := pairSeal(key, []byte(secret))
	if err != nil {
		return err
	}
	msgID, err := newUUID()
	if err != nil {
		return err
	}
	env, err := bus.NewEnvelope(bus.MsgPairSecret, c.nodeID, msgID, bus.PairSecretPayload{
		Session: sess.id,
		Box:     base64.StdEncoding.EncodeToString(box),
	})
	if err != nil {
		return err
	}
	if err := c.pairSend(ctx, sess, env); err != nil {
		return fmt.Errorf("send pair_secret: %w", err)
	}
	c.pairing.mu.Lock()
	hook := c.pairing.joinHook
	c.pairing.mu.Unlock()
	if hook != nil && sess.initAddr != "" {
		if err := hook(sess.initAddr); err != nil {
			c.logger.Warn("pair: join hook", "err", err)
		}
	}
	return nil
}

// sendPairReady emits pair_ready for an existing session (initial answer
// or idempotent replay on a duplicate pair_hello).
func (c *Core) sendPairReady(ctx context.Context, sess *pairSession) {
	msgID, err := newUUID()
	if err != nil {
		return
	}
	env, err := bus.NewEnvelope(bus.MsgPairReady, c.nodeID, msgID, bus.PairReadyPayload{
		Session: sess.id,
		X:       hex.EncodeToString(sess.respX),
		Pub:     hex.EncodeToString(respPub(c)),
		Nonce:   hex.EncodeToString(sess.respNonce),
	})
	if err == nil {
		if err := c.pairSend(ctx, sess, env); err != nil {
			c.logger.Debug("pair: send ready", "session", sess.id, "err", err)
		}
	}
}

// sendPairReject ends a session on the wire with a stable reason token.
func (c *Core) sendPairReject(conn *bus.Conn, session, reason string) {
	msgID, err := newUUID()
	if err != nil {
		return
	}
	env, err := bus.NewEnvelope(bus.MsgPairReject, c.nodeID, msgID, bus.PairRejectPayload{
		Session: session, Reason: reason,
	})
	if err != nil {
		return
	}
	if conn != nil {
		_ = conn.Send(env)
	}
}

// sendPairRejectSess ends a session on whichever channel it was created on —
// the direct conn or the transit route back through the contact point.
func (c *Core) sendPairRejectSess(ctx context.Context, sess *pairSession, reason string) {
	msgID, err := newUUID()
	if err != nil {
		return
	}
	env, err := bus.NewEnvelope(bus.MsgPairReject, c.nodeID, msgID, bus.PairRejectPayload{
		Session: sess.id, Reason: reason,
	})
	if err == nil {
		_ = c.pairSend(ctx, sess, env)
	}
}

// respPub returns the responder's Ed25519 identity pubkey — the same key
// pair hellos sign — so the SAS binds the real on-mesh identity, not a
// pair-only alias.
func respPub(c *Core) []byte {
	pub, _, _ := c.nodeKeyPair()
	return pub
}

// pairRateAllow records one pair_hello attempt from ip, returning false
// past the per-window budget. Caller holds pairing.mu.
func (c *Core) pairRateAllow(ip string) bool {
	if ip == "" {
		ip = "?"
	}
	cutoff := time.Now().Add(-pairRateWindow)
	hits := c.pairing.rate[ip]
	kept := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= pairRatePerIP {
		c.pairing.rate[ip] = kept
		return false
	}
	c.pairing.rate[ip] = append(kept, time.Now())
	return true
}

// sweepPairSessions expires in-memory sessions whose window elapsed — the
// waitPairConfirm goroutine normally owns expiry, this is the backstop for
// sessions orphaned by a wedged conn. Called from the monitor tick.
func (c *Core) sweepPairSessions(ctx context.Context) {
	c.pairing.mu.Lock()
	var dead []*pairSession
	for _, s := range c.pairing.sessions {
		if time.Now().After(s.expires) {
			dead = append(dead, s)
		}
	}
	c.pairing.mu.Unlock()
	for _, s := range dead {
		_ = c.pairSessionSetState(ctx, s.id, PairStateExpired)
		// closeConn, not s.conn.Close(): a pairing that arrived over TRANSIT
		// has no conn — the reply rides the contact point's replyHop — and
		// the unconditional Close dereferenced nil. The panic was recovered
		// by the monitor's guard, whose cancel() took the WHOLE daemon down,
		// so any cross-NAT pairing could randomly kill the node.
		s.closeConn()
		// Drop the expired session so the sweep does not reprocess it (and
		// so its id cannot be reused while a reply is still in flight).
		c.pairing.mu.Lock()
		delete(c.pairing.sessions, s.id)
		c.pairing.mu.Unlock()
	}
	// And rows left 'ready' past expiry — e.g. written before a crash —
	// would otherwise list forever.
	if c.db != nil {
		_, _ = c.db.ExecContext(ctx,
			`UPDATE pair_sessions SET state=? WHERE state=? AND expires_at<?`,
			PairStateExpired, PairStateReady, time.Now().Unix())
	}
}

// pairSessionInsert publishes the session row the operator's CLI lists.
func (c *Core) pairSessionInsert(ctx context.Context, s *pairSession, sas string) error {
	if c.db == nil {
		return nil
	}
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO pair_sessions (id, peer_addr, peer_name, peer_pub, sas, state, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET state=excluded.state, sas=excluded.sas`,
		s.id, s.initAddr, s.initName, hex.EncodeToString(s.initPub),
		sas, PairStateReady, time.Now().Unix(), s.expires.Unix())
	return err
}

func (c *Core) pairSessionState(ctx context.Context, id string) (string, error) {
	var st string
	err := c.db.QueryRowContext(ctx, `SELECT state FROM pair_sessions WHERE id=?`, id).Scan(&st)
	return st, err
}

func (c *Core) pairSessionSetState(ctx context.Context, id, state string) error {
	if c.db == nil {
		return nil
	}
	_, err := c.db.ExecContext(ctx, `UPDATE pair_sessions SET state=? WHERE id=?`, state, id)
	return err
}

// pairSAS derives the 6-digit short authentication string shown on both
// screens. Bound into it: both ephemeral X keys, both Ed25519 identity
// pubs, both nonces, and the DH secret — every value that differs under a
// MITM relay, so mismatched displays are the detection.
func pairSAS(initX, respX, initPub, respPub, initNonce, respNonce, dh []byte) string {
	h := sha256.New()
	h.Write(initPub)
	h.Write(respPub)
	h.Write(initX)
	h.Write(respX)
	h.Write(initNonce)
	h.Write(respNonce)
	h.Write(dh)
	sum := h.Sum(nil)
	v := binary.BigEndian.Uint32(sum[:4]) % 1000000
	return fmt.Sprintf("%03d-%03d", v/1000, v%1000)
}

// pairBoxKey derives the AES-256 key sealing the shared-secret transfer:
// HKDF over the DH secret salted by both session nonces.
func pairBoxKey(dh, initNonce, respNonce []byte) ([]byte, error) {
	salt := append(append([]byte{}, initNonce...), respNonce...)
	key, err := hkdf.Key(sha256.New, dh, salt, "panda-pair/1", 32)
	if err != nil {
		return nil, err
	}
	return key, nil
}

// pairSeal encrypts plaintext under key: box = nonce ‖ AES-256-GCM(ct).
func pairSeal(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plaintext, nil), nil
}

// PairOpen unseals a pair box — exported for the initiator CLI.
func PairOpen(key, box []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(box) < g.NonceSize() {
		return nil, errors.New("pair box too short")
	}
	return g.Open(nil, box[:g.NonceSize()], box[g.NonceSize():], nil)
}

// PairDeriveKey exposes the initiator side of the box key derivation.
func PairDeriveKey(dh, initNonce, respNonce []byte) ([]byte, error) {
	return pairBoxKey(dh, initNonce, respNonce)
}

// PairCode exposes the initiator-side SAS derivation for the CLI.
func PairCode(initX, respX, initPub, respPub, initNonce, respNonce, dh []byte) string {
	return pairSAS(initX, respX, initPub, respPub, initNonce, respNonce, dh)
}

// PairRejected reports a pair_reject frame as an error — the typed
// Reason token ("rejected", "expired", "busy", "already_joined", …) lets
// callers render the cause without parsing prose.
type PairRejected struct{ Reason string }

func (e *PairRejected) Error() string { return "pairing refused: " + e.Reason }

// PairClient is the initiator half of the pairing ceremony, shared by
// `panda pair <target>` and the web console: DialPair performs the
// key exchange and leaves the session waiting on the responder's human;
// WaitSecret blocks until the sealed secret arrives or the answer is no.
type PairClient struct {
	conn    *bus.Conn
	Session string // initiator-minted session id
	Code    string // SAS both screens must show
	// PeerPub is the responder's Ed25519 identity (hex) from pair_ready.
	// A join URI's k= field is checked against it before any secret moves —
	// that check is what makes a bootstrap address a contact point rather
	// than a trusted introducer.
	PeerPub   string
	dh        []byte
	nonce     []byte
	respNonce []byte
}

// DialPair dials dialAddr (bare host:port or ws(s):// URL), sends
// pair_hello, and reads the responder's pair_ready. pub is this node's
// Ed25519 identity (an ephemeral one is fine — it only binds the SAS).
// selfID/name/listenAddr are the display fields the responder's operator
// sees next to the confirm prompt.
func DialPair(ctx context.Context, dialAddr, selfID, name, listenAddr string, pub ed25519.PublicKey) (*PairClient, error) {
	return DialPairTo(ctx, dialAddr, "", selfID, name, listenAddr, pub)
}

// DialPairTo is DialPair with a forwarding target (P4b): when target is
// non-empty the contact point is asked to relay the handshake to that stable
// identity instead of answering it, and the answers come back the same way.
// The contact point never sees a secret — pair_secret is already boxed to the
// DH the two pairing ends derived.
func DialPairTo(ctx context.Context, dialAddr, target, selfID, name, listenAddr string, pub ed25519.PublicKey) (*PairClient, error) {
	dialAddr = normalizeDialableAddr(dialAddr)
	initKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	session, err := newUUID()
	if err != nil {
		return nil, err
	}

	client := bus.NewClient("ws://"+dialAddr+"/ws", nil)
	conn, err := client.Dial(ctx)
	if err != nil {
		return nil, err
	}
	msgID, _ := newUUID()
	hello, err := bus.NewEnvelope(bus.MsgPairHello, selfID, msgID, bus.PairHelloPayload{
		Session: session,
		X:       hex.EncodeToString(initKey.PublicKey().Bytes()),
		Pub:     hex.EncodeToString(pub),
		Nonce:   hex.EncodeToString(nonce),
		Addr:    listenAddr,
		Name:    name,
		NodeID:  selfID,
		Target:  target,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.Send(hello); err != nil {
		conn.Close()
		return nil, err
	}

	var env bus.Envelope
	if err := conn.ReadJSON(&env); err != nil {
		conn.Close()
		return nil, err
	}
	if env.Type == bus.MsgPairReject {
		var rj bus.PairRejectPayload
		_ = env.PayloadInto(&rj)
		conn.Close()
		return nil, &PairRejected{Reason: rj.Reason}
	}
	if env.Type != bus.MsgPairReady {
		conn.Close()
		return nil, fmt.Errorf("unexpected reply %s", env.Type)
	}
	var ready bus.PairReadyPayload
	if err := env.PayloadInto(&ready); err != nil || ready.Session != session {
		conn.Close()
		return nil, errors.New("pair_ready: malformed")
	}
	respX, err := hex.DecodeString(ready.X)
	if err != nil || len(respX) != 32 {
		conn.Close()
		return nil, errors.New("pair_ready: bad key")
	}
	respPub, err := hex.DecodeString(ready.Pub)
	if err != nil || len(respPub) != ed25519.PublicKeySize {
		conn.Close()
		return nil, errors.New("pair_ready: bad pub")
	}
	respNonce, err := hex.DecodeString(ready.Nonce)
	if err != nil || len(respNonce) < 8 {
		conn.Close()
		return nil, errors.New("pair_ready: bad nonce")
	}
	respXKey, err := ecdh.X25519().NewPublicKey(respX)
	if err != nil {
		conn.Close()
		return nil, err
	}
	dh, err := initKey.ECDH(respXKey)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &PairClient{
		conn:      conn,
		Session:   session,
		PeerPub:   hex.EncodeToString(respPub),
		Code:      pairSAS(initKey.PublicKey().Bytes(), respX, pub, respPub, nonce, respNonce, dh),
		dh:        dh,
		nonce:     nonce,
		respNonce: respNonce,
	}, nil
}

// ErrPairExpired marks a pairing session that ended because the responder's
// human never confirmed within pairSessionTTL. The responder closes the
// socket when its own TTL lapses; the initiator's context is deliberately
// longer, so that close arrives as a plain read error while ctx.Err() is
// still nil. Without this sentinel callers see a raw "i/o timeout" and the
// UI's "expired" state is unreachable — the panel reports an error for the
// most ordinary outcome of all: nobody answered.
var ErrPairExpired = errors.New("pairing session expired")

// classifyPairReadError keeps the network error for a genuinely broken
// connection and calls a read failure "expired" only once the wait has
// reached the responder's own session TTL. A socket dropped after five
// seconds is a transport failure, not an abandoned confirmation.
func classifyPairReadError(err error, waited, remoteTTL time.Duration) error {
	if err == nil || waited < remoteTTL {
		return err
	}
	return fmt.Errorf("%w: %w", ErrPairExpired, err)
}

// WaitSecret blocks on the responder's human answer: pair_secret returns
// the mesh secret in plaintext bytes; pair_reject returns *PairRejected;
// ctx cancellation or a dropped conn returns the underlying error. The
// responder's session TTL bounds the wait on its side, so the caller's
// ctx only needs a margin past it.
func (c *PairClient) WaitSecret(ctx context.Context) (string, error) {
	started := time.Now()
	done := make(chan struct{})
	defer close(done)
	guard.Go(nil, "pair: wait secret watcher", nil, func() {
		select {
		case <-ctx.Done():
			c.conn.Close()
		case <-done:
		}
	})
	for {
		var env bus.Envelope
		if err := c.conn.ReadJSON(&env); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", classifyPairReadError(err, time.Since(started), pairSessionTTL)
		}
		switch env.Type {
		case bus.MsgPairReject:
			var rj bus.PairRejectPayload
			_ = env.PayloadInto(&rj)
			return "", &PairRejected{Reason: rj.Reason}
		case bus.MsgPairSecret:
			var ps bus.PairSecretPayload
			if err := env.PayloadInto(&ps); err != nil || ps.Session != c.Session {
				continue
			}
			key, err := pairBoxKey(c.dh, c.nonce, c.respNonce)
			if err != nil {
				return "", err
			}
			box, err := base64.StdEncoding.DecodeString(ps.Box)
			if err != nil {
				return "", err
			}
			plain, err := PairOpen(key, box)
			if err != nil {
				return "", errors.New("pair box integrity check failed")
			}
			return string(plain), nil
		}
	}
}

// Close drops the pairing conn — the responder treats it as an abandoned
// session and expires the row.
func (c *PairClient) Close() { c.conn.Close() }

// PairSessionRow is the operator-facing view of an inbound pairing request.
type PairSessionRow struct {
	ID        string
	PeerAddr  string
	PeerName  string
	PeerPub   string
	SAS       string
	State     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// ListPairSessions reads pair_sessions for `panda pair` — newest first,
// expired/terminal rows included so the display can say what happened.
func ListPairSessions(db *sql.DB) ([]PairSessionRow, error) {
	rows, err := db.Query(
		`SELECT id, peer_addr, peer_name, peer_pub, sas, state, created_at, expires_at
		 FROM pair_sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PairSessionRow
	for rows.Next() {
		var r PairSessionRow
		var ca, ea int64
		if err := rows.Scan(&r.ID, &r.PeerAddr, &r.PeerName, &r.PeerPub, &r.SAS, &r.State, &ca, &ea); err != nil {
			return nil, err
		}
		r.CreatedAt, r.ExpiresAt = time.Unix(ca, 0), time.Unix(ea, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PairPendingRow is one inbound request a human still has to answer: state
// 'ready' and still inside its window.
//
// It is deliberately narrower than PairSessionRow. The listing surfaces
// (`panda pair`, the console's pairing card) want every row including the
// terminal ones so they can say what happened; every *notification* surface —
// the TUI banner, the console's floating request card, the `status` hint —
// wants only the rows that can still be acted on. Asking the question in SQL
// keeps those three from each re-deriving "is this still answerable" and
// drifting apart from the sweep that expires rows.
type PairPendingRow struct {
	ID        string
	PeerName  string
	PeerAddr  string
	SAS       string
	ExpiresAt time.Time
}

// PairPending lists the inbound requests that are still answerable, newest
// first.
func PairPending(db *sql.DB) ([]PairPendingRow, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(
		`SELECT id, peer_name, peer_addr, sas, expires_at FROM pair_sessions
		 WHERE state=? AND expires_at>? ORDER BY created_at DESC`,
		PairStateReady, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PairPendingRow
	for rows.Next() {
		var r PairPendingRow
		var ea int64
		if err := rows.Scan(&r.ID, &r.PeerName, &r.PeerAddr, &r.SAS, &ea); err != nil {
			return nil, err
		}
		r.ExpiresAt = time.Unix(ea, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PairFingerprint digests the pairing table's (id, state) pairs so a console
// watching the change feed learns that somebody knocked — and that the request
// was answered or expired — without polling the endpoint on its own timer.
//
// The SAS and the peer name are left out on purpose: the digest only has to
// answer "did the set of requests change", and hashing the presented code
// would make the fingerprint flip on data no renderer keys on.
func PairFingerprint(db *sql.DB) string {
	if db == nil {
		return ""
	}
	rows, err := db.Query(`SELECT id, state FROM pair_sessions ORDER BY id`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	sum := sha256.New()
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return ""
		}
		sum.Write([]byte(id))
		sum.Write([]byte{':'})
		sum.Write([]byte(state))
		sum.Write([]byte{';'})
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

// AnswerPairSession flips a ready session to confirmed or rejected — the
// responder CLI's side of the human decision. ID may be a unique prefix so
// the operator can answer with the displayed short id.
func AnswerPairSession(db *sql.DB, idPrefix, state string) error {
	var id string
	err := db.QueryRow(`SELECT id FROM pair_sessions WHERE id LIKE ? AND state=?`,
		idPrefix+"%", PairStateReady).Scan(&id)
	if err != nil {
		return fmt.Errorf("no ready pairing session matching %q", idPrefix)
	}
	res, err := db.Exec(`UPDATE pair_sessions SET state=? WHERE id=? AND state=?`,
		state, id, PairStateReady)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("pairing session %s already resolved", id)
	}
	return nil
}

// PairSessionState reads one session's state — the confirm CLI polls it to
// show "the daemon finished the join" feedback.
func PairSessionState(db *sql.DB, idPrefix string) (string, error) {
	var st string
	err := db.QueryRow(`SELECT state FROM pair_sessions WHERE id LIKE ?`, idPrefix+"%").Scan(&st)
	return st, err
}

// resolvePairAddr fills an unroutable or empty host in the initiator's
// advertised address with the source IP of the conn — the same rule LAN
// beacons use, since "where the datagram came from" is the only usable
// hint for a wildcard listener.
func resolvePairAddr(advertised, srcIP string) string {
	host, port, err := net.SplitHostPort(advertised)
	if err != nil || port == "" {
		return ""
	}
	ip := net.ParseIP(host)
	if host == "" || (ip != nil && (ip.IsUnspecified() || ip.IsLoopback())) {
		return net.JoinHostPort(srcIP, port)
	}
	return advertised
}

// normalizeDialableAddr strips a ws/wss scheme from an address before it is
// stored as a peer entry — the peer list takes bare host:port.
func normalizeDialableAddr(addr string) string {
	addr = strings.TrimPrefix(addr, "ws://")
	addr = strings.TrimPrefix(addr, "wss://")
	addr = strings.TrimSuffix(addr, "/ws")
	return addr
}
