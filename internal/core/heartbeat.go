// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/hwinfo"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/version"
	"net"
	"runtime"
	"sync"
	"time"
)

// Idle reports whether the node has no active (running, dispatched, or
// waiting-for-context) tasks. The Dreaming scheduler uses it to run
// consolidation only when the node is free, so dreaming never competes with
// real work.
func (c *Core) Idle(ctx context.Context) bool {
	return c.store.Idle(ctx)
}

// RunHeartbeat starts the heartbeat loop: each tick updates the local ledger
// (last_seen + live capacity) and broadcasts a wire heartbeat to every
// connected peer, so a peer's TMB freshness weight and DCPS capacity signals
// are fed by real traffic instead of going stale after the initial hello.
func (c *Core) RunHeartbeat(ctx context.Context) {
	t := time.NewTicker(c.node.hbTick)
	defer t.Stop()
	// Beat immediately so the directory is fresh on startup.
	c.node.beat(ctx)
	c.broadcastHeartbeat(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.node.beat(ctx)
			c.broadcastHeartbeat(ctx)
		}
	}
}

// broadcastHeartbeat sends this node's live status/capacity to every connected
// peer. Send failures are logged and skipped — a dead conn is reaped by the
// read loop, and the next tick reaches the replacement.
func (c *Core) broadcastHeartbeat(ctx context.Context) {
	capJSON, load := c.node.capacitySnapshot(ctx)
	projects := c.projectNames()
	// The wire set goes to peers below; the same list lands on the local
	// directory's self row so RouteAt's selfNode reads residence off the same
	// table it scores peers from. A nil list (store read failed) is skipped —
	// the gossip contract treats absent as "leave the stored set alone".
	if projects != nil {
		if b, err := json.Marshal(projects); err == nil {
			// Write only on change: the residence set moves when work lands,
			// not per beat — an unconditional UPDATE each tick was WAL churn
			// on an idle node.
			if err := ledger.UpdateAdjacencyIfChanged(c.db, c.nodeID, "", "", "", string(b)); err != nil {
				c.logger.Debug("publish self residence", "err", err)
			}
		}
	}
	c.mu.RLock()
	conns := make(map[string]*bus.Conn, len(c.peers))
	for id, p := range c.peers {
		conns[id] = p.conn
	}
	c.mu.RUnlock()
	if len(conns) == 0 {
		return
	}
	// The payload is identical for every peer (msg_id dedup is per-receiver,
	// so a shared id is safe), and it is built once: link metrics and the
	// neighbor set used to be recomputed inside the per-peer loop.
	lms := c.linkMetrics()
	wireLinks := make([]bus.LinkMetric, len(lms))
	for i, l := range lms {
		wireLinks[i] = bus.LinkMetric{Peer: l.Peer, RTTms: l.RTTms, Kind: l.Kind}
	}
	status := "online"
	if c.node.draining(ctx) {
		status = "draining"
	}
	// M1 epidemic directory: re-mint our signed row (fresh seen_at — churn
	// free, it sits outside content_hash) then fold the stamp table into the
	// beat's digest. Peers whose table agrees see a match and stay silent;
	// a single mismatch costs one dir_pull exchange, converging both sides.
	c.refreshSelfDirRow(ctx)
	dirDigest := c.dirDigest(ctx)
	msgID, err := newUUID()
	if err != nil {
		c.logger.Warn("mint heartbeat id", "err", err)
		return
	}
	env, err := bus.NewEnvelope(bus.MsgHeartbeat, c.nodeID, msgID, bus.HeartbeatPayload{
		Status: status, Load: load, Capacity: capJSON,
		BlockedAgents: c.blockedAgents(),
		Neighbors:     c.livePeerIDs(), Links: wireLinks,
		Contacts: c.wireContacts(), Projects: projects,
		Ver: version.Version, Prov: wireProvenance(),
		DirDigest: dirDigest, EdgeLoad: len(conns),
	})
	if err != nil {
		c.logger.Warn("build heartbeat", "err", err)
		return
	}
	// Fan out concurrently: Send blocks until the conn's writer places the
	// frame (bounded by writeWait), so one wedged peer would otherwise delay
	// every later peer's heartbeat — every tick — by that bound.
	for id, conn := range conns {
		e := env
		e.To = id
		c.goGuarded("heartbeat send "+id, func() {
			if err := conn.Send(e); err != nil {
				c.logger.Debug("heartbeat send", "peer", id, "err", err)
			}
		})
	}
}

