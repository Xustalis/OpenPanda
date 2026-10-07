// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// udp.go wires the farsky datagram plane into the core: the UDP listener, the
// peer endpoint table it maintains, NAT keepalives, and the punch handshake
// that establishes an endpoint between two nodes that cannot hold TCP.
//
// Trust boundary: a sealed datagram proves mesh membership (the AEAD key is
// derived from the shared secret), so env.From on an opened datagram is as
// trustworthy as an authenticated hello — the same posture the bundle plane
// takes with SourceEID. The endpoint table binds From → source address on
// receipt, and dispatch runs the ordinary handler path from there; hello
// itself never travels datagrams because there is no conn to bind to.

// udpKeepalivePeriod is how often a live UDP route is refreshed. NAT
// mappings commonly expire after ~30-60s of silence; 25s keeps the pinhole
// open without measurable load.
const udpKeepalivePeriod = 25 * time.Second

// udpRouteTTL is how long a bound endpoint may stay silent before the route
// is reaped. A UDP send can never tell delivery from a blackhole, so the only
// liveness signal a route has is authenticated inbound traffic — envelopes,
// punch frames, or keepalives from the bound endpoint. 90s matches the
// stale-peer directory horizon (stalePeerAfter): about four keepalive periods
// of silence, long enough that a peer behind a flaky NAT is not bounced out
// by a few lost datagrams, short enough that a dead endpoint stops absorbing
// sends before the outbox would have parked them.
const udpRouteTTL = 90 * time.Second

// punchInterval / punchWindow define the spray: an offerer sends a signed
// punch to every candidate every interval until an ack lands or the window
// closes. 250ms × 4s ≈ 16 datagrams per candidate — enough for a NAT mapping
// to open and a stray drop, cheap enough to run on metered links.
const (
	punchInterval = 250 * time.Millisecond
	punchWindow   = 4 * time.Second
)

// punchSession tracks one in-flight pinhole negotiation keyed by its nonce.
type punchSession struct {
	peer    string
	addrs   []*net.UDPAddr
	started time.Time
	done    chan struct{} // closed when a verified punch/ack lands
}

// Bound the tables members can grow: every entry behind them requires an
// authenticated mesh member (AEAD-sealed datagram or signed punch on a
// verified conn), so these are member-DoS caps in the same spirit as
// msgSeenMax — generous against any real mesh, finite against a peer minting
// identities or flooding offers.
const (
	udpMaxRoutes     = 1024 // bound From→endpoint pairs
	udpMaxPeersKnown = 512  // peers we track candidates/ports for
	udpMaxCands      = 16   // candidate strings kept per peer — also the
	// spray fan-out bound, which is what makes a hostile offer's Hosts
	// list a bounded reflection cost rather than an arbitrary amplifier
	punchMaxSessions = 64 // concurrent pinhole negotiations
)

// ListenUDP starts the datagram plane on addr. The shared secret is required
// — it derives both the AEAD key and the punch signer, and an unkeyed UDP
// socket would accept nothing anyway. The listener and its keepalive loop
// stop with ctx.
func (c *Core) ListenUDP(ctx context.Context, addr string, stunServers []string) error {
	if c.sharedSecret == "" {
		return nil // no secret: datagrams could never authenticate — off, not broken
	}
	u, err := bus.ListenUDP(addr, c.sharedSecret, c.logger)
	if err != nil {
		return err
	}
	c.udpMu.Lock()
	c.udp = u
	c.udpPort = u.LocalAddr().Port
	c.udpMu.Unlock()

	u.OnEnvelope = func(env bus.Envelope, src *net.UDPAddr) { c.handleUDPEnvelope(ctx, env, src) }
	u.OnPunch = func(f bus.PunchFrame, src *net.UDPAddr, isAck bool) { c.handlePunchFrame(f, src, isAck) }
	u.OnKeepalive = func(src *net.UDPAddr) { c.noteUDPAlive(src) }
	go u.ReadLoop(ctx)
	go c.udpKeepaliveLoop(ctx)
	c.logger.Info("udp: datagram plane listening", "addr", u.LocalAddr())

	// Reflexive discovery is best-effort and asynchronous: an external STUN
	// server — or later, any mesh peer — answers with the public address our
	// punch candidates should carry. Failure changes nothing; the observed-IP
	// hint from hello replies usually covers it.
	if len(stunServers) > 0 {
		go c.stunDiscover(ctx, stunServers)
	}
	return nil
}

