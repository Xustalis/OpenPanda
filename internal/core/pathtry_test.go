// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

func TestAddrRankAndPublic(t *testing.T) {
	v6 := addrRank("[2001:4860:4860::8888]:7836")
	v4pub := addrRank("203.0.113.10:7836")
	v4priv := addrRank("192.168.1.5:7836")
	host := addrRank("node.example.com:7836")
	if !(v6 < v4pub && v4pub <= host && host < v4priv) {
		t.Fatalf("rank order wrong: v6=%d v4pub=%d host=%d v4priv=%d", v6, v4pub, host, v4priv)
	}
	for _, a := range []string{"203.0.113.10:7836", "[2001:4860:4860::8888]:7836", "node.example.com:7836"} {
		if !addrPublic(a) {
			t.Fatalf("%s should pass the public gate", a)
		}
	}
	for _, a := range []string{"192.168.1.5:7836", "10.0.0.8:7836", "127.0.0.1:7836",
		"169.254.10.10:7836", "[fd00::1]:7836", "not-an-addr"} {
		if addrPublic(a) {
			t.Fatalf("%s passed the public gate", a)
		}
	}
}

// seedDirRowForDial plants an employee_cache row with the fields the ladder
// reads: addrs, learned_via and dir_sig.
func seedDirRowForDial(t *testing.T, c *Core, id string, addrs []string, learnedVia, dirSig string) {
	t.Helper()
	aj, _ := json.Marshal(addrs)
	if _, err := c.db.Exec(`INSERT INTO employee_cache (id, addrs, learned_via, dir_sig, status)
		VALUES (?, ?, ?, ?, 'online')
		ON CONFLICT(id) DO UPDATE SET addrs=excluded.addrs,
			learned_via=excluded.learned_via, dir_sig=excluded.dir_sig`,
		id, string(aj), learnedVia, dirSig); err != nil {
		t.Fatalf("seed dir row: %v", err)
	}
}

func TestDialPeerAddrsProvenance(t *testing.T) {
	ctx := context.Background()

	t.Run("unsigned indirect refused", func(t *testing.T) {
		c := newCoreOnDB(t, openTestDB(t), "aa")
		// A gossip-learned row with NO signature: it must never aim a dial,
		// even at a public address.
		seedDirRowForDial(t, c, "ghost", []string{"203.0.113.9:7836"}, "via-peer", "")
		if c.dialPeerAddrs(ctx, "ghost") {
			t.Fatal("unsigned indirect row dialed")
		}
	})

	t.Run("indirect private addrs skipped", func(t *testing.T) {
		c := newCoreOnDB(t, openTestDB(t), "aa")
		// Signed gossip row — signed means servable, but its LAN hints are
		// meaningless off the originator's segment and stay undialed.
		seedDirRowForDial(t, c, "ghost", []string{"192.168.7.7:7836", "10.1.2.3:7836"}, "via-peer", "sig")
		if c.dialPeerAddrs(ctx, "ghost") {
			t.Fatal("private addr on indirect row dialed")
		}
	})

	t.Run("no row and no addrs", func(t *testing.T) {
		c := newCoreOnDB(t, openTestDB(t), "aa")
		if c.dialPeerAddrs(ctx, "stranger") {
			t.Fatal("dialed a peer with no directory row")
		}
		seedDirRowForDial(t, c, "ghost", nil, "", "sig")
		if c.dialPeerAddrs(ctx, "ghost") {
			t.Fatal("dialed a peer with an empty addr list")
		}
	})
}

