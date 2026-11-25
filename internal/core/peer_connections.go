// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/version"
	"time"
)

// SetLimits configures the global and per-IP inbound connection limits. A
// limit <= 0 disables that limit.
func (c *Core) SetLimits(maxConns, maxConnsPerIP int) {
	c.maxConns = maxConns
	c.maxConnsPerIP = maxConnsPerIP
}

// Listen starts the WebSocket server and accepts connections. Blocks until
// ctx is done.
func (c *Core) Listen(ctx context.Context, addr string) error {
	c.listenAddr.Store(addr)
	srv := bus.NewServer(addr, c.logger, func(conn *bus.Conn) {
		// guard.Call, not a goroutine: the server accounts connection limits
		// from this blocking callback, so wrapping must not change that —
		// it only keeps one peer's panic from taking the daemon down.
		guard.Call(c.logger, "core: inbound "+conn.RemoteAddr(), nil, func() {
			c.handleInbound(ctx, conn)
		})
	})
	// The bind may choose a different address than the one asked for — a
	// ":0" listener gets an ephemeral port — and hello payloads plus
	// discovery beacons must advertise what peers can actually dial. The
	// configured address stays as the pre-bind value so a node that is still
	// starting describes itself instead of advertising an empty string.
	srv.OnBound(func(bound string) { c.listenAddr.Store(bound) })
	srv.SetLimits(c.maxConns, c.maxConnsPerIP)
	return srv.Listen(ctx)
}

// ListenAddr reports the address this node's WebSocket listener is bound to,
// or the configured address before the bind has happened. It is what the
// capability summary and discovery beacons advertise.
func (c *Core) ListenAddr() string {
	addr, _ := c.listenAddr.Load().(string)
	return addr
}

// handleInbound runs the read loop for a connection (server- or client-
// side). It also starts the transport ping loop so idle connections stay
// alive and dead peers are detected by pong timeout.
func (c *Core) handleInbound(ctx context.Context, conn *bus.Conn) {
	defer func() {
		// Remove any peer entry that used this conn so sendTo rejects it and
		// the reconnect loop can re-dial.
		c.removePeerForConn(conn)
		conn.Close()
	}()
	c.goGuarded("conn ping loop "+conn.PeerID(), func() { conn.StartPingLoop(ctx, 30*time.Second) })
	for {
		// A cancelled ctx (shutdown, conn teardown ordered elsewhere) ends the
		// loop even when ReadJSON is still blocked: the peer's ping/close will
		// unblock it, and the deadline keeps a wedged read bounded.
		if ctx.Err() != nil {
			return
		}
		var env bus.Envelope
		if err := conn.ReadJSON(&env); err != nil {
			c.logger.Debug("inbound read closed", "err", err)
			return
		}
		// Identity binding (docs/design/routing-and-scheduling.md): the hello handshake binds an
		// authenticated node id to this conn. Before binding, only hello is
		// accepted; after binding, every message must carry the bound id or the
		// sender is spoofed and the conn is dropped.
		if id := conn.PeerID(); id != "" {
			if env.From != id {
				c.logger.Warn("spoofed sender on connection", "bound", id, "from", env.From)
				return
			}
		} else if env.Type != bus.MsgHello && env.Type != bus.MsgHelloReject && !isPairMessage(env.Type) {
			// The pairing frames and the hello_reject verdict are the only
			// pre-hello traffic besides the hello itself — pair_hello runs a
			// sealed DH exchange so the unauthenticated channel it rides
			// cannot leak the secret, and hello_reject carries only a fixed
			// reason code that is worth more to the rejected peer than the
			// nothing it reveals to anyone else. MsgTransit is rejected here
			// too: pair-over-transit (P4b) forwards pair frames from an
			// ALREADY-authenticated contact point, so a transit envelope on
			// an unauthenticated conn has no legitimate sender.
			c.logger.Warn("message before hello", "type", env.Type, "from", env.From)
			return
		}
		c.dispatch(ctx, conn, env)
		// Refresh the keepalive deadline once the conn is authenticated — on
		// every frame, not just on pongs. Pongs only get processed while this
		// loop sits in ReadJSON, so a long dispatch or a slow sender alone
		// could starve the refresh and kill a healthy conn at pongWait; any
		// inbound frame proves transport liveness just as well. A dead peer
		// still stops answering our pings and expires on schedule. A conn that
		// is NOT yet authenticated keeps its short hello deadline: a rejected
		// hello must not earn the long keepalive window.
		if conn.PeerID() != "" {
			_ = conn.ResetReadDeadline()
		}
	}
}

