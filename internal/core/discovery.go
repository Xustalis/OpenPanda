// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// LAN discovery (roadmap Track 1): a UDP broadcast beacon — "a panda node
// lives at this address" — plus the pending list it feeds. The beacon is a
// HINT, never proof of membership: it carries no secret and it cannot admit
// anyone. Admission is unchanged: the operator runs `panda nodes add`/`pair`,
// and the first authenticated hello (shared secret + Ed25519) is still what
// proves the peer.
//
// The beacon IS Ed25519-signed over its asserted fields (id, addr, ver, pub,
// ts) — not to prove membership (a stranger can mint a keypair too) but to
// pin the advertised fingerprint to the advertised address: without the
// signature a LAN peer could broadcast a victim's node id + pubkey next to
// its own address, show the operator a familiar fingerprint, and harvest the
// signed hello our dial emits (replayable at the real node inside its
// freshness window). A row whose signature verifies is recorded as verified;
// a v1 beacon with no signature still lands, flagged unsigned; a beacon
// carrying a BROKEN signature is tampering and is dropped outright — the
// same fail-closed posture the hello applies to a half-present identity.
//
// Design constraints this file keeps:
//   - nothing secret in a datagram: shared_secret NEVER leaves the wire format
//   - nothing trusted from a datagram: rows land in pending_nodes, not
//     employee_cache; a forged beacon can only litter the hint list, and a
//     TTL sweep clears litter on its own. Verified rows evict unverified
//     ones first under the cap, so a flood of unsigned beacons displaces
//     noise before signal
//   - nothing surprising: a node on loopback-only announces nothing (nothing
//     can reach it), and a node without a shared secret cannot pair anyway,
//     so it does not broadcast either

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net"
	"strconv"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/version"
)

const (
	// beaconMagic is the wire marker every discovery datagram must start with.
	// The value embeds a version so a future format does not parse as this
	// one by accident.
	beaconMagic = "panda-beacon/1"

	// beaconInterval is how often a discoverable node announces itself.
	// pendingTTL tolerates ~3 missed beacons before the hint expires —
	// transient loss must not flicker the list, permanent silence must
	// clear it.
	beaconInterval = 15 * time.Second
	pendingTTL     = 60 * time.Second

	// beaconMaxBytes caps a datagram a listener will parse. The format is
	// ~150 bytes; anything bigger is not a beacon worth reading.
	beaconMaxBytes = 1024

	// pendingCap bounds the hint table: on a hostile LAN anyone can forge
	// beacons, and an unbounded table is an unbounded write target. Full
	// means evict the stalest row — a flood may still evict a legit node,
	// but a bounded list degrades to noise, not to a resource leak.
	pendingCap = 64

	// autoDialTick/autoDialCooldown pace the discovery auto-dial pass: fresh
	// src-pinned candidates are tried on the first tick after their beacon;
	// failures re-arm only after the cooldown so a wrong-secret neighbour or
	// a flapping host does not own the dial path. The cadence deliberately
	// sits below pendingTTL so a silent node stops being dialed as soon as
	// its beacons stop.
	autoDialTick     = 5 * time.Second
	autoDialCooldown = 60 * time.Second
)

// autoDialCand is a beacon-origin dial candidate: an address the receiver
// resolved to the datagram's own source IP (src-pinned), lastSeen for TTL,
// lastDial for the failure cooldown, and gateWarned so a cleartext refusal
// logs its remediation hint once per candidate instead of once per pass.
type autoDialCand struct {
	addr       string
	lastSeen   time.Time
	lastDial   time.Time
	gateWarned bool
}

// Beacon is the discovery datagram's payload. Every field is self-asserted:
// Pub exists so the pending list can show a fingerprint the operator can
// compare BEFORE pairing, not so the receiver can trust it. Sig, when
// present, is the broadcaster's Ed25519 signature over the asserted fields —
// it makes the fingerprint unfakeable without claiming membership (the
// signing key is self-asserted too: anyone can mint one, nobody can mint
// someone else's). TS is signed display metadata, not a freshness gate —
// nodes without reliable clocks must not lose discovery over it.
type Beacon struct {
	Panda string `json:"panda"` // beaconMagic
	ID    string `json:"id"`
	Addr  string `json:"addr"` // WS listen addr as configured (host may be empty)
	Ver   string `json:"ver"`
	Pub   string `json:"pub"`           // Ed25519 pub hex — display hint, not auth
	TS    int64  `json:"ts,omitempty"`  // announce time; bound into Sig
	Sig   string `json:"sig,omitempty"` // Ed25519 over id:addr:ver:pub:ts
}

