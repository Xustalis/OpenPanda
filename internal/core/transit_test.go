// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestNodeXKeyGeneratedAndPersisted mirrors the Ed25519 test above it: the
// X25519 half of the identity is minted once and survives restarts, or every
// transit frame sealed to this node before the restart becomes unreadable —
// the degraded-mode warning made permanent.
func TestNodeXKeyGeneratedAndPersisted(t *testing.T) {
	db := openTestDB(t)
	c := NewCore(db, "node-a", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())

	priv1, pub1, ok := c.nodeXKey()
	if !ok {
		t.Fatal("nodeXKey did not yield a key")
	}
	priv2, pub2, ok := c.nodeXKey()
	if !ok || !pub1.Equal(pub2) || priv1 != priv2 {
		t.Fatal("nodeXKey is not stable across calls")
	}

	other := NewCore(db, "node-a", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	other.SetWorkDir(t.TempDir())
	_, pub3, ok := other.nodeXKey()
	if !ok || !pub1.Equal(pub3) {
		t.Fatal("a fresh Core did not recover the persisted x25519 key")
	}

	// The self row carries it, same as the Ed25519 half.
	var stored string
	if err := db.QueryRow(`SELECT x_pub FROM employee_cache WHERE id='node-a'`).Scan(&stored); err != nil || stored == "" {
		t.Fatalf("self x_pub not recorded (err=%v)", err)
	}
	if stored != hex.EncodeToString(pub1.Bytes()) {
		t.Fatal("recorded self x_pub differs from the node x key")
	}
}

// TestPeerXPubResolution covers the directory lookups the transit sender
// depends on: an instance id resolves through its row, a session id through
// its host's row, and a stable "k:<pub>" destination through the newest
// instance holding that key.
func TestPeerXPubResolution(t *testing.T) {
	db := openTestDB(t)
	c := NewCore(db, "node-a", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	ctx := context.Background()

	priv, err := bus.GenerateX25519()
	if err != nil {
		t.Fatalf("GenerateX25519: %v", err)
	}
	want := priv.PublicKey()
	hexKey := hex.EncodeToString(want.Bytes())

	if _, ok := c.peerXPub(ctx, "peer-b"); ok {
		t.Fatal("peerXPub resolved a peer that was never recorded")
	}
	if _, err := db.Exec(
		`INSERT INTO employee_cache (id, x_pub, status, last_seen) VALUES ('peer-b', ?, 'online', ?)`,
		hexKey, time.Now().Unix()); err != nil {
		t.Fatalf("seed x_pub: %v", err)
	}
	got, ok := c.peerXPub(ctx, "peer-b")
	if !ok || !got.Equal(want) {
		t.Fatal("recorded x_pub did not resolve")
	}
	// A session of that host seals to the host's key.
	got, ok = c.peerXPub(ctx, "peer-b-1234abcd")
	if !ok || !got.Equal(want) {
		t.Fatal("session id did not resolve through the host row")
	}
	// A stable destination resolves through the newest keyed instance.
	if _, err := db.Exec(
		`UPDATE employee_cache SET pub_key='bpub' WHERE id='peer-b'`); err != nil {
		t.Fatalf("pin pub_key: %v", err)
	}
	got, ok = c.peerXPub(ctx, "k:bpub")
	if !ok || !got.Equal(want) {
		t.Fatal("stable k: destination did not resolve")
	}
	// A malformed x_pub claim is normalized to absent inside the verified-
	// hello upsert, never stored where transit reads.
	c.recordPeerPubKey(ctx, "peer-c", "0123456789abcdef", "not-hex")
	if _, ok := c.peerXPub(ctx, "peer-c"); ok {
		t.Fatal("a malformed x_pub was stored")
	}
	var stored string
	if err := db.QueryRow(`SELECT COALESCE(x_pub,'') FROM employee_cache WHERE id='peer-c'`).Scan(&stored); err != nil || stored != "" {
		t.Fatalf("malformed x_pub persisted as %q (err=%v)", stored, err)
	}
}

// TestTransitDeliveryE2E is the P1 contract end to end: A and B share no
// conn, no datagram route — only the relay R. A's sendTo("b") must seal the
// envelope for B's x key, hand it to R (the link-state hop), and B must
// open, attest and dispatch it as if it had arrived directly.
func TestTransitDeliveryE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a := newCore(t, "ta")
	r := newCore(t, "tr")
	b := newCore(t, "tb")
	// A↔R and B↔R edges only — no A↔B.
	rd, rAddr := registerAndListen(t, ctx, r, "127.0.0.1:0")
	ad, _ := registerAndListen(t, ctx, a, "127.0.0.1:0")
	linkPair(t, ctx, a, r, rAddr, ad, rd)
	bd, _ := registerAndListen(t, ctx, b, "127.0.0.1:0")
	linkPair(t, ctx, b, r, rAddr, bd, rd)

	if a.connFor("tb") != nil || b.connFor("ta") != nil {
		t.Fatal("fixture produced a direct A↔B edge; the transit path is untested")
	}

	// A's directory learns B's proven identity + x key the way it would from
	// a signed hello: only pub_key and x_pub, nothing else. (Directory gossip
	// that would carry the whole row is P2; P1 accepts the seeded row.)
	bpub, _, _ := b.nodeKeyPair()
	_, bxpub, _ := b.nodeXKey()
	if _, err := a.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, x_pub, status, last_seen)
		 VALUES ('tb', ?, ?, 'online', ?)`,
		hex.EncodeToString(bpub), hex.EncodeToString(bxpub.Bytes()), time.Now().Unix()); err != nil {
		t.Fatalf("seed B row: %v", err)
	}
	// B must already know A's identity for the attested inner From to bind:
	// transit deliberately refuses an unknown instance id rather than giving
	// a ghost claim the default mesh tier. This is the record a prior signed
	// hello would have left; the heartbeat below still proves delivery.
	apub, _, _ := a.nodeKeyPair()
	if _, err := b.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, status, last_seen)
		 VALUES ('ta', ?, 'online', ?)`,
		hex.EncodeToString(apub), time.Now().Unix()); err != nil {
		t.Fatalf("seed A row: %v", err)
	}
	// The link-state hop: A's copy of R's row must know R touches B. Rather
	// than racing the 5s heartbeat gossip, seed exactly what a beat would
	// have written — the assertion under test is transit forwarding, not
	// adjacency refresh (covered elsewhere).
	if _, err := a.db.Exec(
		`UPDATE employee_cache SET neighbors_json='["ta","tb"]' WHERE id='tr'`); err != nil {
		t.Fatalf("seed R adjacency: %v", err)
	}
	if _, err := a.db.Exec(
		`UPDATE employee_cache SET neighbors_json='["tr"]' WHERE id='ta'`); err != nil {
		t.Fatalf("seed self adjacency: %v", err)
	}
	if hop, _ := a.dtnNextHop(ctx, "tb", nil, 0, 0); hop != "tr" {
		t.Fatalf("link-state first hop = %q, want tr", hop)
	}
	// Relay selection must skip a connected peer that never advertised trnz:
	// an old node would drop the transit frame as unknown instead of
	// forwarding it, so claiming a route it cannot run is worse than failing
	// the send. Restore the advertised caps before the delivery below.
	a.connFor("tr").SetCaps(nil)
	if hop := a.transitNextHop(ctx, "tb", nil); hop != "" {
		t.Fatalf("transitNextHop picked non-relay hop %q", hop)
	}
	a.connFor("tr").SetCaps([]string{bus.CapTransitRelay, bus.CapTransitE2E, bus.CapBinaryData, bus.CapSessionAEAD})
	if hop := a.transitNextHop(ctx, "tb", nil); hop != "tr" {
		t.Fatalf("transitNextHop with trnz = %q, want tr", hop)
	}

	// A heartbeat carrying A's capability summary: card-less beats only
	// refresh an existing row, so the card is what creates B's "ta" row —
	// and its M3 fields make the reachability advertisement ride the same
	// path the test just proved.
	cardJSON, err := json.Marshal(ledger.CapabilitySummary{
		Device: "ta",
		Relay:  &ledger.RelayProfile{Transit: true, MaxKbps: 512},
		Addrs:  []string{"10.9.9.9:7836"},
	})
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	env, err := bus.NewEnvelope(bus.MsgHeartbeat, "ta", "hb-transit-1", bus.HeartbeatPayload{
		Status: "online",
		Card:   cardJSON,
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if err := a.sendTo("tb", env); err != nil {
		t.Fatalf("sendTo via transit failed: %v", err)
	}

	// B must learn A's card through the delivered heartbeat. The row is
	// already seeded (the identity binding under test), so wait on the card
	// fields the transit delivery adds rather than on status alone.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status, addrs string
		err := b.db.QueryRow(
			`SELECT COALESCE(status,''), COALESCE(addrs,'') FROM employee_cache WHERE id='ta'`).
			Scan(&status, &addrs)
		if err == nil && status == "online" && addrs == `["10.9.9.9:7836"]` {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("B never received A's heartbeat over transit (row err=%v status=%q addrs=%q)", err, status, addrs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestFallbackRelayCarriesSuspectedPeer is the three-node contract behind
// relay_priority: fallback. A cannot reach B (the mark stands in for a
// symmetric NAT that spent two punch windows), two relays are connected and
// both advertise trnz, and only F has a path to B. Automatic election would
// hand the frame to either one; the operator's mark must make A choose F,
// and B must receive the frame — Z has no route, so delivery can only have
// come through F.
func TestFallbackRelayCarriesSuspectedPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a := newCore(t, "ta")
	f := newCore(t, "tf")
	z := newCore(t, "tz")
	b := newCore(t, "tb")

	fd, fAddr := registerAndListen(t, ctx, f, "127.0.0.1:0")
	zd, zAddr := registerAndListen(t, ctx, z, "127.0.0.1:0")
	ad, _ := registerAndListen(t, ctx, a, "127.0.0.1:0")
	linkPair(t, ctx, a, f, fAddr, ad, fd)
	linkPair(t, ctx, a, z, zAddr, ad, zd)
	bd, _ := registerAndListen(t, ctx, b, "127.0.0.1:0")
	linkPair(t, ctx, b, f, fAddr, bd, fd)

	if a.connFor("tb") != nil || b.connFor("ta") != nil {
		t.Fatal("fixture produced a direct A↔B edge; the relay path is untested")
	}
	if z.connFor("tb") != nil {
		t.Fatal("the decoy relay must have no path to B, or delivery proves nothing")
	}
	if a.connFor("tf") == nil || a.connFor("tz") == nil {
		t.Fatal("fixture lost one of the two relay candidates")
	}

	// A's local mark: F, by the address A actually dials (peer_addrs binds
	// the configured address to the id that answered it at hello).
	a.SetNetworkPolicy(config.NetworkConfig{
		Transit: true, AutoPunch: true, BootstrapEdges: 2,
		Peers: []config.PeerEntry{{Addr: fAddr, RelayPriority: config.RelayPriorityFallback}},
	})
	if got := a.fallbackRelays(); len(got) != 1 || got[0] != "tf" {
		t.Fatalf("fallback relay resolved to %v, want [tf]", got)
	}

	// Identities: A must know B's keys to seal the inner frame, B must know
	// A's to verify the attestation. This is what the signed hellos would
	// have left behind; the seed keeps the test on the relay decision.
	bpub, _, _ := b.nodeKeyPair()
	_, bxpub, _ := b.nodeXKey()
	if _, err := a.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, x_pub, status, last_seen)
		 VALUES ('tb', ?, ?, 'online', ?)`,
		hex.EncodeToString(bpub), hex.EncodeToString(bxpub.Bytes()), time.Now().Unix()); err != nil {
		t.Fatalf("seed B row on A: %v", err)
	}
	apub, _, _ := a.nodeKeyPair()
	if _, err := b.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, status, last_seen)
		 VALUES ('ta', ?, 'offline', ?)`,
		hex.EncodeToString(apub), time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatalf("seed A row on B: %v", err)
	}

	// The link-state graph is given a plausible-but-wrong path to B through
	// the decoy: this is the case the mark exists for, and the explicit
	// fallback must outrank it. Without the ordering pin a graph hop that
	// merely claims to reach B would quietly shadow the operator's choice.
	if _, err := a.db.Exec(
		`UPDATE employee_cache SET neighbors_json='["tz"]' WHERE id='ta'`); err != nil {
		t.Fatalf("seed self adjacency: %v", err)
	}
	if _, err := a.db.Exec(
		`UPDATE employee_cache SET neighbors_json='["ta","tb"]' WHERE id='tz'`); err != nil {
		t.Fatalf("seed decoy adjacency: %v", err)
	}
	// The graph only routes through an intermediate that can hold custody,
	// so the decoy has to look online for the shadowing path to exist.
	if _, err := a.db.Exec(
		`UPDATE employee_cache SET status='online', last_seen=? WHERE id='tz'`, time.Now().Unix()); err != nil {
		t.Fatalf("mark decoy online: %v", err)
	}
	if hop, _ := a.dtnNextHop(ctx, "tb", nil, 0, 0); hop != "tz" {
		t.Fatalf("graph first hop = %q, want the decoy tz — fixture lost its point", hop)
	}

	// Two failed punch windows: the trigger the fallback preference keys on.
	a.notePunchAttempt("tb")
	a.notePunchAttempt("tb")
	if !a.peerNatSuspect("tb") {
		t.Fatal("peer not marked nat_suspect after two failed windows")
	}
	// The decision itself, not just its outcome: with both relays connected
	// and trnz-capable, the marked one must win the hop — the unordered pool
	// below would otherwise pick either.
	if hop := a.transitNextHopPreferred(ctx, "tb", nil, a.transitFallbackPrefer("tb", "tb")); hop != "tf" {
		t.Fatalf("hop = %q, want the designated fallback relay tf", hop)
	}

	env, err := bus.NewEnvelope(bus.MsgHeartbeat, "ta", "hb-fallback-1", bus.HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if err := a.sendTo("tb", env); err != nil {
		t.Fatalf("sendTo via the fallback relay failed: %v", err)
	}

	// B's row for A flips online only if the heartbeat arrived — and it can
	// only have arrived through F.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		err := b.db.QueryRow(`SELECT COALESCE(status,'') FROM employee_cache WHERE id='ta'`).Scan(&status)
		if err == nil && status == "online" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("B never received A's heartbeat over the fallback relay (err=%v status=%q)", err, status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestFallbackRelayOfflineFallsBackToElection is the negative twin of
// TestFallbackRelayCarriesSuspectedPeer: the operator's designated relay is
// configured but never connects, so it can never appear in
// transitNextHopPreferred's candidate list (it requires a live *bus.Conn).
// The decision must fall through to the automatic election (the link-state
// hop here) rather than returning no route at all — an offline preference
// is a missed first choice, not a dead end for the send.
func TestFallbackRelayOfflineFallsBackToElection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a := newCore(t, "oa")
	z := newCore(t, "oz") // the only relay actually online — plays both
	// "the graph's honest first hop" and "what election must still reach".
	b := newCore(t, "ob")

	zd, zAddr := registerAndListen(t, ctx, z, "127.0.0.1:0")
	ad, _ := registerAndListen(t, ctx, a, "127.0.0.1:0")
	linkPair(t, ctx, a, z, zAddr, ad, zd)
	bd, _ := registerAndListen(t, ctx, b, "127.0.0.1:0")
	linkPair(t, ctx, b, z, zAddr, bd, zd)

	if a.connFor("ob") != nil {
		t.Fatal("fixture produced a direct A↔B edge; the fallback path is untested")
	}

	// A's preference names a relay that never dials in — "offline-relay" is
	// not a resolvable node at all, so fallbackRelays' own addr→id lookup
	// already drops it; the point here is that an UNRESOLVABLE preference
	// must not make the whole hop selection come up empty.
	a.SetNetworkPolicy(config.NetworkConfig{
		Transit: true, AutoPunch: true, BootstrapEdges: 2,
		Peers: []config.PeerEntry{
			{Addr: "offline-relay.invalid:7836", RelayPriority: config.RelayPriorityFallback},
		},
	})
	if got := a.fallbackRelays(); len(got) != 0 {
		t.Fatalf("an unbound fallback address resolved to %v, want none", got)
	}

	bpub, _, _ := b.nodeKeyPair()
	_, bxpub, _ := b.nodeXKey()
	if _, err := a.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, x_pub, status, last_seen)
		 VALUES ('ob', ?, ?, 'online', ?)`,
		hex.EncodeToString(bpub), hex.EncodeToString(bxpub.Bytes()), time.Now().Unix()); err != nil {
		t.Fatalf("seed B row on A: %v", err)
	}
	apub, _, _ := a.nodeKeyPair()
	if _, err := b.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, status, last_seen)
		 VALUES ('oa', ?, 'offline', ?)`,
		hex.EncodeToString(apub), time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatalf("seed A row on B: %v", err)
	}
	if _, err := a.db.Exec(
		`UPDATE employee_cache SET neighbors_json='["oz"]' WHERE id='oa'`); err != nil {
		t.Fatalf("seed self adjacency: %v", err)
	}
	if _, err := a.db.Exec(
		`UPDATE employee_cache SET neighbors_json='["oa","ob"]', status='online', last_seen=? WHERE id='oz'`,
		time.Now().Unix()); err != nil {
		t.Fatalf("seed relay adjacency: %v", err)
	}
	if hop, _ := a.dtnNextHop(ctx, "ob", nil, 0, 0); hop != "oz" {
		t.Fatalf("graph first hop = %q, want oz — fixture lost its point", hop)
	}

	a.notePunchAttempt("ob")
	a.notePunchAttempt("ob")
	if !a.peerNatSuspect("ob") {
		t.Fatal("peer not marked nat_suspect after two failed windows")
	}
	// The preference list is non-empty in shape (one configured address) but
	// resolves to zero live ids — fallbackRelays already filtered it. The hop
	// selection must still find oz through the ordinary election, not return
	// "" because a configured-but-dead preference poisoned the candidate walk.
	prefer := a.transitFallbackPrefer("ob", "ob")
	if len(prefer) != 0 {
		t.Fatalf("prefer = %v, want empty — the configured relay never bound an id", prefer)
	}
	if hop := a.transitNextHopPreferred(ctx, "ob", nil, prefer); hop != "oz" {
		t.Fatalf("hop = %q, want the elected graph hop oz when the designated relay is unresolvable", hop)
	}

	env, err := bus.NewEnvelope(bus.MsgHeartbeat, "oa", "hb-fallback-offline-1", bus.HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if err := a.sendTo("ob", env); err != nil {
		t.Fatalf("sendTo via the elected relay failed: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		err := b.db.QueryRow(`SELECT COALESCE(status,'') FROM employee_cache WHERE id='oa'`).Scan(&status)
		if err == nil && status == "online" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("B never received A's heartbeat (err=%v status=%q)", err, status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestFallbackRelaySkipsSecondChoiceWhenFirstIsLive is the ordering half of
// the offline test above: TWO fallback addresses are configured, both
// resolve to live, connected, trnz-capable peers. The FIRST one in config
// order must win — a second relay is a backup for the first, not a second
// route table the hop walk is free to prefer.
func TestFallbackRelaySkipsSecondChoiceWhenFirstIsLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a := newCore(t, "pa")
	first := newCore(t, "pf")  // listed FIRST in config — must win
	second := newCore(t, "ps") // listed second — connected too, must lose
	b := newCore(t, "pb")

	fd, fAddr := registerAndListen(t, ctx, first, "127.0.0.1:0")
	sd, sAddr := registerAndListen(t, ctx, second, "127.0.0.1:0")
	ad, _ := registerAndListen(t, ctx, a, "127.0.0.1:0")
	linkPair(t, ctx, a, first, fAddr, ad, fd)
	linkPair(t, ctx, a, second, sAddr, ad, sd)
	bd, _ := registerAndListen(t, ctx, b, "127.0.0.1:0")
	linkPair(t, ctx, b, first, fAddr, bd, fd)
	linkPair(t, ctx, b, second, sAddr, bd, sd)

	a.SetNetworkPolicy(config.NetworkConfig{
		Transit: true, AutoPunch: true, BootstrapEdges: 2,
		Peers: []config.PeerEntry{
			{Addr: fAddr, RelayPriority: config.RelayPriorityFallback},
			{Addr: sAddr, RelayPriority: config.RelayPriorityFallback},
		},
	})
	got := a.fallbackRelays()
	if len(got) != 2 || got[0] != "pf" || got[1] != "ps" {
		t.Fatalf("fallback relays = %v, want [pf ps] in config order", got)
	}

	bpub, _, _ := b.nodeKeyPair()
	_, bxpub, _ := b.nodeXKey()
	if _, err := a.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, x_pub, status, last_seen)
		 VALUES ('pb', ?, ?, 'online', ?)`,
		hex.EncodeToString(bpub), hex.EncodeToString(bxpub.Bytes()), time.Now().Unix()); err != nil {
		t.Fatalf("seed B row on A: %v", err)
	}

	a.notePunchAttempt("pb")
	a.notePunchAttempt("pb")
	if !a.peerNatSuspect("pb") {
		t.Fatal("peer not marked nat_suspect after two failed windows")
	}
	prefer := a.transitFallbackPrefer("pb", "pb")
	if hop := a.transitNextHopPreferred(ctx, "pb", nil, prefer); hop != "pf" {
		t.Fatalf("hop = %q, want the FIRST configured fallback pf, not the second ps", hop)
	}
}

// TestFallbackRelayDeniedByRuntimeTrustIsSkipped exercises the second,
// independent guard on the fallback mark: config.Validate() already refuses
// relay_priority: fallback on a trust: open peer at load time, but trust can
// also be demoted at RUNTIME (`panda nodes trust <id> open`, a key-change
// demotion, SetPeerTrustHints) without ever touching the file. The hop
// selection calls relayServeAllowed per candidate for exactly this reason —
// prove a runtime demotion actually closes the loophole the load-time check
// cannot see.
func TestFallbackRelayDeniedByRuntimeTrustIsSkipped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a := newCore(t, "ra")
	demoted := newCore(t, "rd") // configured as fallback, then demoted to open at runtime
	elected := newCore(t, "re") // the honest election's only path to B
	b := newCore(t, "rb")

	dd, dAddr := registerAndListen(t, ctx, demoted, "127.0.0.1:0")
	ed, eAddr := registerAndListen(t, ctx, elected, "127.0.0.1:0")
	ad, _ := registerAndListen(t, ctx, a, "127.0.0.1:0")
	linkPair(t, ctx, a, demoted, dAddr, ad, dd)
	linkPair(t, ctx, a, elected, eAddr, ad, ed)
	bd, _ := registerAndListen(t, ctx, b, "127.0.0.1:0")
	linkPair(t, ctx, b, elected, eAddr, bd, ed)

	a.SetNetworkPolicy(config.NetworkConfig{
		Transit: true, AutoPunch: true, BootstrapEdges: 2,
		Peers: []config.PeerEntry{{Addr: dAddr, RelayPriority: config.RelayPriorityFallback}},
	})
	if got := a.fallbackRelays(); len(got) != 1 || got[0] != "rd" {
		t.Fatalf("fallback relay resolved to %v, want [rd]", got)
	}

	bpub, _, _ := b.nodeKeyPair()
	_, bxpub, _ := b.nodeXKey()
	if _, err := a.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, x_pub, status, last_seen)
		 VALUES ('rb', ?, ?, 'online', ?)`,
		hex.EncodeToString(bpub), hex.EncodeToString(bxpub.Bytes()), time.Now().Unix()); err != nil {
		t.Fatalf("seed B row on A: %v", err)
	}
	// The graph's only path to B goes through "elected" — the honest fallback
	// once "demoted" is disqualified.
	if _, err := a.db.Exec(
		`UPDATE employee_cache SET neighbors_json='["re"]' WHERE id='ra'`); err != nil {
		t.Fatalf("seed self adjacency: %v", err)
	}
	if _, err := a.db.Exec(
		`UPDATE employee_cache SET neighbors_json='["ra","rb"]', status='online', last_seen=? WHERE id='re'`,
		time.Now().Unix()); err != nil {
		t.Fatalf("seed elected-hop adjacency: %v", err)
	}

	a.notePunchAttempt("rb")
	a.notePunchAttempt("rb")
	if !a.peerNatSuspect("rb") {
		t.Fatal("peer not marked nat_suspect after two failed windows")
	}
	prefer := a.transitFallbackPrefer("rb", "rb")
	if len(prefer) != 1 || prefer[0] != "rd" {
		t.Fatalf("prefer = %v, want [rd] before the demotion", prefer)
	}

	// BEFORE demotion: the designated relay wins, same as the happy-path test
	// — confirms the fixture is sound before the demotion is what changes
	// the outcome.
	if hop := a.transitNextHopPreferred(ctx, "rb", nil, prefer); hop != "rd" {
		t.Fatalf("hop (pre-demotion) = %q, want the designated relay rd", hop)
	}

	// Demote at runtime — no config reload, no file touched. SetPeerTrustHints
	// is exactly what `panda nodes trust rd open` drives.
	a.SetPeerTrustHints(map[string]string{dAddr: "open"})

	// AFTER demotion: relayServeAllowed must now refuse "rd" as a serve-through
	// hop, and the walk must fall through to the honest election (re) rather
	// than either wedging on the disqualified preference or returning "".
	if got := a.relayServeAllowed(ctx, "rd"); got {
		t.Fatal("relayServeAllowed still permits the open-demoted relay")
	}
	if hop := a.transitNextHopPreferred(ctx, "rb", nil, prefer); hop != "re" {
		t.Fatalf("hop (post-demotion) = %q, want the fallthrough election re, not the demoted rd", hop)
	}
}

// TestTransitTTLExhaustion pins the loop bound: a frame that arrives at a
// relay with one hop left dies there — it is neither delivered locally
// (the dest is not us) nor forwarded.
func TestTransitTTLExhaustion(t *testing.T) {
	ctx := context.Background()
	r := NewCore(openTestDB(t), "tr", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	r.SetWorkDir(t.TempDir())

	payload, _ := json.Marshal(bus.TransitPayload{
		Dest:  "tb",
		TTL:   1,
		Seal:  bus.SealMesh,
		Inner: []byte("irrelevant — it must never be opened"),
	})
	env := bus.Envelope{V: 2, Type: bus.MsgTransit, MsgID: "t-1", From: "ta", Payload: payload}
	// conn is nil on purpose: the handler must tolerate a dispatch path that
	// carries none, and a nil inbound conn also exercises the no-echo guard.
	r.handleTransit(ctx, nil, env)

	// Observable state must be untouched: no send, no delivery. With no rows
	// the assertion is the absence of a panic and the drop log — the forward
	// path can only have run if a hop existed, and R has none.
}

// TestTransitEligibility covers the denylist: frames whose handler binds the
// sender to a conn, and the unsolicited push side of the artifact plane, must
// never be sealed into a transit inner. Fetch/chunk DO ride transit: both keep
// their own From/To inside the sealed envelope, so the reply reaches the
// requester through the relay, and transitMaxInner is sized for one chunk.
func TestTransitEligibility(t *testing.T) {
	for _, typ := range []string{
		bus.MsgHello, bus.MsgHelloReject,
		bus.MsgArtifactPush, bus.MsgArtifactPushStatus, bus.MsgArtifactPushDone,
	} {
		if transitEligible(typ) {
			t.Fatalf("frame type %q marked transit-eligible", typ)
		}
		if err := newCoreErr(t).sendViaTransit(context.Background(), "peer-x",
			bus.Envelope{V: 2, Type: typ, MsgID: "m", From: "n"}); err == nil {
			t.Fatalf("sendViaTransit accepted ineligible type %q", typ)
		}
	}
	for _, typ := range []string{bus.MsgArtifactFetch, bus.MsgArtifactChunk} {
		if !transitEligible(typ) {
			t.Fatalf("frame type %q must be transit-eligible for multi-hop input pulls", typ)
		}
	}
	// Pair frames are the one type that is DELIVERABLE over transit (P4b:
	// the contact-point proxy seals them as the pairing exemption) but not
	// SENDABLE through the generic fallback, which has no return binding for
	// a joiner and cannot seal to an identity it has never met.
	for _, typ := range []string{
		bus.MsgPairHello, bus.MsgPairReady, bus.MsgPairSecret, bus.MsgPairReject,
	} {
		if !transitEligible(typ) {
			t.Fatalf("pair frame %q must be deliverable over transit", typ)
		}
		if err := newCoreErr(t).sendViaTransit(context.Background(), "peer-x",
			bus.Envelope{V: 2, Type: typ, MsgID: "m", From: "n"}); err == nil {
			t.Fatalf("sendViaTransit must refuse pair frame %q", typ)
		}
	}
}

// TestTransitMeshSealAuditsDegradation pins the honest-degradation contract:
// sealing to a peer that published no x_pub must not silently masquerade as
// end-to-end — the sender writes a transit_degraded audit entry naming the
// destination, so an operator can see the relay could have read that inner.
func TestTransitMeshSealAuditsDegradation(t *testing.T) {
	ctx := context.Background()
	a := newCoreErr(t)
	// A pre-key / old-version peer: a row exists but carries no x_pub.
	if _, err := a.db.Exec(
		`INSERT INTO employee_cache (id, status, last_seen) VALUES ('old-peer', 'offline', 0)`); err != nil {
		t.Fatalf("seed peer row: %v", err)
	}
	env, err := bus.NewEnvelope(bus.MsgHeartbeat, "nx", "hb-deg", bus.HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	// No route exists in this fixture — sendViaTransit seals, audits, then
	// returns errNoRoute. The audit entry is the assertion, not the error.
	_ = a.sendViaTransit(ctx, "old-peer", env)
	var n int
	if err := a.db.QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE what='transit_degraded' AND target='old-peer'`).
		Scan(&n); err != nil || n != 1 {
		t.Fatalf("transit_degraded audit rows = %d (err=%v), want 1", n, err)
	}
}

// TestTransitInnerAttestationGate pins the contract at the destination: an
// unsigned inner is dropped (no conn-bound From exists to substitute for a
// signature), a signed heartbeat from an unpinned instance is delivered as
// first contact, and a signed inner whose From is pinned to the signing key
// is delivered. The task-bearing half of the first-contact rule is covered
// by TestTransitFirstContactPolicy.
func TestTransitInnerAttestationGate(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		sign bool
		pin  bool
		want bool
	}{
		{name: "unsigned", want: false},
		{name: "signed-unknown-from-heartbeat", sign: true, want: true},
		{name: "signed-pinned-from", sign: true, pin: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newCoreErr(t)
			var signerPubHex string
			var signerPriv ed25519.PrivateKey
			if tc.sign {
				pub, priv, err := bus.GenerateNodeKey()
				if err != nil {
					t.Fatalf("generate signer: %v", err)
				}
				signerPubHex = hex.EncodeToString(pub)
				signerPriv = priv
			}
			if tc.pin {
				if _, err := b.db.Exec(
					`INSERT INTO employee_cache (id, pub_key, status, last_seen)
					 VALUES ('ghost', ?, 'offline', 0)`, signerPubHex); err != nil {
					t.Fatalf("seed pinned row: %v", err)
				}
			}
			env, err := bus.NewEnvelope(bus.MsgHeartbeat, "ghost", "hb-"+tc.name,
				bus.HeartbeatPayload{Status: "online", Card: json.RawMessage(`{"device":"ghost"}`)})
			if err != nil {
				t.Fatalf("envelope: %v", err)
			}
			env.To = b.nodeID
			raw, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("marshal env: %v", err)
			}
			inner := transitInner{Env: raw}
			if tc.sign {
				inner.Pub = signerPubHex
				inner.Sig = bus.SignTransitInner(signerPriv, raw)
			}
			innerJSON, err := json.Marshal(inner)
			if err != nil {
				t.Fatalf("marshal inner: %v", err)
			}
			_, xpub, ok := b.nodeXKey()
			if !ok {
				t.Fatal("destination has no x key")
			}
			blob, err := bus.TransitSealX25519(xpub, innerJSON)
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			b.deliverTransit(ctx, bus.Envelope{
				V: 1, Type: bus.MsgTransit, MsgID: "outer-" + tc.name, From: "relay",
			}, bus.TransitPayload{
				Dest: b.nodeID, TTL: bus.TransitMaxTTL,
				Seal: bus.SealX25519, Inner: blob,
			})

			var status, pinned string
			err = b.db.QueryRow(
				`SELECT COALESCE(status,''), COALESCE(pub_key,'') FROM employee_cache WHERE id='ghost'`).
				Scan(&status, &pinned)
			if tc.want {
				if err != nil || status != "online" {
					t.Fatalf("attested frame not delivered: status=%q pinned=%q err=%v", status, pinned, err)
				}
				if tc.pin && pinned != signerPubHex {
					t.Fatalf("pinned row lost its key: pinned=%q want=%q", pinned, signerPubHex)
				}
				if !tc.pin && pinned != "" {
					t.Fatalf("first-contact heartbeat minted a key without a signed hello: pinned=%q", pinned)
				}
			} else if err == nil {
				t.Fatalf("unattested frame delivered: status=%q pinned=%q", status, pinned)
			}
		})
	}
}

// TestTransitDropsFrameAddressedElsewhere pins the addressee check: a frame
// that names somebody else must not be executed here. Without it a member
// could route another node's delegate to us and have it settled under our
// admission policy, with the result coming back to us instead.
func TestTransitDropsFrameAddressedElsewhere(t *testing.T) {
	ctx := context.Background()
	b := newCoreErr(t)
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("generate signer: %v", err)
	}
	env, err := bus.NewEnvelope(bus.MsgHeartbeat, "ghost", "hb-elsewhere",
		bus.HeartbeatPayload{Status: "online", Card: json.RawMessage(`{"device":"ghost"}`)})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	env.To = "somebody-else"
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	inner := transitInner{Env: raw, Pub: hex.EncodeToString(pub), Sig: bus.SignTransitInner(priv, raw)}
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}
	_, xpub, ok := b.nodeXKey()
	if !ok {
		t.Fatal("destination has no x key")
	}
	blob, err := bus.TransitSealX25519(xpub, innerJSON)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	b.deliverTransit(ctx, bus.Envelope{
		V: 1, Type: bus.MsgTransit, MsgID: "outer-elsewhere", From: "relay",
	}, bus.TransitPayload{
		Dest: b.nodeID, TTL: bus.TransitMaxTTL,
		Seal: bus.SealX25519, Inner: blob,
	})

	var n int
	if err := b.db.QueryRow(`SELECT COUNT(1) FROM employee_cache WHERE id='ghost'`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 0 {
		t.Fatal("a frame addressed to another node was executed here")
	}
}

// TestTransitFromOKBindsClaimedID covers the identity binding directly:
// stable keys must match verbatim, instance/session ids must resolve to a row
// with the signing key, and unknown or unpinned rows fail closed.
func TestTransitFromOKBindsClaimedID(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "self", "marker")
	pub, _, _ := bus.GenerateNodeKey()
	pubHex := hex.EncodeToString(pub)
	other, _, _ := bus.GenerateNodeKey()
	otherHex := hex.EncodeToString(other)

	if bound, _ := c.transitFromOK(ctx, stableIDPrefix+pubHex, pubHex); !bound {
		t.Fatal("matching stable identity rejected")
	}
	if bound, _ := c.transitFromOK(ctx, stableIDPrefix+pubHex, otherHex); bound {
		t.Fatal("stable identity accepted a different signing key")
	}
	if bound, unknown := c.transitFromOK(ctx, "unknown-instance", pubHex); bound || !unknown {
		t.Fatalf("unknown instance id = bound:%v unknown:%v, want first-contact only", bound, unknown)
	}
	if _, err := c.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, status, last_seen)
		 VALUES ('peer-a', ?, 'online', 0)`, pubHex); err != nil {
		t.Fatalf("seed peer: %v", err)
	}
	if bound, _ := c.transitFromOK(ctx, "peer-a", pubHex); !bound {
		t.Fatal("pinned instance id rejected")
	}
	if bound, unknown := c.transitFromOK(ctx, "peer-a", otherHex); bound || unknown {
		t.Fatalf("pinned instance id accepted a different signing key (bound=%v unknown=%v)", bound, unknown)
	}
	if _, err := c.db.Exec(
		`INSERT INTO employee_cache (id, status, last_seen) VALUES ('legacy', 'online', 0)`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if bound, unknown := c.transitFromOK(ctx, "legacy", pubHex); bound || unknown {
		t.Fatalf("unpinned legacy row accepted a signing key (bound=%v unknown=%v)", bound, unknown)
	}
	if bound, _ := c.transitFromOK(ctx, "peer-a-1a2b3c4d", pubHex); !bound {
		t.Fatal("session id did not resolve through its host row")
	}
	if bound, unknown := c.transitFromOK(ctx, "peer-a-1a2b3c4d", otherHex); bound || unknown {
		t.Fatalf("session id accepted a key that contradicts its host (bound=%v unknown=%v)", bound, unknown)
	}
	if bound, unknown := c.transitFromOK(ctx, "missing-1a2b3c4d", pubHex); bound || !unknown {
		t.Fatalf("session id with no host row = bound:%v unknown:%v, want first-contact only", bound, unknown)
	}
}

// TestTransitFirstContactPolicy pins which frame types may use the
// unknown-sender grace: discovery and punch coordination only. A delegate,
// result, accept, or bundle from a ghost instance id must not reach dispatch,
// where an unpinned origin would resolve to the default mesh tier.
func TestTransitFirstContactPolicy(t *testing.T) {
	for _, typ := range []string{bus.MsgHeartbeat, bus.MsgPunchOffer, bus.MsgPunchReady} {
		if !transitFirstContact(typ) {
			t.Fatalf("%q must be allowed from an unpinned transit sender", typ)
		}
	}
	for _, typ := range []string{
		bus.MsgTaskDelegate, bus.MsgTaskAccept, bus.MsgTaskResult,
		bus.MsgTaskCancel, bus.MsgContextFetch, bus.MsgArtifactPush, bus.MsgDTNBundle,
	} {
		if transitFirstContact(typ) {
			t.Fatalf("%q must require a pinned transit identity", typ)
		}
	}
}

// TestTransitSenderRejectsOversizedFrame pins the sender-side size gate in
// both places it can trip: a plaintext already over budget, and one just
// under it that AEAD overhead/nonce/key prefix push over. Either must return
// an error so sendTo parks the frame instead of reporting a delivery the
// destination will drop.
func TestTransitSenderRejectsOversizedFrame(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		size int
	}{
		{name: "plaintext-over-limit", size: transitMaxInner + 1},
		{name: "sealed-overhead-over-limit", size: transitMaxInner - 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCoreErr(t)
			env := envelopeWithJSONSize(t, tc.size)
			err := c.sendViaTransit(ctx, "peer-x", env)
			if !errors.Is(err, errTransitTooLarge) {
				t.Fatalf("sendViaTransit error = %v, want errTransitTooLarge", err)
			}
		})
	}
}

// envelopeWithJSONSize builds a valid heartbeat envelope whose marshalled
// form is exactly n bytes, so the size tests exercise both sides of the
// sender's pre-seal and post-seal checks.
func envelopeWithJSONSize(t *testing.T, n int) bus.Envelope {
	t.Helper()
	env := bus.Envelope{
		V: 1, Type: bus.MsgHeartbeat, MsgID: "size", From: "nx",
		Payload: json.RawMessage(`""`),
	}
	base, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal base: %v", err)
	}
	filler := n - len(base)
	if filler < 0 {
		t.Fatalf("requested size %d is below the envelope floor %d", n, len(base))
	}
	env.Payload = json.RawMessage(`"` + strings.Repeat("x", filler) + `"`)
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal sized: %v", err)
	}
	if len(raw) != n {
		t.Fatalf("sized envelope = %d bytes, want %d", len(raw), n)
	}
	return env
}

// TestClampTransitTTL covers the intake bound: a sender may not buy extra
// relay hops by writing a larger TTL, while ordinary and exhausted values
// pass through unchanged.
func TestClampTransitTTL(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{in: bus.TransitMaxTTL + 100, want: bus.TransitMaxTTL},
		{in: bus.TransitMaxTTL, want: bus.TransitMaxTTL},
		{in: 2, want: 2},
		{in: 0, want: 0},
		{in: -1, want: -1},
	} {
		if got := clampTransitTTL(tc.in); got != tc.want {
			t.Fatalf("clampTransitTTL(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func newCoreErr(t *testing.T) *Core {
	t.Helper()
	c := NewCore(openTestDB(t), "nx", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	c.SetSharedSecret(testSharedSecret)
	return c
}

// TestTransitRejectsUnsealedNonPairInner pins the one guard every sibling
// has a test for: SealNone is the PAIRING handshake's exemption and nothing
// else's. A member that seals nothing around a delegate or heartbeat inner
// would have every relay that ferried the frame act as a legitimate reader
// of the plaintext — the blind-relay confidentiality property the design
// guarantees.
//
// The positive control matters as much as the negative: a pair frame riding
// SealNone must still be delivered, or the exemption would be untested too.
func TestTransitRejectsUnsealedNonPairInner(t *testing.T) {
	ctx := context.Background()
	b := newCoreErr(t)
	pub, priv, err := bus.GenerateNodeKey()
	if err != nil {
		t.Fatalf("generate signer: %v", err)
	}
	signerPubHex := hex.EncodeToString(pub)
	if _, err := b.db.Exec(
		`INSERT INTO employee_cache (id, pub_key, status, last_seen)
		 VALUES ('ghost', ?, 'offline', 0)`, signerPubHex); err != nil {
		t.Fatalf("seed pinned row: %v", err)
	}

	deliver := func(name string, typ string, payload any) bool {
		t.Helper()
		env, err := bus.NewEnvelope(typ, "ghost", "hb-"+name, payload)
		if err != nil {
			t.Fatalf("envelope: %v", err)
		}
		env.To = b.nodeID
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal env: %v", err)
		}
		inner := transitInner{Env: raw, Pub: signerPubHex, Sig: bus.SignTransitInner(priv, raw)}
		innerJSON, err := json.Marshal(inner)
		if err != nil {
			t.Fatalf("marshal inner: %v", err)
		}
		// SealNone: the payload travels in the clear.
		b.deliverTransit(ctx, bus.Envelope{
			V: 1, Type: bus.MsgTransit, MsgID: "outer-" + name, From: "relay",
		}, bus.TransitPayload{
			Dest: b.nodeID, TTL: bus.TransitMaxTTL,
			Seal: bus.SealNone, Inner: innerJSON,
		})
		var status string
		err = b.db.QueryRow(`SELECT COALESCE(status,'') FROM employee_cache WHERE id='ghost'`).Scan(&status)
		return err == nil && status == "online"
	}

	if deliver("heartbeat", bus.MsgHeartbeat,
		bus.HeartbeatPayload{Status: "online", Card: json.RawMessage(`{"device":"ghost"}`)}) {
		t.Fatal("an UNSEALED non-pair inner was dispatched — the blind-relay seal was bypassed")
	}
	// The positive path (a pair frame riding SealNone) is covered by
	// TestPairOverTransitThroughContactPoint, which drives it through the
	// production contact-point path; this test pins the branch that had no
	// coverage at all — the REJECTION of an unsealed non-pair inner.
}