// removePeerForConn deletes any peer whose conn matches the given one and
// marks it offline in the local capability directory so routing stops
// considering it.
func (c *Core) removePeerForConn(conn *bus.Conn) {
	c.mu.Lock()
	// Drop the per-conn greeting marker: a reconnect arrives on a NEW conn,
	// which must get a fresh hello reply to bind our identity. The eviction
	// marker goes too — the conn object can be GC'd and its address reused
	// by a future conn, which must not inherit "already dead".
	delete(c.greetedConns, conn)
	delete(c.evictedConns, conn)
	var gone []string
	for id, p := range c.peers {
		if p.conn == conn {
			delete(c.peers, id)
			gone = append(gone, id)
		}
	}
	c.mu.Unlock()
	for _, id := range gone {
		c.logger.Info("peer disconnected", "peer", id)
		c.mu.Lock()
		delete(c.peerBlocked, id)
		c.mu.Unlock()
		// Liveness writes belong to the row's owner: a borrowed engine whose
		// transient conn to this peer just closed must not flip the shared
		// row — the owning daemon's own conn to the same peer is still live,
		// and marking it offline here would blind the daemon's routing until
		// the next hello restores it.
		if c.ownsNodeRow {
			if err := ledger.MarkOffline(c.db, id); err != nil {
				c.logger.Warn("mark peer offline", "peer", id, "err", err)
			}
		}
	}
	if len(gone) > 0 {
		c.refreshSelfNeighbors(context.Background())
	}
}

// DialPeer connects outbound to addr, sends hello, and starts reading in the
// background. It returns once the connection is established (or fails); the
// read loop keeps the connection alive. Short-lived schedulers (the ask CLI)
// use this so they can dial every peer and then proceed to routing. A daemon
// that must hold the connection and reconnect on drop should use MaintainPeer.
//
// The ctx bounds the DIAL, not the connection. A caller that wants a short
// dial budget (the reachability ladder) must not hand that budget to the
// read loop: cancelling it the moment the dial returns would close the
// connection before the peer's hello reply was ever read, and the edge
// would die on arrival. See dialPeerWithLife.
func (c *Core) DialPeer(ctx context.Context, addr string) error {
	return c.dialPeerWithLife(ctx, context.WithoutCancel(ctx), addr)
}

// dialPeerWithLife connects with a dial budget (ctx) and serves the
// resulting connection under a separate lifetime (life). The two are different
// clocks: the budget bounds the attempt, the lifetime bounds the edge.
func (c *Core) dialPeerWithLife(dialCtx, life context.Context, addr string) error {
	conn, err := c.dial(dialCtx, addr)
	if err != nil {
		return err
	}
	c.goGuarded("inbound read "+conn.PeerID(), func() { c.handleInbound(life, conn) })
	return nil
}