// clampCapacity sanitizes a peer-advertised capacity blob before it reaches the
// directory: a negative count or limit would otherwise be stored verbatim and
// leak into scoring, where waitSignal reads negative depth as "better than
// idle". Zero is what such a field means; the positive values pass through.
func clampCapacity(capJSON string) string {
	if capJSON == "" {
		return capJSON
	}
	var cap ledger.Capacity
	if err := json.Unmarshal([]byte(capJSON), &cap); err != nil {
		return capJSON // malformed input stays for Heartbeat's own handling
	}
	if cap.CPUCores < 0 {
		cap.CPUCores = 0
	}
	if cap.RAMGB < 0 {
		cap.RAMGB = 0
	}
	if cap.MaxConcurrent < 0 {
		cap.MaxConcurrent = 0
	}
	if cap.CurrentTasks < 0 {
		cap.CurrentTasks = 0
	}
	if l := cap.Live; l != nil && (l.MemFreeGB < -1 || l.DiskFreeGB < -1 || l.GPUUtil < -1 || l.GPUUtil > 100) {
		// A peer-advertised live block outside its value domain is corrupt:
		// drop it whole so a forged negative "free" figure cannot read as a
		// huge headroom bonus in scoring. -1 is the legit unmeasured sentinel.
		cap.Live = nil
	}
	b, err := json.Marshal(cap)
	if err != nil {
		return capJSON
	}
	return string(b)
}

// sanitizeSummary applies the same negative-field clamp to a capability card a
// peer sent in a hello or a heartbeat. The fields here route work and gate
// hardware fits, so a negative figure is never data — it is either a bug or a
// lie, and zero is its honest form.
func sanitizeSummary(s ledger.CapabilitySummary) ledger.CapabilitySummary {
	c := &s.Capacity
	if c.CPUCores < 0 {
		c.CPUCores = 0
	}
	if c.RAMGB < 0 {
		c.RAMGB = 0
	}
	if c.MaxConcurrent < 0 {
		c.MaxConcurrent = 0
	}
	if c.CurrentTasks < 0 {
		c.CurrentTasks = 0
	}
	if s.SchedulerTier < 0 {
		s.SchedulerTier = 0
	}
	// Fail-closed boundary: a peer's malformed window is dropped rather than
	// routed by — SendAt refuses it anyway, but the directory should not
	// carry claims that cannot be evaluated.
	valid := s.Contacts[:0]
	for _, ct := range s.Contacts {
		if ct.Valid() {
			valid = append(valid, ct)
		}
	}
	s.Contacts = valid
	// Reachability fields get the same clamp: a negative bandwidth claim is
	// a bug or a lie, and self-asserted dial addrs are deduped and capped —
	// the field is a hint list, never a quota a peer may spend unboundedly.
	if s.Relay != nil && s.Relay.MaxKbps < 0 {
		s.Relay.MaxKbps = 0
	}
	if len(s.Addrs) > 16 {
		s.Addrs = s.Addrs[:16]
	}
	seen := make(map[string]bool, len(s.Addrs))
	addrs := s.Addrs[:0]
	for _, a := range s.Addrs {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		addrs = append(addrs, a)
	}
	s.Addrs = addrs
	return s
}