func TestAutoPunchElection(t *testing.T) {
	ctx := context.Background()
	// The lexicographically-smaller node is the elected offerer: "aa" must
	// offer to "zz" itself, while "zz" only sends a punch_want hint back.
	small := newCoreOnDB(t, openTestDB(t), "aa")
	if err := small.ListenUDP(ctx, "127.0.0.1:0", nil); err != nil {
		t.Fatalf("udp: %v", err)
	}
	small.learnPeerUDP("zz", 7836, nil, "")
	small.autoPunch(ctx, "zz")
	small.path.mu.Lock()
	_, offered := small.path.punchAt["zz"]
	_, wanted := small.path.wantAt["zz"]
	small.path.mu.Unlock()
	if !offered {
		t.Fatal("elected offerer never marked its offer")
	}
	if wanted {
		t.Fatal("elected offerer sent a want — the roles are inverted")
	}

	large := newCoreOnDB(t, openTestDB(t), "zz")
	if err := large.ListenUDP(ctx, "127.0.0.1:0", nil); err != nil {
		t.Fatalf("udp: %v", err)
	}
	large.learnPeerUDP("aa", 7836, nil, "")
	large.autoPunch(ctx, "aa")
	large.path.mu.Lock()
	_, offered = large.path.punchAt["aa"]
	_, wanted = large.path.wantAt["aa"]
	large.path.mu.Unlock()
	if offered {
		t.Fatal("non-offerer sprayed an offer — double-punch")
	}
	if !wanted {
		t.Fatal("non-offerer never sent the punch_want hint")
	}

	// Interval gate: a second kick inside the window does neither.
	small.autoPunch(ctx, "zz")
	large.autoPunch(ctx, "aa") // wantAt dedupes internally too — no state change to assert beyond absence of panic.
}

func TestHandlePunchWant(t *testing.T) {
	ctx := context.Background()

	// "aa" is the elected offerer for pair (aa, zz): a want from "zz" is
	// honored — marked and offered.
	a := newCoreOnDB(t, openTestDB(t), "aa")
	if err := a.ListenUDP(ctx, "127.0.0.1:0", nil); err != nil {
		t.Fatalf("udp: %v", err)
	}
	a.learnPeerUDP("zz", 7836, nil, "")
	env, err := bus.NewEnvelope(bus.MsgPunchWant, "zz", "m1", bus.PunchWantPayload{TTL: bus.PunchMaxTTL})
	if err != nil {
		t.Fatal(err)
	}
	a.handlePunchWant(ctx, env)
	a.path.mu.Lock()
	_, marked := a.path.punchAt["zz"]
	a.path.mu.Unlock()
	if !marked {
		t.Fatal("elected offerer did not honor a want from the larger side")
	}

	// "zz" is NOT the offerer: a want from "aa" is nonsense and ignored —
	// this is also the loop-breaker between two confused nodes.
	z := newCoreOnDB(t, openTestDB(t), "zz")
	if err := z.ListenUDP(ctx, "127.0.0.1:0", nil); err != nil {
		t.Fatalf("udp: %v", err)
	}
	z.learnPeerUDP("aa", 7836, nil, "")
	env2, err := bus.NewEnvelope(bus.MsgPunchWant, "aa", "m2", bus.PunchWantPayload{TTL: bus.PunchMaxTTL})
	if err != nil {
		t.Fatal(err)
	}
	z.handlePunchWant(ctx, env2)
	z.path.mu.Lock()
	_, marked = z.path.punchAt["aa"]
	z.path.mu.Unlock()
	if marked {
		t.Fatal("non-offerer honored a want — election violated")
	}

	// Relay hop: a want addressed elsewhere is forwarded, not honored.
	env3, err := bus.NewEnvelope(bus.MsgPunchWant, "zz", "m3", bus.PunchWantPayload{TTL: bus.PunchMaxTTL})
	if err != nil {
		t.Fatal(err)
	}
	env3.To = "middle"
	a.handlePunchWant(ctx, env3)
	a.path.mu.Lock()
	count := len(a.path.punchAt)
	a.path.mu.Unlock()
	if count != 1 {
		t.Fatal("relayed want was also honored locally")
	}
}