// marshalBeacon renders the datagram. The advertised addr keeps whatever
// host the operator configured — including an empty one: the receiver
// substitutes the source IP when the host is unspecified or loopback. A nil
// priv emits the unsigned v1 form (a node whose identity key could not
// materialize still announces — it just earns no verified mark).
func marshalBeacon(id, listenAddr string, pub ed25519.PublicKey, priv ed25519.PrivateKey) []byte {
	b := Beacon{
		Panda: beaconMagic,
		ID:    id,
		Addr:  listenAddr,
		Ver:   version.Version,
		Pub:   hex.EncodeToString(pub),
	}
	if priv != nil {
		b.TS = time.Now().Unix()
		b.Sig = bus.SignBeacon(priv, b.ID, b.Addr, b.Ver, b.Pub, b.TS)
	}
	raw, err := json.Marshal(b)
	if err != nil || len(raw) > beaconMaxBytes {
		return nil
	}
	return raw
}

// parseBeacon validates a datagram's SHAPE. It refuses anything that is not
// a well-formed small beacon: wrong magic, oversized id, a port that is not
// a port, a pubkey that is not hex — and a signature field that is not a
// 64-byte hex blob, or that arrives with no key to verify against. Shape is
// all this checks; beaconVerified answers whether the sig actually signs the
// fields. A refused datagram is dropped silently — LAN noise is not a log
// stream's business.
func parseBeacon(raw []byte) (Beacon, bool) {
	var b Beacon
	if len(raw) == 0 || len(raw) > beaconMaxBytes {
		return b, false
	}
	if err := json.Unmarshal(raw, &b); err != nil || b.Panda != beaconMagic {
		return b, false
	}
	if b.ID == "" || len(b.ID) > 128 || len(b.Addr) > 128 || len(b.Ver) > 32 || len(b.Pub) > 128 || len(b.Sig) > 160 {
		return b, false
	}
	host, port, err := net.SplitHostPort(b.Addr)
	if err != nil || len(host) > 64 {
		return b, false
	}
	if p, err := strconv.Atoi(port); err != nil || p <= 0 || p > 65535 {
		return b, false
	}
	if b.Pub != "" {
		if dec, err := hex.DecodeString(b.Pub); err != nil || len(dec) != ed25519.PublicKeySize {
			return b, false
		}
	}
	if b.Sig != "" {
		// A signature with no advertised key signs nothing — malformed, and
		// indistinguishable from a tamper probe. Fail closed.
		if b.Pub == "" {
			return b, false
		}
		if dec, err := hex.DecodeString(b.Sig); err != nil || len(dec) != ed25519.SignatureSize {
			return b, false
		}
	}
	return b, true
}

// beaconVerified reports whether the beacon's signature genuinely signs its
// asserted fields under its advertised key. Unsigned beacons are not
// "unverified" in the failure sense — they are the v1 form — so the caller
// distinguishes three states: unsigned (record, flagged), signed-and-valid
// (record, marked verified), signed-and-broken (drop: a well-formed key and
// a bad signature is tampering, never a keyless node that grew a sig field).
func beaconVerified(b Beacon) bool {
	pub, err := hex.DecodeString(b.Pub)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	return bus.VerifyBeaconSig(ed25519.PublicKey(pub), b.ID, b.Addr, b.Ver, b.Pub, b.TS, b.Sig)
}

// resolveBeaconAddr fills in the host the sender could not know: an empty,
// wildcard or loopback host means "dial me where you saw me" — which for a
// LAN broadcast is exactly the source IP of the datagram.
func resolveBeaconAddr(b Beacon, src net.IP) string {
	host, port, _ := net.SplitHostPort(b.Addr)
	if ip := net.ParseIP(host); host != "" && ip == nil {
		return b.Addr // a DNS name the sender chose on purpose
	} else if host != "" && !ip.IsUnspecified() && !ip.IsLoopback() {
		return b.Addr // an explicit routable IP — trust it
	}
	return net.JoinHostPort(src.String(), port)
}