// handleHeartbeat refreshes the sender's directory row (last_seen + capacity).
// This is the TMB "new message overwrites the slot" mapping: each heartbeat
// upserts the sender's slot wholesale, and Route's freshness weight — not the
// heartbeat cadence — decides how much the data is worth.
func (c *Core) handleHeartbeat(ctx context.Context, env bus.Envelope) {
	var p bus.HeartbeatPayload
	if err := env.PayloadInto(&p); err != nil {
		c.logger.Warn("bad heartbeat", "err", err)
		return
	}
	status := p.Status
	if status == "" {
		status = "online"
	}
	// Change-gated: a steady peer's identical beat only needs last_seen
	// bumped once in a while — the row write is what refreshes staleness, and
	// peerFreshnessSec keeps it well under the 90s ExpireStale horizon.
	if err := ledger.HeartbeatIfChanged(c.db, env.From, status, clampCapacity(p.Capacity), peerFreshnessSec); err != nil {
		c.logger.Warn("apply heartbeat", "from", env.From, "err", err)
	}
	// Version gossip (Track 3): every beat re-stamps the sender's software
	// version, so a mid-release upgrade refreshes the skew display without
	// waiting for a reconnect-time hello.
	if err := ledger.SetNodeVerIfChanged(c.db, env.From, p.Ver); err != nil {
		c.logger.Warn("stamp peer version", "from", env.From, "err", err)
	}
	if err := ledger.SetNodeProvIfChanged(c.db, env.From, sanitizePeerProv(p.Prov)); err != nil {
		c.logger.Warn("stamp peer provenance", "from", env.From, "err", err)
	}
	// Heartbeats also publish the sender's circuit-open agents so this node's
	// routing can weigh the peer's failure history (see applyPeerBlockers).
	// The field is absent both on old nodes and when nothing is blocked; both
	// cases mean "no list to remember", so drop any previously published one.
	c.mu.Lock()
	if len(p.BlockedAgents) > 0 {
		c.peerBlocked[env.From] = p.BlockedAgents
	} else {
		delete(c.peerBlocked, env.From)
	}
	c.mu.Unlock()
	// Adjacency gossip (docs/design/routing-and-scheduling.md): the sender's edge set, its measured weights
	// and its contact plan ride every beat so the directory's view of the
	// link-state graph — and the schedule it opens on — tracks reality
	// instead of freezing at hello time.
	if p.Neighbors != nil || p.Links != nil || p.Contacts != nil || p.Projects != nil {
		var nbJSON, linksJSON, contactsJSON, projectsJSON string
		if p.Neighbors != nil {
			if b, err := json.Marshal(p.Neighbors); err == nil {
				nbJSON = string(b)
			}
		}
		if p.Links != nil {
			if b, err := json.Marshal(p.Links); err == nil {
				linksJSON = string(b)
			}
		}
		if p.Contacts != nil {
			if b, err := json.Marshal(p.Contacts); err == nil {
				contactsJSON = string(b)
			}
		}
		if p.Projects != nil {
			if b, err := json.Marshal(p.Projects); err == nil {
				projectsJSON = string(b)
			}
		}
		if err := ledger.UpdateAdjacencyIfChanged(c.db, env.From, nbJSON, linksJSON, contactsJSON, projectsJSON); err != nil {
			c.logger.Warn("update adjacency", "from", env.From, "err", err)
		}
	}
	// A card-carrying heartbeat is the peer announcing a hot reload: adopt
	// its new capability summary right away instead of routing against the
	// hello-time card until the next reconnect. Absent on ordinary beats —
	// and on every old-node heartbeat — the field is simply not there.
	if len(p.Card) > 0 {
		var sum ledger.CapabilitySummary
		if err := json.Unmarshal(p.Card, &sum); err != nil {
			c.logger.Warn("bad capability card in heartbeat", "peer", env.From, "err", err)
		} else if ledger.SessionRowID(env.From) {
			// A session id is a client seat, not a fleet member — storing its
			// card would leave a permanent ghost row per session.
		} else if err := ledger.UpsertRemote(c.db, env.From, sanitizeSummary(sum)); err != nil {
			c.logger.Warn("upsert remote card from heartbeat", "peer", env.From, "err", err)
		} else {
			c.logger.Info("peer card updated", "peer", env.From)
		}
	}
	// Edge load (M3): the sender's advertised live-edge count feeds
	// rendezvous scoring so the mesh prefers less-pinned candidates.
	c.notePeerEdgeLoad(env.From, p.EdgeLoad)
	// M1 last: the beat may have just changed our own table (card, adjacency),
	// so the digest compare runs after every write above — a mismatch then
	// reflects the post-apply state, not a stale snapshot.
	c.maybeDirPull(ctx, env.From, p.DirDigest)
}