// MaintainPeer connects outbound to addr and blocks serving the connection
// until the peer EDGE is down, not just our conn: after a mutual-dial dedup
// (see ensurePeer) our outbound conn may be the loser while the peer's own
// inbound conn keeps the edge alive — redialing then would only recreate the
// one-second flap the dedup exists to stop. A nil return means the edge
// dropped (or ctx is done) and the caller may redial; a non-nil return means
// the dial (or hello) failed.
func (c *Core) MaintainPeer(ctx context.Context, addr string) error {
	conn, err := c.dial(ctx, addr)
	if err != nil {
		return err
	}
	c.handleInbound(ctx, conn)
	// The peer answered our hello with hello_reject and closed us: that is a
	// refusal, not a dropped healthy link. Report it as a dial failure so the
	// maintain loop takes the exponential backoff path — two nodes with
	// mismatched secrets otherwise redial each other every second forever.
	if reason := conn.RejectReason(); reason != "" {
		if reason == "binding" {
			return ErrHelloBinding
		}
		return fmt.Errorf("%w: %s", ErrAuthRejected, reason)
	}
	// Our outbound conn ended. If the peer still reaches us on its own conn,
	// wait for that conn to die too before handing control back.
	//
	// A nil connFor at the first check is not proof the edge is gone: in a
	// mutual dial our outbound can lose the dedup and be torn down while the
	// peer's surviving conn is still mid-handshake on our side — checking
	// once at that instant reads "no peer" and returns, and the caller
	// redials into the exact one-second flap the dedup exists to stop. So
	// absent conns are waited out through a short grace window; a peer that
	// is genuinely gone costs at most that window before the redial.
	if id := conn.PeerID(); id != "" {
		graceUntil := time.Now().Add(2 * time.Second)
		for c.connFor(id) != nil || time.Now().Before(graceUntil) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return nil
}

// helloBindTarget resolves the node id behind a dial address from the
// addr→id bindings the last successful hello recorded. Empty when the address
// has never produced a hello, when the lookup fails, or when it names this
// node; the hello then goes out unbound and the receiver's legacy path
// applies. The cost is one small table read per dial, and a dial only happens
// on connect or reconnect (once per backoff step), never per message.
func (c *Core) helloBindTarget(addr string) string {
	if c.db == nil {
		return ""
	}
	bindings, err := ledger.PeerAddrBindings(c.db)
	if err != nil {
		return ""
	}
	id := bindings[NormalizeDialAddr(addr)]
	if id == "" || id == c.nodeID {
		return ""
	}
	return id
}

// dial establishes a WS connection to addr and sends hello, advertising this
// node's capability card. The caller owns the returned conn and must read it
// (synchronously via handleInbound, or asynchronously via go handleInbound).
func (c *Core) dial(ctx context.Context, addr string) (*bus.Conn, error) {
	// The cleartext gate runs AFTER the hello exchange now, not before the
	// socket opens: ws:// to a peer that negotiates sessaead is encrypted
	// and safe on any network, so the socket must open for the caps to be
	// seen. A peer that cannot encrypt still hits the policy post-hello —
	// on the wire the only exposure is the two (already HMAC-signed) hello
	// envelopes, never task traffic.
	u := normalizeWSURL(addr)
	client := bus.NewClient(u, c.logger)
	conn, err := client.Dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	// Locally-initiated: peer dedup needs the direction (see ensurePeer).
	conn.MarkOutbound()
	conn.SetDialAddr(addr)
	// Send hello to identify ourselves and advertise our capability card.
	card, err := c.helloCard()
	if err != nil {
		conn.Close()
		return nil, err
	}
	msgID, err := newUUID()
	if err != nil {
		conn.Close()
		return nil, err
	}
	ts := time.Now().Unix()
	nonce, err := newUUID()
	if err != nil {
		conn.Close()
		return nil, err
	}
	// Address the hello at the node behind this address when the last
	// successful hello recorded the binding. The receiver refuses a hello
	// that names a different node, which is what stops a member from
	// replaying our hello at a third peer; an address we have never seen
	// produce a hello has no id to name, and goes out unbound.
	to := c.helloBindTarget(addr)
	hello := bus.HelloPayload{
		NodeID:        c.nodeID,
		Ver:           version.Version,
		Prov:          wireProvenance(),
		Card:          card,
		Ts:            ts,
		Nonce:         nonce,
		Sig:           bus.HelloSigN(c.sharedSecretValue(), c.nodeID, ts, nonce),
		To:            to,
		UDPPort:       c.UDPPort(),
		Caps:          c.helloCaps(),
		Authoritative: c.edgeAuthority.Load(),
	}
	if to != "" {
		hello.SigB = bus.HelloSigB(c.sharedSecretValue(), c.nodeID, to, ts, nonce)
	}
	conn.SetLocalNonce(nonce)
	c.signHello(&hello, ts, nonce)
	env, err := bus.NewEnvelope(bus.MsgHello, c.nodeID, msgID, hello)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.Send(env); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send hello: %w", err)
	}
	// Our hello already went out on this conn, so the peer's reply must not
	// trigger a reply-to-the-reply: mark it greeted up front. Without this
	// the dial side minted a second hello (and nonce) on every connect, and
	// the session KDF could not tell which nonce pair keyed the link.
	c.mu.Lock()
	c.greetedConns[conn] = true
	c.mu.Unlock()
	// The hello is on the wire, but admission is asynchronous: the peer can
	// still reject it (bad signature, clock skew, wrong identity). Calling
	// this "connected" made an unauthenticated socket look like a formed
	// link in INFO logs — the false positive that hid a secret mismatch for
	// hours while auto-dial kept retrying.
	c.logger.Debug("hello sent to peer; awaiting authentication", "peer", addr)
	return conn, nil
}