// RunDiscovery owns the LAN-discovery socket: one ListenUDP serves both the
// beacon announcer (a ticker WriteToUDP to the subnet broadcast) and the
// listener feeding pending_nodes. bindAddr supplies only the port — the
// socket binds the wildcard address regardless, because a socket bound to a
// single interface address cannot hear broadcasts aimed at the others.
// advertiseAddr is this node's WS listen address as configured — receivers
// substitute the source IP when its host is not routable. autoDial enables
// the zero-config half of discovery: src-pinned beacons get an outbound
// DialPeer so same-secret nodes connect without `nodes admit` — the hello's
// shared secret remains the only admission proof. Runs until ctx ends;
// socket or broadcast failure degrades to warn-and-idle, never daemon
// death — discovery is convenience, not substrate. IPv4 only: broadcast is
// an IPv4 concept (v6 multicast discovery is a later track).
func (c *Core) RunDiscovery(ctx context.Context, bindAddr, advertiseAddr string, autoDial bool) {
	// ResolveUDPAddr, not SplitHostPort+Atoi: it also vets the port — a
	// non-numeric port ("discovery_addr: \":abc\"") must die here with a
	// warning rather than silently bind an ephemeral port and announce to
	// port 0, which is what a bare Atoi-then-0 would do.
	uaddr, err := net.ResolveUDPAddr("udp", bindAddr)
	if err != nil {
		c.logger.Warn("discovery: bad bind addr", "addr", bindAddr, "err", err)
		return
	}
	port := uaddr.Port
	// udp4, not udp: an AF_INET6 socket does not receive IPv4 broadcast
	// datagrams (dual-stack covers unicast, not broadcast), so a "udp"
	// listener binds happily and then never hears a single beacon. The
	// beacon wire is IPv4-only by design, so the v4 wildcard is the
	// correct socket.
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		c.logger.Warn("discovery: listener failed", "addr", bindAddr, "err", err)
		return
	}
	defer conn.Close()
	// ReadFromUDP blocks; closing the socket on shutdown is what releases
	// the read loop below — without this the goroutine outlives ctx forever.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	c.logger.Info("discovery: listening for LAN beacons", "addr", conn.LocalAddr())

	// The announcer is suppressed, not the listener, when announcing is
	// pointless: a loopback-only node cannot be reached anyway, and a node
	// without a shared secret cannot pair — its beacon would advertise a
	// join that can only fail.
	announce := c.sharedSecret != "" && !advertiseIsLoopback(advertiseAddr)
	if c.sharedSecret == "" {
		c.logger.Info("discovery: no shared_secret — listening only (cannot pair, so not announcing)")
	} else if !announce {
		c.logger.Info("discovery: listen_addr is loopback — listening only (nothing could reach the advertised address)")
	}
	if announce {
		go c.discoveryAnnounceLoop(ctx, conn, port, advertiseAddr)
	}

	// Auto-dial needs the same secret the hello will be checked against:
	// without one every handshake fails closed, so the dial pass would be
	// pure churn. The pending list still fills either way.
	if autoDial && c.sharedSecret != "" {
		c.autoDialOn.Store(true)
		go c.autoDialLoop(ctx)
	} else if autoDial {
		c.logger.Info("discovery: auto-dial idle — no shared_secret to satisfy the hello")
	}

	buf := make([]byte, beaconMaxBytes+64)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				c.logger.Warn("discovery: read failed", "err", err)
				return
			}
		}
		c.onBeacon(ctx, buf[:n], src)
	}
}

// discoveryAnnounceLoop emits the beacon every beaconInterval. The same
// socket receives our own broadcast on a LAN — the self-id check in
// onBeacon drops it, which doubles as a live smoke test that the wire
// works. Targets are recomputed per tick: interfaces come and go (a VPN
// rising, a dock connecting) and a stale target list is a node that
// silently stops being seen.
func (c *Core) discoveryAnnounceLoop(ctx context.Context, conn *net.UDPConn, port int, advertiseAddr string) {
	pub, priv, _ := c.nodeKeyPair()
	tick := func() {
		raw := marshalBeacon(c.nodeID, advertiseAddr, pub, priv)
		if raw == nil {
			return
		}
		for _, target := range beaconTargets(port) {
			if _, err := conn.WriteToUDP(raw, target); err != nil {
				c.logger.Debug("discovery: beacon send failed", "target", target, "err", err)
			}
		}
	}
	tick() // announce immediately — a rebooted node should not wait 15s to appear
	t := time.NewTicker(beaconInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// beaconTargets lists the addresses one beacon should be sent to: the
// limited broadcast plus every up-and-broadcast interface's subnet-directed
// broadcast. The limited form alone is not enough in practice — a
// multi-homed host (VMware/WSL/VPN adapters beside the real NIC) can route
// 255.255.255.255 out an interface the peers are not on, and some APs drop
// it — while the directed form is scoped to the exact subnet each interface
// serves. Link-local (169.254/16) subnets are skipped: nothing pairs there.
func beaconTargets(port int) []*net.UDPAddr {
	out := []*net.UDPAddr{{IP: net.IPv4bcast, Port: port}}
	seen := map[string]bool{net.IPv4bcast.String(): true}
	ifs, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ipn.IP.To4() == nil || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			if bcast := directedBroadcast(ipn); bcast != nil {
				if s := bcast.String(); !seen[s] {
					seen[s] = true
					out = append(out, &net.UDPAddr{IP: bcast, Port: port})
				}
			}
		}
	}
	return out
}