// UDPPort reports the bound datagram port (0 when the plane is off); the
// hello advertises it so peers know where to punch.
func (c *Core) UDPPort() int {
	c.udpMu.Lock()
	defer c.udpMu.Unlock()
	return c.udpPort
}

// UDPRoute returns the confirmed datagram endpoint for peer, or nil — the
// punch-maintain loop uses it to decide whether another offer is due.
func (c *Core) UDPRoute(peer string) *net.UDPAddr { return c.udpRouteFor(peer) }

// udpRouteFor returns the confirmed datagram endpoint for peer, if any.
func (c *Core) udpRouteFor(peer string) *net.UDPAddr {
	c.udpMu.Lock()
	defer c.udpMu.Unlock()
	if c.udpRoutes == nil {
		return nil
	}
	return c.udpRoutes[peer]
}

// handleUDPEnvelope receives a decrypted envelope. The AEAD open already
// authenticated the sender as a mesh member — but one shared key cannot say
// WHICH member, so src must also be an address the claimed From itself
// vouched for (see udpPeerAddrOK) before it may bind the endpoint. The
// envelope then goes to the ordinary dispatch — minus hello, which exists
// to bind a conn this path does not have.
func (c *Core) handleUDPEnvelope(ctx context.Context, env bus.Envelope, src *net.UDPAddr) {
	if env.From == "" || env.From == c.nodeID {
		return
	}
	if env.Type == bus.MsgHello {
		c.logger.Warn("udp: hello over datagram dropped", "from", env.From)
		return
	}
	if !c.udpPeerTrusted(env.From, src) {
		c.logger.Debug("udp: envelope from unverifiable addr", "from", env.From, "addr", src)
		return
	}
	c.bindUDPRoute(env.From, src)
	c.dispatch(ctx, nil, env)
}

// udpPeerTrusted reports whether src may stand for peer on the datagram
// plane. AEAD proves a member sent the datagram; this check is what stops a
// member from binding an arbitrary node id to its own address by spraying
// sealed frames. Trust comes from, in order: an established route on the
// same IP (a port move is a NAT rebind and heals itself; an IP move must
// re-prove through the paths below), a public candidate IP the peer
// advertised itself, the observed IP a live conn reported (a TCP remote
// addr cannot be spoofed), or an active punch session — the offer/ready
// exchange that opened it already traveled authenticated mesh channels.
//
// Residual, by design of the shared secret: a member can still mint a
// punch_offer naming another node as Src and listing ITS OWN address,
// poisoning that peer's candidate set before the checks above consult it.
// Per-peer identity needs asymmetric keys — that is the post-farsky work.
func (c *Core) udpPeerTrusted(peer string, src *net.UDPAddr) bool {
	c.udpMu.Lock()
	ok := c.udpPeerAddrOKLocked(peer, src)
	c.udpMu.Unlock()
	if ok {
		return true
	}
	c.punchMu.Lock()
	defer c.punchMu.Unlock()
	for _, s := range c.punching {
		if s.peer == peer {
			return true
		}
	}
	return false
}

// udpPeerAddrOKLocked is the address half of udpPeerTrusted; caller holds udpMu.
func (c *Core) udpPeerAddrOKLocked(peer string, src *net.UDPAddr) bool {
	if r := c.udpRoutes[peer]; r != nil && r.IP.Equal(src.IP) {
		return true
	}
	// Advertised candidates count only at public IPs: private ranges collide
	// across unrelated LANs, so a member on our own segment could otherwise
	// borrow any private address a remote peer happened to publish.
	for _, s := range c.udpCands[peer] {
		h, _, err := net.SplitHostPort(s)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(h); ip != nil && ip.Equal(src.IP) && ip.IsGlobalUnicast() && !ip.IsPrivate() {
			return true
		}
	}
	if obs := c.udpPeerIP[peer]; obs != "" {
		if h, _, err := net.SplitHostPort(obs); err == nil {
			obs = h
		}
		if ip := net.ParseIP(obs); ip != nil && ip.Equal(src.IP) {
			return true
		}
	}
	return false
}