func TestNatSuspectWindow(t *testing.T) {
	ctx := context.Background()
	c := newCoreOnDB(t, openTestDB(t), "aa")
	if err := c.ListenUDP(ctx, "127.0.0.1:0", nil); err != nil {
		t.Fatalf("udp: %v", err)
	}
	c.learnPeerUDP("zz", 7836, nil, "")
	c.path.init()

	// First failed window: the offer goes out, the spray runs, no route lands —
	// one strike. The verdict is applied when the window closes, not at offer
	// time: a pinhole confirmed inside the window is a success, and counting
	// before the spray had a chance marked every punch as a failure.
	c.autoPunch(ctx, "zz")
	c.notePunchAttempt("zz")
	c.path.mu.Lock()
	tries := c.path.punchTries["zz"]
	c.path.mu.Unlock()
	if tries != 1 {
		t.Fatalf("punchTries = %d, want 1", tries)
	}
	// Age the interval so the second window can fire.
	c.path.mu.Lock()
	c.path.punchAt["zz"] = time.Now().Add(-2 * autoPunchEvery)
	c.path.mu.Unlock()
	c.autoPunch(ctx, "zz")
	c.notePunchAttempt("zz")
	c.path.mu.Lock()
	suspect, marked := c.path.natSuspect["zz"]
	c.path.mu.Unlock()
	if !marked || time.Now().After(suspect) {
		t.Fatal("peer not marked nat_suspect after two failed windows")
	}
	// While suspected, a further kick is a no-op: punchAt stays stale.
	c.path.mu.Lock()
	before := c.path.punchAt["zz"]
	c.path.mu.Unlock()
	c.autoPunch(ctx, "zz")
	c.path.mu.Lock()
	if !c.path.punchAt["zz"].Equal(before) {
		t.Fatal("nat_suspect peer was re-punched inside the window")
	}
	c.path.mu.Unlock()
	// A confirmed UDP route clears the mark.
	c.path.mu.Lock()
	c.path.natSuspect["zz"] = time.Now().Add(-time.Second)
	c.udpMu.Lock()
	if c.udpRoutes == nil {
		c.udpRoutes = map[string]*net.UDPAddr{}
	}
	c.udpRoutes["zz"] = &net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 7836}
	c.udpMu.Unlock()
	c.path.punchAt["zz"] = time.Now().Add(-2 * autoPunchEvery)
	c.path.mu.Unlock()
	c.autoPunch(ctx, "zz")
	c.path.mu.Lock()
	_, still := c.path.natSuspect["zz"]
	c.path.mu.Unlock()
	if still {
		t.Fatal("confirmed route did not clear nat_suspect")
	}
}

func TestSeedPunchCandidates(t *testing.T) {
	ctx := context.Background()
	c := newCoreOnDB(t, openTestDB(t), "aa")
	// Indirect signed row: public hints become spray candidates at udp_port,
	// private ones do not.
	seedDirRowForDial(t, c, "ghost",
		[]string{"203.0.113.9:7836", "192.168.4.4:7836"}, "via-peer", "sig")
	c.seedPunchCandidates(ctx, "ghost", 7800)
	c.udpMu.Lock()
	cands := append([]string(nil), c.udpCands["ghost"]...)
	c.udpMu.Unlock()
	if len(cands) != 1 || cands[0] != "203.0.113.9:7800" {
		t.Fatalf("synthesized candidates = %v, want [203.0.113.9:7800]", cands)
	}
	// Unsigned indirect rows seed nothing.
	seedDirRowForDial(t, c, "ghost2", []string{"203.0.113.9:7836"}, "via-peer", "")
	c.seedPunchCandidates(ctx, "ghost2", 7800)
	c.udpMu.Lock()
	if len(c.udpCands["ghost2"]) != 0 {
		t.Fatal("unsigned indirect row fed spray candidates")
	}
	c.udpMu.Unlock()
}

func TestKickReachableDedupe(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "aa")
	c.kickReachable("zz")
	c.path.mu.Lock()
	first, ok := c.path.kickAt["zz"]
	c.path.mu.Unlock()
	if !ok {
		t.Fatal("kick never recorded")
	}
	time.Sleep(5 * time.Millisecond)
	c.kickReachable("zz")
	c.path.mu.Lock()
	if !c.path.kickAt["zz"].Equal(first) {
		t.Fatal("dedupe window let a second kick through")
	}
	c.path.mu.Unlock()
	// Self and session ids never kick.
	c.kickReachable("aa")
	c.kickReachable("laptop-1a2b3c4d")
	c.path.mu.Lock()
	_, selfKicked := c.path.kickAt["aa"]
	_, sessKicked := c.path.kickAt["laptop-1a2b3c4d"]
	c.path.mu.Unlock()
	if selfKicked || sessKicked {
		t.Fatal("self/session id kicked the ladder")
	}
}