// summary reduces this node's capability card to the compact profile it
// advertises over hello (and card-carrying heartbeats). Remote nodes need
// ability IDs, capacity and the declared hardware profile to route — not the
// executable commands themselves.
func (c *Core) summary() ledger.CapabilitySummary {
	card := c.Card()
	card.Capacity.ArtifactMergeVersion = artifactMergeVersion
	card.Capacity.ExecutionProtocol = bus.ExecutionProtocolVersion
	router := c.currentRouter()
	s := ledger.CapabilitySummary{
		Device:          card.Device,
		ResourceClass:   card.ResourceClass,
		NodeKind:        card.NodeKind,
		NodeIdentity:    card.NodeIdentity,
		OS:              runtime.GOOS,
		FormFactor:      hwinfo.FormFactor(),
		SchedulerTier:   c.tier,
		Chip:            card.Chip,
		Capacity:        card.Capacity,
		ResourceProfile: card.ResourceProfile,
		Projects:        c.projectNames(),
	}
	for _, n := range card.Native {
		s.NativeIDs = append(s.NativeIDs, n.ID)
	}
	s.AgentCaps = make(map[string][]string, len(card.Agents))
	// Advertise only agents that can actually run here (CLI present,
	// credentials or injection configured, and the model endpoint
	// reachable). A card entry whose CLI is installed but locked out —
	// or whose provider is down — would otherwise attract cross-device
	// routing and fail at runtime after a long hang. The endpoint probe
	// inside AgentDispatchable is bounded but runs per agent — fan it out
	// so N dead providers cost one probe duration, not N, on the
	// handshake path that calls this.
	if router != nil && len(card.Agents) > 0 {
		usable := make(map[string]bool, len(card.Agents))
		reasons := make(map[string]string, len(card.Agents))
		var wg sync.WaitGroup
		var mu sync.Mutex
		for name, ag := range card.Agents {
			wg.Add(1)
			c.goGuarded("agent usable probe "+name, func() {
				defer wg.Done()
				ok, reason := router.AgentUsableReason(name, ag)
				mu.Lock()
				usable[name] = ok
				reasons[name] = reason
				mu.Unlock()
			})
		}
		wg.Wait()
		for name, ag := range card.Agents {
			if usable[name] {
				s.AgentCaps[name] = ag.Capabilities
			} else {
				c.logger.Debug("summary: agent not advertised", "agent", name, "reason", reasons[name])
			}
		}
	} else {
		for name, ag := range card.Agents {
			s.AgentCaps[name] = ag.Capabilities
		}
	}
	for _, m := range card.Manual {
		s.ManualIDs = append(s.ManualIDs, m.ID)
	}
	for _, a := range card.Actuators {
		s.ActuatorIDs = append(s.ActuatorIDs, a.ID)
	}
	// docs/design/routing-and-scheduling.md link-state advertisement: the live peer set is this node's edge
	// list in the mesh routing graph. Published in the hello so every peer's
	// directory learns the topology, not just this node's abilities.
	s.Neighbors = c.livePeerIDs()
	s.Links = c.linkMetrics()
	s.Contacts = c.contacts
	// M3 reachability advertisement: the relay services this node will
	// provide (card-declared) and the addresses a peer can try before it
	// spends a punch or a relay. Both are additive fields — old peers
	// ignore them, new peers read them for the path-priority ladder.
	if card.Relay != nil {
		r := *card.Relay
		pol := c.netPolicy()
		if !pol.transit {
			r.Transit = false
		}
		switch pol.rendezvous {
		case "never":
			r.Rendezvous = false
		case "always":
			// Operator assertion: the card's declaration rides unchanged.
		default: // "auto"
			// The claim is evidence-based: peers confirmed they could dial us
			// back (dial_check), or — before there is anyone to ask — a
			// globally routable address, which is all a fresh node has.
			if r.Rendezvous && !c.rendezvousClaimable() {
				r.Rendezvous = false
			}
		}
		if r.Transit || r.Rendezvous {
			s.Relay = &r
		}
	}
	s.Addrs = c.selfAddrs()
	return s
}