// bindUDPRoute records src as peer's datagram endpoint and stamps its
// liveness: the envelope that arrived over it is authenticated traffic, so
// the route is proven fresh right now. A brand-new binding is routine; an IP
// change on an established route is worth a warn — it is either a network
// migration or a member racing the real peer for the name.
func (c *Core) bindUDPRoute(peer string, src *net.UDPAddr) {
	c.udpMu.Lock()
	if c.udpRoutes == nil {
		c.udpRoutes = make(map[string]*net.UDPAddr)
	}
	if c.udpHeard == nil {
		c.udpHeard = make(map[string]time.Time)
	}
	prev := c.udpRoutes[peer]
	if prev == nil && len(c.udpRoutes) >= udpMaxRoutes {
		c.udpMu.Unlock()
		c.logger.Warn("udp: route table full, datagram dropped", "peer", peer)
		return
	}
	c.udpRoutes[peer] = src
	c.udpHeard[peer] = time.Now()
	c.udpMu.Unlock()
	switch {
	case prev == nil:
		c.logger.Info("udp: endpoint bound", "peer", peer, "addr", src)
	case !prev.IP.Equal(src.IP):
		c.logger.Warn("udp: endpoint IP changed", "peer", peer, "was", prev, "now", src)
	case prev.Port != src.Port:
		c.logger.Debug("udp: endpoint rebound", "peer", peer, "addr", src)
	}
}

// udpKeepaliveLoop holds NAT mappings open for every bound endpoint — and
// reaps the dead ones. A route that has seen no authenticated inbound traffic
// for udpRouteTTL is deleted: sending to a dead endpoint returns success at
// write time (UDP has no delivery signal), so without expiry a stale route
// silently blackholes every envelope sendTo routes through it — including
// terminal task results the outbox then drops as "delivered". Reaping also
// re-arms the punch-maintain loop, which only re-offers while UDPRoute is nil.
// The loop stops when ctx ends or the UDP plane goes away.
func (c *Core) udpKeepaliveLoop(ctx context.Context) {
	t := time.NewTicker(udpKeepalivePeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.udpMu.Lock()
		u := c.udp
		stale := c.sweepStaleUDPRoutesLocked(time.Now())
		targets := make([]*net.UDPAddr, 0, len(c.udpRoutes))
		for _, a := range c.udpRoutes {
			targets = append(targets, a)
		}
		c.udpMu.Unlock()
		for _, peer := range stale {
			c.logger.Info("udp: route expired (silent endpoint)", "peer", peer)
		}
		if u == nil {
			return
		}
		for _, a := range targets {
			_ = u.SendKeepalive(a)
		}
	}
}

// sweepStaleUDPRoutesLocked deletes routes whose last authenticated inbound
// traffic is older than udpRouteTTL, returning the expired peer ids. Caller
// holds udpMu. A route with no freshness record at all counts as silent —
// every legitimate bind writes one, so a missing stamp means the entry
// predates the tracking or lost it to a map reset.
func (c *Core) sweepStaleUDPRoutesLocked(now time.Time) []string {
	var stale []string
	for peer := range c.udpRoutes {
		if now.Sub(c.udpHeard[peer]) > udpRouteTTL {
			delete(c.udpRoutes, peer)
			delete(c.udpHeard, peer)
			stale = append(stale, peer)
		}
	}
	return stale
}