// ensurePeer registers conn under id. A repeated hello on the SAME conn is a
// no-op. A hello on a NEW conn takes over in two distinct situations:
//
//   - Same direction (both conns locally-initiated, or both inbound): the
//     fresh, authenticated hello proves liveness, while the old conn may be a
//     half-dead socket whose read loop has not noticed yet (P1-7). Without
//     replacement a reconnecting peer could never reclaim its identity —
//     sends kept going to the dead conn, and when the dead conn's read loop
//     finally exited, removePeerForConn would delete the identity even though
//     the live replacement conn was right there.
//
//   - Opposite directions: two nodes dialed each other simultaneously, so
//     each side holds one outbound and one inbound conn to the same peer.
//     Both cannot survive — the second registration used to close the first,
//     whose MaintainPeer redialed a second later, replacing the other side's
//     conn in turn: an endless one-second connect/disconnect flap that
//     churned the capability directory offline/online. The tie-break is
//     deterministic: the conn initiated by the lexicographically smaller
//     node id wins, and both sides compute the same winner, so exactly one
//     TCP conn survives and both registries agree.
//
// The old conn on a replacement is closed AFTER the swap and outside the
// mutex; its read-loop cleanup calls removePeerForConn(oldConn), which
// matches by conn identity — the registry now holds the new conn, so the new
// registration survives. A losing (deduped) conn is closed by the hello
// handler after the identity-binding reply that handleHello already sent on
// it (greeting is per-conn, so the loser's first hello always earned one), so
// the losing dialer can bind our identity and quiesce instead of redialing
// blind.
//
// Returns accepted=false when this conn lost the mutual-dial tie-break and
// must not be registered; true otherwise. auth carries the peer's declared
// edge authority from its hello (process holds the node identity lock).
func (c *Core) ensurePeer(id string, conn *bus.Conn, auth bool) (accepted bool) {
	c.mu.Lock()
	if _, dead := c.evictedConns[conn]; dead {
		// This conn already lost arbitration once — its read loop is only
		// still talking because the socket had frames buffered before the
		// close. Eviction is permanent: let the replacement keep the edge.
		c.mu.Unlock()
		return false
	}
	old := c.peers[id]
	if old != nil && old.conn == conn {
		c.mu.Unlock()
		return true
	}
	if old != nil && old.conn.Outbound() != conn.Outbound() {
		// Opposite directions: deterministic mutual-dial dedup. The winner
		// is the conn initiated by the smaller node id.
		if conn.Outbound() != (c.nodeID < id) {
			c.mu.Unlock()
			c.logger.Info("peer connection deduped", "peer", id)
			return false
		}
	} else if old != nil && c.peerLivenessWindow > 0 && old.conn.LiveWithin(c.peerLivenessWindow) &&
		!(auth && !old.authoritative) {
		// Same direction, incumbent still demonstrably alive: the new conn is
		// a same-id SIBLING SESSION, not a reconnect — a CLI/TUI engine shares
		// the daemon's stable node id, so every `panda` invocation used to
		// kick the daemon's edge conn here (and the daemon's redial kicked
		// the CLI's), producing the periodic connect/disconnect flap that
		// stranded in-flight delegates. A live incumbent keeps the edge; the
		// newcomer still got its identity-binding hello reply above, so the
		// losing side quiesces the same way a mutual-dial loser does. Only a
		// conn silent past the keepalive bound falls through to replacement,
		// preserving the half-dead-socket takeover this branch exists for.
		//
		// One exception ranks the sibling check itself: a newcomer whose
		// hello claims edge authority (its process holds the node identity
		// lock — the real daemon) reclaims the edge from a live but
		// non-authoritative incumbent. That incumbent is a stray sibling that
		// claimed the identity while the daemon was down; left alone it
		// answers keepalives forever and delegated work lands on the wrong
		// engine. Two authoritative claimants fall back to the liveness
		// rule — duplicate daemons on one identity are a misconfiguration no
		// tie-break can fix.
		c.mu.Unlock()
		c.logger.Info("peer connection held by live same-id session", "peer", id)
		return false
	}
	c.peers[id] = &Peer{id: id, conn: conn, authoritative: auth}
	if old != nil {
		// Mark the loser before releasing the lock: a frame its read loop
		// already buffered must find the eviction recorded by the time the
		// dispatch thread re-enters ensurePeer.
		c.evictedConns[old.conn] = struct{}{}
	}
	n := len(c.peers)
	c.mu.Unlock()

	if old != nil {
		c.logger.Info("peer connection replaced", "peer", id)
		old.conn.Close()
	}
	c.logger.Info("peer registered", "peer", id, "active", n)
	c.refreshSelfNeighbors(context.Background())
	return true
}