// directedBroadcast computes an interface's subnet broadcast address
// (ip | ^mask) from its network, or nil for anything not IPv4.
func directedBroadcast(ipn *net.IPNet) net.IP {
	ip4 := ipn.IP.To4()
	if ip4 == nil || len(ipn.Mask) != 4 {
		return nil
	}
	bcast := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		bcast[i] = ip4[i] | ^ipn.Mask[i]
	}
	return bcast
}

// onBeacon validates one datagram and upserts the hint. Skips: self (our own
// broadcast echoes back), malformed datagrams, beacons with a broken
// signature, and ids already in the directory — a joined node is fleet, not
// pending.
func (c *Core) onBeacon(ctx context.Context, raw []byte, src *net.UDPAddr) {
	b, ok := parseBeacon(raw)
	if !ok || b.ID == c.nodeID {
		return
	}
	verified := false
	if b.Sig != "" {
		if !beaconVerified(b) {
			c.logger.Debug("discovery: beacon with invalid signature dropped", "id", b.ID, "from", src.IP)
			return
		}
		verified = true
	}
	now := time.Now().Unix()
	addr := resolveBeaconAddr(b, src.IP)
	// Auto-dial eligibility is narrower than pending-list eligibility: the
	// resolved host must be the datagram's own source IP. resolveBeaconAddr
	// produces exactly that for a wildcard/empty/loopback host, and an
	// explicit host equal to the source pins itself. Anything else — a DNS
	// name, a third-party IP — stays operator-admit only: it could be an
	// honest alias, but it could also be a forged pointer at a victim, and
	// an auto-dialer must never become a reflection tool.
	//
	// The predicate is CONNECTION state, not membership: a directory row
	// that is merely stale must not block re-discovery — a joined-but-
	// offline node's beacon is exactly the reachability hint auto-dial
	// exists to use. Only an already-live edge makes a hint redundant.
	if c.autoDialOn.Load() && c.connFor(b.ID) == nil {
		if host, _, err := net.SplitHostPort(addr); err == nil && host == src.IP.String() {
			c.noteAutoDial(b.ID, addr)
		}
	}
	var joined int
	_ = c.db.QueryRowContext(ctx, `SELECT 1 FROM employee_cache WHERE id=?`, b.ID).Scan(&joined)
	if joined == 1 {
		return
	}
	var known, total int
	// One roundtrip for both lookups — this runs per datagram on the LAN
	// socket, and every skipped query is WAL work saved.
	_ = c.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pending_nodes WHERE id=?), (SELECT COUNT(1) FROM pending_nodes)`,
		b.ID).Scan(&known, &total)
	if known == 0 && total >= pendingCap {
		// Cap reached: make room before this insert. Unverified rows evict
		// first, stalest among them — an unsigned-beacon flood displaces
		// other noise before it displaces a fingerprint a signature proved.
		_, _ = c.db.ExecContext(ctx, `DELETE FROM pending_nodes WHERE id = (
			SELECT id FROM pending_nodes ORDER BY verified ASC, last_seen ASC LIMIT 1)`)
	}
	if err := ledger.UpsertPending(c.db, ledger.PendingNode{
		ID: b.ID, Addr: addr, PubKey: b.Pub, Ver: b.Ver, Verified: verified,
		FirstSeen: now, LastSeen: now,
	}); err != nil {
		c.logger.Warn("discovery: record pending", "id", b.ID, "err", err)
		return
	}
	// A node whose pending row just materialized is news worth one line;
	// re-beacons every beaconInterval are not — silent refresh after that.
	if known == 0 {
		c.logger.Info("discovery: LAN node seen", "id", b.ID, "addr", addr, "ver", b.Ver)
	}
	// TTL sweep, paced: a DELETE on every received beacon was WAL churn on a
	// busy LAN (each node re-announces every beaconInterval), while the
	// sweep's precision need only be on the order of the TTL itself. Litter
	// lingers at most half a TTL longer — the expiry is already approximate.
	if now-c.pendingSweepAt >= int64(pendingTTL.Seconds())/2 {
		c.pendingSweepAt = now
		if _, err := c.db.ExecContext(ctx, `DELETE FROM pending_nodes WHERE last_seen < ?`, now-int64(pendingTTL.Seconds())); err != nil {
			c.logger.Warn("discovery: sweep pending", "err", err)
		}
	}
}

// advertiseIsLoopback reports whether the configured WS listen address can
// only be reached from this very machine — the case where broadcasting it
// would advertise a door nobody else can knock on.
func advertiseIsLoopback(listenAddr string) bool {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return true // unparseable config: announce nothing
	}
	if host == "" {
		return false // wildcard: every interface, reachable
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false // a DNS name: presumably reachable, announce
}

// noteAutoDial records or refreshes a src-pinned candidate. Called from the
// discovery read loop only when autoDialOn is armed — the table otherwise
// stays empty, so `discovery_dial: "off"` disables the feature at its source.
func (c *Core) noteAutoDial(id, addr string) {
	c.autoDialMu.Lock()
	defer c.autoDialMu.Unlock()
	cand := c.autoDialCands[id]
	cand.addr, cand.lastSeen = addr, time.Now()
	c.autoDialCands[id] = cand
}

// autoDialLoop runs the dial pass every autoDialTick until ctx ends.
func (c *Core) autoDialLoop(ctx context.Context) {
	t := time.NewTicker(autoDialTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.autoDialPass(ctx)
		}
	}
}

// autoDialPass dials the candidates that are due: a candidate whose node is
// now connected leaves the table (the pending hint row goes with it — a live
// peer is fleet, not a suggestion); stale candidates expire with pendingTTL;
// the rest respect the per-node cooldown. Dials run inline and serially — a
// LAN holds a handful of candidates at most, and DialPeer returns as soon as
// the hello is on the wire.
func (c *Core) autoDialPass(ctx context.Context) {
	now := time.Now()
	var connected, due []string
	c.autoDialMu.Lock()
	for id, cand := range c.autoDialCands {
		switch {
		case now.Sub(cand.lastSeen) > pendingTTL:
			delete(c.autoDialCands, id)
		case c.connFor(id) != nil:
			delete(c.autoDialCands, id)
			connected = append(connected, id)
		default:
			// No directory check here on purpose: a joined-but-offline node
			// whose beacon still arrives is dial-worthy (its row is stale,
			// the hint is live). Only a standing connection clears a
			// candidate — that is the branch above.
			if now.Sub(cand.lastDial) >= autoDialCooldown {
				cand.lastDial = now
				c.autoDialCands[id] = cand
				due = append(due, id)
			}
		}
	}
	c.autoDialMu.Unlock()

	// A joined node's pending row outlives its welcome otherwise — it sat in
	// `panda nodes` as "not paired" while the fleet list showed it running.
	for _, id := range connected {
		c.logger.Info("discovery: LAN node connected", "id", id)
		_ = ledger.ForgetPending(c.db, id)
	}
	for _, id := range due {
		c.autoDialMu.Lock()
		cand, ok := c.autoDialCands[id]
		c.autoDialMu.Unlock()
		if !ok {
			continue
		}
		if err := c.DialPeer(ctx, cand.addr); err != nil {
			// The cleartext gate is the failure an operator can act on —
			// surface its remediation once per candidate, at Warn, then let
			// the cooldown keep retries quiet at Debug.
			if !cand.gateWarned && cleartextOK(cand.addr, c.allowCleartext, c.cleartextFor) != nil {
				c.logger.Warn("discovery: LAN node seen but ws:// auto-dial refused — open the segment with network.allow_cleartext_for (or admit manually: panda nodes admit)", "id", id, "addr", cand.addr)
				c.autoDialMu.Lock()
				if cur, ok := c.autoDialCands[id]; ok {
					cur.gateWarned = true
					c.autoDialCands[id] = cur
				}
				c.autoDialMu.Unlock()
			} else {
				c.logger.Debug("discovery: auto-dial failed", "id", id, "addr", cand.addr, "err", err)
			}
			continue
		}
		// DialPeer returns once the hello is on the wire — admission lands
		// (or fails) on the conn asynchronously. Keep the candidate: the
		// next pass's connFor check does the "auto-connected" bookkeeping if
		// the edge stood, and a rejected hello simply retries on cooldown
		// while the beacon keeps the row fresh.
		c.logger.Info("discovery: auto-dialing LAN node", "id", id, "addr", cand.addr)
	}
}