// noteUDPAlive refreshes a bound route's freshness when a keepalive arrives
// from its exact endpoint. The sealed keepalive proves mesh membership but
// carries no identity, so it may only renew the route whose endpoint it came
// from — matching on IP alone would let a NAT-rebound port keep a dead
// binding alive (sends would still go to the old port), and matching less
// would let one member refresh another's route. A keepalive from an unknown
// endpoint renews nothing; the peer's own envelopes rebind it instead.
func (c *Core) noteUDPAlive(src *net.UDPAddr) {
	c.udpMu.Lock()
	defer c.udpMu.Unlock()
	for peer, a := range c.udpRoutes {
		if a.Port == src.Port && a.IP.Equal(src.IP) {
			c.udpHeard[peer] = time.Now()
			return
		}
	}
}

// stunDiscover queries the configured STUN servers in turn and records the
// first reflexive answer as a candidate the next punch offer advertises.
// Each query reuses the shared socket so the mapping it learns is the one a
// punch would actually create.
func (c *Core) stunDiscover(ctx context.Context, servers []string) {
	for _, srv := range servers {
		c.udpMu.Lock()
		u := c.udp
		c.udpMu.Unlock()
		if u == nil {
			return
		}
		addr, err := u.STUNBinding(ctx, srv)
		if err != nil {
			c.logger.Debug("udp: stun", "server", srv, "err", err)
			continue
		}
		c.udpMu.Lock()
		c.udpReflexive = addr.String()
		c.udpMu.Unlock()
		c.logger.Info("udp: reflexive address learned", "via", srv, "addr", addr)
		return
	}
}

// udpCandidates lists the host:port strings a punch offer should advertise:
// every non-loopback interface address, the reflexive IP a peer's hello
// observed for us, and any STUN-discovered mapping — all at the UDP listen
// port, because a punch sprayed from the shared socket opens a mapping for
// exactly that port.
func (c *Core) udpCandidates() []string {
	c.udpMu.Lock()
	port := c.udpPort
	reflexive := c.udpReflexive
	c.udpMu.Unlock()
	if port == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(host string) {
		if host == "" {
			return
		}
		ep := net.JoinHostPort(host, itoa(port))
		if !seen[ep] {
			seen[ep] = true
			out = append(out, ep)
		}
	}
	if reflexive != "" {
		if h, _, err := net.SplitHostPort(reflexive); err == nil {
			add(h)
		} else {
			add(reflexive)
		}
	}
	c.udpMu.Lock()
	you := c.observedIP
	c.udpMu.Unlock()
	add(you)
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || !ipn.IP.IsGlobalUnicast() || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			add(ipn.IP.String())
		}
	}
	return out
}

// PunchPeer initiates a pinhole toward peerID: it sends a punch_offer
// envelope through the mesh (directly if a conn exists, relayed along the
// link-state graph otherwise) and sprays signed punch frames at every
// candidate it already knows for the peer. The ready reply starts the peer
// spraying back; the first verified datagram from either direction binds the
// route. Safe to call while a WS conn exists — the datagram route simply
// becomes an additional path and sendTo keeps preferring the conn.
func (c *Core) PunchPeer(ctx context.Context, peerID string) error {
	c.udpMu.Lock()
	u := c.udp
	c.udpMu.Unlock()
	if u == nil {
		return errUDPDisabled
	}
	nonce, err := newUUID()
	if err != nil {
		return err
	}
	payload := bus.PunchOfferPayload{
		Nonce: nonce,
		Src:   c.nodeID,
		Hosts: c.udpCandidates(),
		TTL:   bus.PunchMaxTTL,
	}
	msgID, err := newUUID()
	if err != nil {
		return err
	}
	env, err := bus.NewEnvelope(bus.MsgPunchOffer, c.nodeID, msgID, payload)
	if err != nil {
		return err
	}
	env.To = peerID
	c.startPunchSession(nonce, peerID, c.knownCandidates(peerID))
	if err := c.routeTo(ctx, peerID, env); err != nil {
		// The offer could not be routed, but the spray still runs out the
		// window: a peer punching us at the same time completes the
		// handshake without ever seeing our offer, and stale candidates are
		// better than none. The error is still reported so callers can
		// distinguish "no route yet" from a confirmed path.
		return err
	}
	return nil
}

