// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestBeaconCodecRoundTrip: a marshaled beacon parses back to the same
// fields — the wire contract sender and listener share.
func TestBeaconCodecRoundTrip(t *testing.T) {
	pub, _, _ := bus.GenerateNodeKey()
	raw := marshalBeacon("node-a", ":7836", pub, nil)
	if raw == nil {
		t.Fatal("marshalBeacon returned nil")
	}
	b, ok := parseBeacon(raw)
	if !ok {
		t.Fatal("a freshly marshaled beacon failed validation")
	}
	if b.ID != "node-a" || b.Addr != ":7836" || b.Pub != hex.EncodeToString(pub) {
		t.Fatalf("round-trip mismatch: %+v", b)
	}
	if b.Ver == "" {
		t.Fatal("beacon carries no version")
	}
}

// TestBeaconRejectsGarbage: the listener's entire trust boundary is this
// parser — every malformed shape must drop, not record.
func TestBeaconRejectsGarbage(t *testing.T) {
	cases := map[string][]byte{
		"empty":        {},
		"not-json":     []byte("hello lan"),
		"wrong-magic":  []byte(`{"panda":"not-us","id":"n","addr":":7836"}`),
		"no-id":        []byte(`{"panda":"panda-beacon/1","addr":":7836"}`),
		"bad-port":     []byte(`{"panda":"panda-beacon/1","id":"n","addr":":99999"}`),
		"text-port":    []byte(`{"panda":"panda-beacon/1","id":"n","addr":":abc"}`),
		"no-addr":      []byte(`{"panda":"panda-beacon/1","id":"n"}`),
		"bad-pub":      []byte(`{"panda":"panda-beacon/1","id":"n","addr":":7836","pub":"zzzz"}`),
		"oversize":     make([]byte, beaconMaxBytes+1),
		"huge-id":      []byte(`{"panda":"panda-beacon/1","id":"` + strings.Repeat("x", 200) + `","addr":":7836"}`),
		"old-magic-v0": []byte(`{"panda":"panda-beacon/0","id":"n","addr":":7836"}`),
	}
	for name, raw := range cases {
		if _, ok := parseBeacon(raw); ok {
			t.Fatalf("%s: garbage accepted", name)
		}
	}
}

// TestResolveBeaconAddr: the receiver substitutes the datagram's source IP
// for any host the sender cannot know — unspecified, empty or loopback — but
// trusts an explicit routable host or DNS name.
func TestResolveBeaconAddr(t *testing.T) {
	src := net.ParseIP("192.168.1.50")
	for addr, want := range map[string]string{
		":7836":            "192.168.1.50:7836", // wildcard host → src
		"0.0.0.0:7836":     "192.168.1.50:7836",
		"127.0.0.1:7836":   "192.168.1.50:7836", // loopback means "me" → src
		"192.168.1.9:7836": "192.168.1.9:7836",  // explicit IP: keep
		"pi.lan:7836":      "pi.lan:7836",       // DNS name: keep
	} {
		b := Beacon{Addr: addr}
		if got := resolveBeaconAddr(b, src); got != want {
			t.Fatalf("resolve %q = %q, want %q", addr, got, want)
		}
	}
}