// TestNatStatePersistsAcrossRestart: the nat_suspect window survives a
// process restart (design §6.2's nat_state), so a node does not re-spray a
// symmetric-NAT peer for two full windows every time it boots.
func TestNatStatePersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c1 := newCoreOnDB(t, db, "n1")
	c1.notePunchAttempt("peer-a")
	c1.notePunchAttempt("peer-a") // reaches natSuspectAfter
	c1.path.mu.Lock()
	raised := c1.path.natSuspect["peer-a"].After(time.Now())
	c1.path.mu.Unlock()
	if !raised {
		t.Fatal("suspicion never raised in the first place")
	}

	c2 := newCoreOnDB(t, db, "n2")
	c2.loadNatState(ctx)
	c2.path.mu.Lock()
	until, ok := c2.path.natSuspect["peer-a"]
	c2.path.mu.Unlock()
	if !ok || !until.After(time.Now()) {
		t.Fatalf("suspect state not restored: ok=%v until=%v", ok, until)
	}

	// A window that elapsed while the node was off is over, not paused.
	if _, err := db.Exec(`UPDATE settings SET value = ? WHERE key = ?`,
		`{"until":{"peer-old":1}}`, natStateSetting); err != nil {
		t.Fatal(err)
	}
	c3 := newCoreOnDB(t, db, "n3")
	c3.loadNatState(ctx)
	c3.path.mu.Lock()
	_, staleKept := c3.path.natSuspect["peer-old"]
	c3.path.mu.Unlock()
	if staleKept {
		t.Fatal("expired suspect entry must be dropped on load")
	}
}

// TestNatStateClearsOnRoute: a proven UDP route wipes the peer's suspicion
// and the persisted copy with it.
func TestNatStateClearsOnRoute(t *testing.T) {
	ctx := context.Background()
	c := newCoreOnDB(t, openTestDB(t), "n1")
	c.path.mu.Lock()
	c.path.init()
	c.path.natSuspect["peer-a"] = time.Now().Add(time.Minute)
	c.path.punchTries["peer-a"] = 1
	c.path.mu.Unlock()
	// No UDP plane means UDPRoute is nil, so drive the clear directly.
	c.path.mu.Lock()
	delete(c.path.natSuspect, "peer-a")
	delete(c.path.punchTries, "peer-a")
	snap := c.snapshotNatStateLocked()
	c.path.mu.Unlock()
	c.saveNatState(ctx, snap)
	var n int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, natStateSetting).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("empty snapshot should delete the persisted key")
	}
}