// startPunchSession registers the nonce and launches the spray goroutine.
// The session table is capped: every offer mints one, and a peer spraying
// offers must not be able to mint goroutines without bound.
func (c *Core) startPunchSession(nonce, peer string, addrs []*net.UDPAddr) {
	sess := &punchSession{peer: peer, addrs: addrs, started: time.Now(), done: make(chan struct{})}
	c.punchMu.Lock()
	if c.punching == nil {
		c.punching = make(map[string]*punchSession)
	}
	if _, dup := c.punching[nonce]; dup {
		// A nonce is minted fresh per offer; a duplicate is a peer replaying
		// one it saw (or an astronomical collision) — never let it evict the
		// live session it names.
		c.punchMu.Unlock()
		return
	}
	if len(c.punching) >= punchMaxSessions {
		c.punchMu.Unlock()
		c.logger.Warn("udp: punch session table full", "peer", peer)
		return
	}
	c.punching[nonce] = sess
	c.punchMu.Unlock()
	go c.punchSpray(nonce, sess)
}

// punchSpray emits a signed punch to each candidate until the session ends
// (confirmed or window expired). A session with no candidates just waits —
// the responder's ready may still deliver some.
func (c *Core) punchSpray(nonce string, sess *punchSession) {
	c.udpMu.Lock()
	u := c.udp
	c.udpMu.Unlock()
	if u == nil {
		return
	}
	deadline := time.NewTimer(punchWindow)
	defer deadline.Stop()
	tick := time.NewTicker(punchInterval)
	defer tick.Stop()
	send := func() {
		f := bus.PunchFrame{
			Nonce: nonce, From: c.nodeID, TS: time.Now().Unix(),
			Sig: bus.PunchSig(c.sharedSecret, nonce, c.nodeID, time.Now().Unix()),
		}
		c.punchMu.Lock()
		addrs := sess.addrs
		c.punchMu.Unlock()
		for _, a := range addrs {
			_ = u.SendPunch(f, a, false)
		}
	}
	send()
	for {
		select {
		case <-sess.done:
			return
		case <-deadline.C:
			c.punchMu.Lock()
			delete(c.punching, nonce)
			c.punchMu.Unlock()
			c.logger.Debug("udp: punch window expired", "peer", sess.peer)
			return
		case <-tick.C:
			send()
		}
	}
}

// finishPunch marks a session confirmed: the spray stops and the session is
// dropped. The endpoint route it bound persists independently.
func (c *Core) finishPunch(nonce string) {
	c.punchMu.Lock()
	if s, ok := c.punching[nonce]; ok {
		delete(c.punching, nonce)
		select {
		case <-s.done:
		default:
			close(s.done)
		}
	}
	c.punchMu.Unlock()
}

// punchSessionAddrs replaces a session's candidate list — the responder's
// ready brings the definitive set.
func (c *Core) punchSessionAddrs(nonce string, addrs []*net.UDPAddr) {
	c.punchMu.Lock()
	if s, ok := c.punching[nonce]; ok {
		s.addrs = addrs
	}
	c.punchMu.Unlock()
}