// TestOnBeaconRecordsPending: a valid beacon lands in pending_nodes with the
// source-IP-resolved address; self and already-joined ids are skipped; stale
// rows sweep.
func TestOnBeaconRecordsPending(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "self", "127.0.0.1:0")

	pub, _, _ := bus.GenerateNodeKey()
	src := &net.UDPAddr{IP: net.ParseIP("192.168.1.60"), Port: 50000}
	raw := marshalBeacon("stranger", ":7836", pub, nil)
	c.onBeacon(ctx, raw, src)

	pending, err := ledger.ListPending(c.db, time.Minute)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after beacon: %v len=%d", err, len(pending))
	}
	p := pending[0]
	if p.ID != "stranger" || p.Addr != "192.168.1.60:7836" {
		t.Fatalf("pending row %+v", p)
	}
	if p.PubKey != hex.EncodeToString(pub) {
		t.Fatal("pubkey not recorded")
	}

	// Self beacon is ignored.
	c.onBeacon(ctx, marshalBeacon("self", ":7836", pub, nil), src)
	// A malformed datagram adds nothing.
	c.onBeacon(ctx, []byte(`{"panda":"panda-beacon/1","id":"bogus"}`), src)
	// A node already in the directory is fleet, not pending.
	if _, err := c.db.Exec(`INSERT INTO employee_cache (id, status, last_seen) VALUES ('stranger','online',?)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	c.onBeacon(ctx, raw, src)
	pending, _ = ledger.ListPending(c.db, time.Minute)
	if len(pending) != 1 {
		t.Fatalf("pending len %d — self/garbage/duplicate leaked in", len(pending))
	}

	// Stale rows sweep on read.
	if _, err := c.db.Exec(`UPDATE pending_nodes SET last_seen=?`, time.Now().Unix()-3600); err != nil {
		t.Fatal(err)
	}
	if pending, _ = ledger.ListPending(c.db, time.Minute); len(pending) != 0 {
		t.Fatal("stale pending row survived the sweep")
	}
}

// TestSignedBeaconVerified: a beacon carrying a genuine Ed25519 signature
// lands with verified set — the fingerprint provably belongs to the
// broadcaster. A beacon signed by a key it does NOT advertise (the
// fingerprint-spoof shape: victim's pub, attacker's key) is dropped
// outright; sig-without-pub is malformed, not unsigned.
func TestSignedBeaconVerified(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "self", "127.0.0.1:0")
	src := &net.UDPAddr{IP: net.ParseIP("192.168.1.60"), Port: 50000}

	pub, priv, _ := bus.GenerateNodeKey()
	c.onBeacon(ctx, marshalBeacon("signed-node", ":7836", pub, priv), src)

	pending, err := ledger.ListPending(c.db, time.Minute)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after signed beacon: %v len=%d", err, len(pending))
	}
	if !pending[0].Verified {
		t.Fatal("signed beacon was not recorded verified")
	}

	// The spoof: victim's id + pubkey (the fingerprint an operator would
	// trust), signed by an unrelated key — i.e. addr lies about who owns the
	// fingerprint. Verify fails and nothing is recorded.
	_, evilPriv, _ := bus.GenerateNodeKey()
	forged := Beacon{
		Panda: beaconMagic, ID: "victim", Addr: ":7836", Ver: "x",
		Pub: hex.EncodeToString(pub), TS: time.Now().Unix(),
	}
	forged.Sig = bus.SignBeacon(evilPriv, forged.ID, forged.Addr, forged.Ver, forged.Pub, forged.TS)
	rawForged, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	c.onBeacon(ctx, rawForged, src)

	// An honestly unsigned beacon still lands — flagged, not refused.
	c.onBeacon(ctx, marshalBeacon("unsigned-node", ":7836", nil, nil), src)

	pending, _ = ledger.ListPending(c.db, time.Minute)
	var sawVictim, sawUnsigned bool
	for _, p := range pending {
		if p.ID == "victim" {
			sawVictim = true
		}
		if p.ID == "unsigned-node" {
			sawUnsigned = p.Verified == false
		}
	}
	if sawVictim {
		t.Fatal("forged-signature beacon was recorded")
	}
	if !sawUnsigned {
		t.Fatal("unsigned beacon missing or wrongly marked verified")
	}

	// A sig field with no advertised key is malformed, not an unsigned node.
	sigNoKey := `{"panda":"panda-beacon/1","id":"n","addr":":7836","sig":"` + strings.Repeat("aa", 64) + `"}`
	if _, ok := parseBeacon([]byte(sigNoKey)); ok {
		t.Fatal("sig-without-key beacon parsed")
	}
}

// TestPendingCapPrefersVerified: at cap, the eviction order is unverified
// first — a forged-beacon flood (everything it emits is unsigned, since a
// forged sig is dropped upstream) displaces noise before it displaces a
// fingerprint a signature actually proved.
func TestPendingCapPrefersVerified(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "self", "127.0.0.1:0")
	src := &net.UDPAddr{IP: net.ParseIP("192.168.1.60"), Port: 50000}
	for i := 0; i < pendingCap; i++ {
		c.onBeacon(ctx, marshalBeacon("junk-"+strconv.Itoa(i), ":7836", nil, nil), src)
	}
	pub, priv, _ := bus.GenerateNodeKey()
	c.onBeacon(ctx, marshalBeacon("real-node", ":7836", pub, priv), src)

	pending, err := ledger.ListPending(c.db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) > pendingCap {
		t.Fatalf("pending grew to %d past cap %d", len(pending), pendingCap)
	}
	var found bool
	for _, p := range pending {
		if p.ID == "real-node" {
			found = p.Verified
		}
	}
	if !found {
		t.Fatal("verified beacon lost to an unsigned flood")
	}
}

// TestPendingCap: a LAN flood of forged beacons must degrade to bounded
// noise, not unbounded growth — the table never exceeds pendingCap and the
// stalest row is the one evicted.
func TestPendingCap(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "self", "127.0.0.1:0")
	src := &net.UDPAddr{IP: net.ParseIP("192.168.1.60"), Port: 50000}
	for i := 0; i < pendingCap+8; i++ {
		c.onBeacon(ctx, marshalBeacon("flood-"+strconv.Itoa(i), ":7836", nil, nil), src)
	}
	pending, err := ledger.ListPending(c.db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) > pendingCap {
		t.Fatalf("pending grew to %d past cap %d", len(pending), pendingCap)
	}
	// The earliest floods were evicted; the latest are present.
	ids := make(map[string]bool, len(pending))
	for _, p := range pending {
		ids[p.ID] = true
	}
	if ids["flood-0"] {
		t.Fatal("oldest flood row was not evicted first")
	}
	if !ids["flood-"+strconv.Itoa(pendingCap+7)] {
		t.Fatal("newest beacon missing after cap eviction")
	}
}

// TestDiscoverySocketEndToEnd: the real listener goroutine — a datagram
// arriving on the discovery socket becomes a pending row with the sender's
// source IP filled in. Unicast stands in for broadcast (the listener cannot
// tell them apart, and loopback is all a test can rely on).
func TestDiscoverySocketEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := newCore(t, "self", "127.0.0.1:0")
	c.sharedSecret = "x" // announcer eligibility; loopback advertise suppresses the send anyway

	// Bind an ephemeral discovery port.
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()

	go c.RunDiscovery(ctx, "127.0.0.1:"+strconv.Itoa(port), "127.0.0.1:7836")
	time.Sleep(100 * time.Millisecond) // let the listener bind

	sender, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sender.Close()
	pub, _, _ := bus.GenerateNodeKey()
	if _, err := sender.Write(marshalBeacon("lan-node", ":9999", pub, nil)); err != nil {
		t.Fatalf("send: %v", err)
	}

	var pending []ledger.PendingNode
	for i := 0; i < 50; i++ {
		pending, _ = ledger.ListPending(c.db, time.Minute)
		if len(pending) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatalf("no pending row after live datagram (len=%d)", len(pending))
	}
	if pending[0].ID != "lan-node" || pending[0].Addr != "127.0.0.1:9999" {
		t.Fatalf("pending row %+v — want lan-node at the beacon's port under the source IP", pending[0])
	}
}

// TestDiscoveryBadBindAddr: a malformed discovery_addr must degrade to a
// warning and a clean return — a non-numeric port must not silently bind an
// ephemeral socket that announces to port 0.
func TestDiscoveryBadBindAddr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newCore(t, "self", "127.0.0.1:0")
	done := make(chan struct{})
	go func() { c.RunDiscovery(ctx, ":not-a-port", ":9999"); close(done) }()
	// The malformed port still goes through a resolver lookup, and on loaded
	// CI runners that syscall alone has taken ~5s. The assertion guards a
	// real hang, not resolver speed, so the budget stays generous.
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunDiscovery hung on a malformed bind addr")
	}
}

// TestAdvertiseIsLoopback: the announcer must not broadcast when the node
// cannot be reached — a loopback listen_addr advertises a door nobody else
// can knock on.
func TestAdvertiseIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		":7836":            false,
		"0.0.0.0:7836":     false,
		"192.168.1.5:7836": false,
		"pi.lan:7836":      false,
		"127.0.0.1:7836":   true,
		"localhost:7836":   true,
		"[::1]:7836":       true,
	} {
		if got := advertiseIsLoopback(addr); got != want {
			t.Fatalf("advertiseIsLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