// TestRelayedPunchWantKeepsItsRequester pins the origin across a relay hop.
//
// A punch_want travels the mesh to the elected offerer, and a relayed
// envelope is re-minted as the FORWARDER's (env.From = this node) because
// the receiving conn authenticates envelopes by their bound peer id. The
// requester therefore survives only in the payload — and it used to survive
// nowhere, so after a relay hop the offerer punched the RELAY (the one node
// already reachable) instead of the node that asked.
func TestRelayedPunchWantKeepsItsRequester(t *testing.T) {
	ctx := context.Background()

	// "aa" is the elected offerer for the pair (aa, zz).
	a := newCoreOnDB(t, openTestDB(t), "aa")
	if err := a.ListenUDP(ctx, "127.0.0.1:0", nil); err != nil {
		t.Fatalf("udp: %v", err)
	}
	a.learnPeerUDP("zz", 7836, nil, "")

	// A want that crossed a relay: the envelope's From is the RELAY (the
	// conn it arrived on), and the payload carries the real requester.
	env, err := bus.NewEnvelope(bus.MsgPunchWant, "middle", "m-relay", bus.PunchWantPayload{
		Src: "zz",
		TTL: bus.PunchMaxTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	a.handlePunchWant(ctx, env)

	a.path.mu.Lock()
	_, markedRequester := a.path.punchAt["zz"]
	_, markedRelay := a.path.punchAt["middle"]
	a.path.mu.Unlock()
	if !markedRequester {
		t.Fatal("the offerer did not honor the want's real requester")
	}
	if markedRelay {
		t.Fatal("the offerer punched the relay instead of the requester")
	}
}

// TestRelayedPunchWantReachesTheRequester pins the PRODUCER half of the
// punch_want contract end to end: a want that crosses a relay must make the
// elected offerer punch the REQUESTER, not the relay.
//
// The consumer half was already pinned by
// TestRelayedPunchWantKeepsItsRequester, but a producer that omitted Src
// passed every test: the relayed want then carried an empty Src, the relay
// re-minted env.From as itself, and the offerer punched the RELAY — the one
// node already reachable — instead of the node that asked.
//
// The sender's transit plane is off here on purpose: the sealed transit path
// keeps the original From inside the inner envelope, so the plain-WS relay —
// where the envelope is re-minted as the forwarder — is the path that loses
// the requester, and it is the one this test drives.
func TestRelayedPunchWantReachesTheRequester(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	requester := newCoreWithNative(t, "zz-want-req", ledger.NativeAbility{ID: "x", Command: "true"})
	relay := newCoreWithNative(t, "mm-want-relay", ledger.NativeAbility{ID: "x", Command: "true"})
	offerer := newCoreWithNative(t, "aa-want-off", ledger.NativeAbility{ID: "x", Command: "true"})
	defer requester.Shutdown(ctx)
	defer relay.Shutdown(ctx)
	defer offerer.Shutdown(ctx)

	// The sender's transit plane is off: the plain-WS relay is the path that
	// re-mints the envelope and loses the requester.
	requester.SetNetworkPolicy(config.NetworkConfig{Transit: false})

	for _, c := range []*Core{requester, relay, offerer} {
		if err := c.Register(ctx); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	// The kernel picks the port; probing one and closing it before the
	// listener takes it is a race with the rest of the test binary.
	relayAddr := startListen(t, ctx, relay, "127.0.0.1:0")
	dial := func(c *Core) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			err := c.DialPeer(ctx, relayAddr)
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("dial relay: %v", err)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	dial(requester)
	dial(offerer)
	waitPeer(t, requester, relay.nodeID)
	waitPeer(t, offerer, relay.nodeID)
	waitPeer(t, relay, requester.nodeID)
	waitPeer(t, relay, offerer.nodeID)

	// The link state the routing consults: the requester reaches the offerer
	// through the relay, and the relay reaches the offerer directly.
	if _, err := requester.db.Exec(
		`UPDATE employee_cache SET neighbors_json=? WHERE id=?`,
		`["`+relay.nodeID+`"]`, requester.nodeID); err != nil {
		t.Fatalf("seed requester neighbors: %v", err)
	}
	if _, err := relay.db.Exec(
		`UPDATE employee_cache SET neighbors_json=? WHERE id=?`,
		`["`+offerer.nodeID+`"]`, relay.nodeID); err != nil {
		t.Fatalf("seed relay neighbors: %v", err)
	}
	if err := ledger.UpsertRemote(requester.db, relay.nodeID, ledger.CapabilitySummary{
		Device:    relay.nodeID,
		Neighbors: []string{requester.nodeID, offerer.nodeID},
	}); err != nil {
		t.Fatalf("upsert remote: %v", err)
	}
	if err := ledger.UpsertRemote(relay.db, offerer.nodeID, ledger.CapabilitySummary{
		Device:    offerer.nodeID,
		Neighbors: []string{relay.nodeID},
	}); err != nil {
		t.Fatalf("upsert remote: %v", err)
	}

	// The requester asks the elected offerer to punch it. "zz" > "aa", so the
	// offerer is the elected side and the requester sends the want.
	requester.sendPunchWant(ctx, offerer.nodeID)

	deadline := time.Now().Add(5 * time.Second)
	for {
		offerer.path.mu.Lock()
		_, markedRequester := offerer.path.punchAt[requester.nodeID]
		_, markedRelay := offerer.path.punchAt[relay.nodeID]
		offerer.path.mu.Unlock()
		if markedRequester {
			if markedRelay {
				t.Fatal("the offerer punched the relay as well as the requester")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the offerer never marked the requester (relay marked=%v)", markedRelay)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestShutdownCancelsAndJoinsReachability(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "shutdown-reach")
	started, settled := make(chan struct{}), make(chan struct{})
	if !c.goReach(context.Background(), "test reach", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(settled)
	}) {
		t.Fatal("reach work was not launched")
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c.Shutdown(ctx)
	select {
	case <-settled:
	default:
		t.Fatal("Shutdown returned before reachability settled")
	}
	if c.goReach(context.Background(), "late reach", func(context.Context) { t.Error("reach launched after shutdown") }) {
		t.Fatal("late work accepted")
	}
}
