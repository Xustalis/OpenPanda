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
	expires   time.Time
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
	var p bus.PairHelloPayload
	if err := env.PayloadInto(&p); err != nil || p.Session == "" {
		c.logger.Warn("pair_hello: malformed", "err", err)
		return
	}
	if conn.PeerID() != "" {
		// A peer that already hellos is already inside — pairing would only
		// re-send the secret it demonstrably holds.
		c.sendPairReject(conn, p.Session, "already_joined")
		return
	}
	// Initiator's view of its own listen address; when it advertises a
	// wildcard/loopback host the responder substitutes the source IP of
	// this conn — the same "dial me where you saw me" rule beacons use.
	srcIP, _, _ := strings.Cut(conn.RemoteAddr(), ":")

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
		c.sendPairReady(conn, sess)
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
		expires:   time.Now().Add(pairSessionTTL),
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
	_ = conn.ResetReadDeadline()
	c.sendPairReady(conn, sess)
	go c.waitPairConfirm(ctx, sess, dh)
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
	if sess != nil && sess.conn == conn {
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
			c.sendPairReject(sess.conn, sess.id, "expired")
			_ = sess.conn.Close()
			return
		}
		state, err := c.pairSessionState(ctx, sess.id)
		if err != nil {
			// Row vanished (operator cleanup, db close): treat as abort.
			c.pairing.mu.Lock()
			delete(c.pairing.sessions, sess.id)
			c.pairing.mu.Unlock()
			_ = sess.conn.Close()
			return
		}
		switch state {
		case PairStateConfirmed:
			if err := c.pairAccept(ctx, sess, dh); err != nil {
				c.logger.Warn("pair: accept failed", "session", sess.id, "err", err)
				c.sendPairReject(sess.conn, sess.id, "accept_failed")
			} else {
				_ = c.pairSessionSetState(ctx, sess.id, PairStateDone)
				c.logger.Info("pair: joined", "peer", sess.initName, "addr", sess.initAddr)
			}
			_ = sess.conn.Close()
			return
		case PairStateRejected:
			c.sendPairReject(sess.conn, sess.id, "rejected")
			_ = sess.conn.Close()
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
	c.mu.RLock()
	secret := c.sharedSecret
	c.mu.RUnlock()
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
	if err := sess.conn.Send(env); err != nil {
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
func (c *Core) sendPairReady(conn *bus.Conn, sess *pairSession) {
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
		_ = conn.Send(env)
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
	if err == nil {
		_ = conn.Send(env)
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
		s.conn.Close()
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