// knownCandidates resolves the candidate endpoints recorded for peer: the
// learned candidate strings (from hellos and offers) plus the reflexive
// guess <observed-IP>:<peer's UDP port> when a hello supplied both halves.
func (c *Core) knownCandidates(peer string) []*net.UDPAddr {
	c.udpMu.Lock()
	strs := append([]string(nil), c.udpCands[peer]...)
	port := c.udpPeerPort[peer]
	obs := c.udpPeerIP[peer]
	c.udpMu.Unlock()
	if port > 0 && obs != "" {
		strs = append(strs, net.JoinHostPort(obs, itoa(port)))
	}
	var out []*net.UDPAddr
	seen := map[string]bool{}
	for _, s := range strs {
		if seen[s] {
			continue
		}
		seen[s] = true
		if a, err := net.ResolveUDPAddr("udp", s); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// learnPeerUDP records what a hello or offer taught us about peer's
// reachability: its UDP listener port (candidate = observed-IP:port), the
// raw candidate strings it published, and the observed IP when a conn gave
// us one. Both the peer set and each peer's candidate list are capped —
// Hosts comes off the wire, and capping it is also what bounds the spray
// fan-out a single offer can buy.
func (c *Core) learnPeerUDP(peer string, port int, hosts []string, observedIP string) {
	c.udpMu.Lock()
	defer c.udpMu.Unlock()
	if c.udpCands == nil {
		c.udpCands = make(map[string][]string)
		c.udpPeerPort = make(map[string]int)
		c.udpPeerIP = make(map[string]string)
	}
	if _, known := c.udpCands[peer]; !known && len(c.udpCands) >= udpMaxPeersKnown {
		return
	}
	if port > 0 {
		c.udpPeerPort[peer] = port
	}
	if observedIP != "" {
		c.udpPeerIP[peer] = observedIP
	}
	for _, h := range hosts {
		if len(c.udpCands[peer]) >= udpMaxCands {
			break
		}
		if !slices.Contains(c.udpCands[peer], h) {
			c.udpCands[peer] = append(c.udpCands[peer], h)
		}
	}
}

// handlePunchFrame processes a verified punch/ack datagram: bind the sender
// endpoint, confirm the session, and answer an opener with an ack so the far
// side learns the path is bidirectional. The HMAC proves membership, not the
// claimed From — src must clear udpPeerTrusted like any other datagram.
func (c *Core) handlePunchFrame(f bus.PunchFrame, src *net.UDPAddr, isAck bool) {
	if f.From == "" || f.From == c.nodeID {
		return
	}
	if !c.udpPeerTrusted(f.From, src) {
		c.logger.Debug("udp: punch from unverifiable addr", "from", f.From, "addr", src)
		return
	}
	c.udpMu.Lock()
	if c.udpRoutes == nil {
		c.udpRoutes = make(map[string]*net.UDPAddr)
	}
	if _, known := c.udpRoutes[f.From]; !known && len(c.udpRoutes) >= udpMaxRoutes {
		u := c.udp
		c.udpMu.Unlock()
		// The route table is full — still answer the punch so the far side
		// can bind us; we just do not spend a route slot on it.
		if !isAck && u != nil {
			ack := bus.PunchFrame{
				Nonce: f.Nonce, From: c.nodeID, TS: time.Now().Unix(),
				Sig: bus.PunchSig(c.sharedSecret, f.Nonce, c.nodeID, time.Now().Unix()),
			}
			_ = u.SendPunch(ack, src, true)
		}
		return
	}
	c.udpRoutes[f.From] = src
	if c.udpHeard == nil {
		c.udpHeard = make(map[string]time.Time)
	}
	c.udpHeard[f.From] = time.Now()
	u := c.udp
	c.udpMu.Unlock()
	c.logger.Info("udp: pinhole confirmed", "peer", f.From, "addr", src, "ack", isAck)
	c.finishPunch(f.Nonce)
	if !isAck && u != nil {
		ack := bus.PunchFrame{
			Nonce: f.Nonce, From: c.nodeID, TS: time.Now().Unix(),
			Sig: bus.PunchSig(c.sharedSecret, f.Nonce, c.nodeID, time.Now().Unix()),
		}
		_ = u.SendPunch(ack, src, true)
	}
}

// punchOrigin resolves the node a punch coordination message belongs to:
// the payload's Src when set (relayed envelopes re-envelope as the last
// hop), else the envelope sender for a direct arrival.
func punchOrigin(p bus.PunchOfferPayload, env bus.Envelope) string {
	if p.Src != "" {
		return p.Src
	}
	return env.From
}

// handlePunchOffer answers a coordination offer: record the initiator's
// candidates, join the session (start spraying at them), and reply
// punch_ready routed back through the mesh.
func (c *Core) handlePunchOffer(ctx context.Context, env bus.Envelope) {
	var p bus.PunchOfferPayload
	if err := env.PayloadInto(&p); err != nil || p.Nonce == "" {
		c.logger.Warn("udp: bad punch offer", "from", env.From)
		return
	}
	origin := punchOrigin(p, env)
	if origin == "" || origin == c.nodeID {
		return
	}
	c.learnPeerUDP(origin, 0, p.Hosts, "")
	c.startPunchSession(p.Nonce, origin, c.knownCandidates(origin))

	reply := bus.PunchReadyPayload{
		Nonce: p.Nonce,
		Src:   c.nodeID,
		Hosts: c.udpCandidates(),
		TTL:   bus.PunchMaxTTL,
	}
	msgID, err := newUUID()
	if err != nil {
		return
	}
	out, err := bus.NewEnvelope(bus.MsgPunchReady, c.nodeID, msgID, reply)
	if err != nil {
		return
	}
	out.To = origin
	if err := c.routeTo(ctx, origin, out); err != nil {
		c.logger.Debug("udp: punch ready unroutable", "to", origin, "err", err)
	}
}

// handlePunchReady joins the initiator side: the responder accepted and sent
// its candidates — swap the spray onto them.
func (c *Core) handlePunchReady(ctx context.Context, env bus.Envelope) {
	var p bus.PunchReadyPayload
	if err := env.PayloadInto(&p); err != nil || p.Nonce == "" {
		return
	}
	origin := punchOrigin(p, env)
	if origin == "" || origin == c.nodeID {
		return
	}
	c.learnPeerUDP(origin, 0, p.Hosts, "")
	c.punchMu.Lock()
	sess, ok := c.punching[p.Nonce]
	matched := ok && sess.peer == origin
	c.punchMu.Unlock()
	if !matched {
		// No session means the offer we sent was already confirmed by a
		// punch — or the ready is unsolicited. A ready naming a different
		// origin than the session's peer is a relay hop replaying a nonce it
		// saw; it must not re-aim our spray.
		return
	}
	c.punchSessionAddrs(p.Nonce, c.knownCandidates(origin))
}

// forwardPunch relays a coordination envelope one hop toward env.To. The
// payload TTL is spent here and the envelope is re-minted as ours — From =
// this node, same msg_id — because the receiving conn authenticates the
// envelope by its bound peer id: keeping the origin's From would trip the
// spoof check (the origin survives in payload Src). The shared msg_id is
// what makes receiver-side dedup, not the TTL alone, kill a residual loop.
func (c *Core) forwardPunch(ctx context.Context, env bus.Envelope) {
	var p bus.PunchOfferPayload
	if err := env.PayloadInto(&p); err != nil {
		return
	}
	if p.TTL <= 1 {
		c.logger.Debug("udp: punch ttl spent", "type", env.Type, "to", env.To)
		return
	}
	p.TTL--
	raw, err := json.Marshal(p)
	if err != nil {
		return
	}
	env.Payload = raw
	env.From = c.nodeID
	if err := c.routeTo(ctx, env.To, env); err != nil {
		c.logger.Debug("udp: punch relay unroutable", "to", env.To, "err", err)
	}
}

// routeTo delivers env toward peerID: the live conn or datagram route when
// one exists, else the first hop of the cheapest advertised path that is
// connected right now — the same no-echo rule DTN relay uses.
func (c *Core) routeTo(ctx context.Context, peerID string, env bus.Envelope) error {
	if err := c.sendTo(peerID, env); err == nil {
		return nil
	}
	// Punch envelopes are small and carry no custody lifetime, so the
	// schedule-aware path gets an unknown size (fits any window) and no
	// expiry bound.
	hop, _ := c.dtnNextHop(ctx, peerID, map[string]bool{env.From: true}, 0, 0)
	if hop == "" {
		return errNoRoute
	}
	return c.sendTo(hop, env)
}

var (
	errUDPDisabled = errString("udp: datagram plane is off")
	errNoRoute     = errString("udp: no route to peer")
)

type errString string

func (e errString) Error() string { return string(e) }

func itoa(i int) string { return strconv.Itoa(i) }