// refreshSelfNeighbors writes this node's live peer set, the measured cost
// of each edge, and its configured contact plan into its own directory row's
// neighbors_json/links_json/contacts_json: the self-edge of the weighted
// link-state graph the routing layer's multi-hop and earliest-arrival
// searches read (docs/design/routing-and-scheduling.md). Best-effort: a failed write just leaves
// the last advertisement in place.
func (c *Core) refreshSelfNeighbors(ctx context.Context) {
	if c.db == nil {
		return
	}
	raw, err := json.Marshal(c.livePeerIDs())
	if err != nil {
		return
	}
	links, err := json.Marshal(c.linkMetrics())
	if err != nil {
		return
	}
	contacts, err := json.Marshal(c.contacts)
	if err != nil {
		return
	}
	row := c.nodeID
	if base, ok := scheduler.EphemeralBase(c.nodeID); ok {
		row = base
	}
	// Read-compare-write: the 5s monitor tick made this one of the node's
	// busiest idle writers, yet the trio almost never changes between ticks —
	// comparing first turns twelve unconditional UPDATEs a minute into one
	// cheap SELECT while staying honest about a row another process rewrote
	// (e.g. a REPL engine's Register upsert blanking the columns).
	if err := ledger.UpdateAdjacencyIfChanged(c.db, row, string(raw), string(links), string(contacts), ""); err != nil {
		c.logger.Debug("refresh self neighbors", "err", err)
	}
	// The advertised dial set lives on the same row (M3). Almost never
	// changes — the update is diff-gated like the adjacency write above.
	if addrs, err := json.Marshal(c.selfAddrs()); err == nil {
		if err := ledger.UpdateAddrsIfChanged(c.db, row, string(addrs)); err != nil {
			c.logger.Debug("refresh self addrs", "err", err)
		}
	}
}

// connFor returns the connection associated with from, if any.
func (c *Core) connFor(from string) *bus.Conn {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p, ok := c.peers[from]; ok {
		return p.conn
	}
	return nil
}