// rendezvousClaimable is the auto-mode gate: a live callback proof wins; with
// no peers to ask, a globally routable address stands in, because a node that
// just started has no way to earn a proof yet and would otherwise never be
// able to offer the role.
func (c *Core) rendezvousClaimable() bool {
	if c.rendezvousProofValid() {
		return true
	}
	return c.hasPublicAddr() && len(c.livePeerIDs()) == 0
}

// hasPublicAddr reports whether any advertised dial candidate is globally
// routable — the proof network.rendezvous:auto requires before the card
// claims the role. Hostnames pass: a domain is the operator's promise that
// something in front of it is reachable, and refusing it would make the
// knob useless for exactly the deployments (a home node behind a mapped
// port) the design says can serve.
func (c *Core) hasPublicAddr() bool {
	for _, a := range c.selfAddrs() {
		host, _, err := net.SplitHostPort(a)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return true
		}
		if ip.IsGlobalUnicast() && !ip.IsPrivate() && !isSharedAddr(ip) {
			return true
		}
	}
	return false
}

// isSharedAddr reports the CGNAT / Tailscale 100.64.0.0/10 band: globally
// shaped, but reachable only inside the overlay that assigned it.
func isSharedAddr(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

// selfAddrs returns this node's advertised dial candidates for the
// capability summary's addrs field (M3). A concretely bound listener
// advertises verbatim; an unspecified bind (":7836", "0.0.0.0") expands to
// the host's non-loopback interface addresses so peers get something they
// can actually dial. Loopback binds advertise the literal — only same-host
// siblings can use it, and they are precisely who needs it. The set is
// deduped and capped so the card stays a summary, not a netstat dump.
func (c *Core) selfAddrs() []string {
	addr, _ := c.listenAddr.Load().(string)
	if addr == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = ""
	}
	if host != "" {
		return []string{net.JoinHostPort(host, port)}
	}
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	out := make([]string, 0, 8)
	for _, ia := range ifaces {
		ipnet, ok := ia.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, net.JoinHostPort(ipnet.IP.String(), port))
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// linkMetrics reports the measured edge weight (ping/pong RTT) to each live
// peer (docs/design/routing-and-scheduling.md). Peers without a sample yet are omitted — the weighted router
// prices an absent metric at the unknown-link default instead of recording
// a fake zero, which would make an unmeasured link look free.
func (c *Core) linkMetrics() []ledger.LinkMetric {
	c.mu.RLock()
	conns := make(map[string]int64, len(c.peers))
	for id, p := range c.peers {
		if p.conn == nil {
			continue
		}
		if rtt := p.conn.RTT(); rtt > 0 {
			// A measured sub-millisecond edge (loopback, same-host) is real
			// data: ceil it to 1ms rather than dropping it as unmeasured.
			// Zero still means "no sample", never "this edge is free".
			conns[id] = max(rtt.Milliseconds(), 1)
		}
	}
	c.mu.RUnlock()
	out := make([]ledger.LinkMetric, 0, len(conns))
	for id, rtt := range conns {
		out = append(out, ledger.LinkMetric{Peer: id, RTTms: rtt, Kind: "ws"})
	}
	// A peer reachable only through a punched datagram route has no WS
	// conn to ping — it still advertises an edge so the fleet view (and
	// any consumer of links_json) can see the transport it is on. RTT is
	// left at zero (unmeasured): a fake sample would price the link better
	// than it is.
	c.udpMu.Lock()
	for id := range c.udpRoutes {
		if _, ok := conns[id]; !ok {
			out = append(out, ledger.LinkMetric{Peer: id, Kind: "udp"})
		}
	}
	c.udpMu.Unlock()
	return out
}

// helloCard marshals the capability summary for the hello payload.
func (c *Core) helloCard() (json.RawMessage, error) {
	return json.Marshal(c.summary())
}
