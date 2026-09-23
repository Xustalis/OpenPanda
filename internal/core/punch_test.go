package core

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// waitUDPRoute polls until core c holds a confirmed datagram endpoint for id.
func waitUDPRoute(t *testing.T, c *Core, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for c.UDPRoute(id) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("udp route to %s never established", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestPunchHandshakeDirect runs the pinhole handshake between two cores that
// share only UDP listeners — no WS conn, no relay. The spray itself carries
// the signed opener; the first frame either direction binds the route and
// the ack confirms it. This is the two-NAT case reduced to loopback.
func TestPunchHandshakeDirect(t *testing.T) {
	ctx := context.Background()
	a := newCoreWithNative(t, "punch-a", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	b := newCoreWithNative(t, "punch-b", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	if err := a.ListenUDP(ctx, "127.0.0.1:0", nil); err != nil {
		t.Fatal(err)
	}
	if err := b.ListenUDP(ctx, "127.0.0.1:0", nil); err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown(ctx)
	defer b.Shutdown(ctx)

	// Each side knows the other's endpoint the way a mutual hello exchange
	// would have taught it: listener port + the observed IP a conn reported.
	// The observed-IP record is what lets B trust A's spray — an unsigned
	// From with no prior authenticated contact must not bind a route.
	bAddr := "127.0.0.1:" + itoa(b.UDPPort())
	aAddr := "127.0.0.1:" + itoa(a.UDPPort())
	a.learnPeerUDP("punch-b", b.UDPPort(), []string{bAddr}, "127.0.0.1")
	b.learnPeerUDP("punch-a", a.UDPPort(), []string{aAddr}, "127.0.0.1")
	_ = a.PunchPeer(ctx, "punch-b") // routeTo fails: no conn, no route — expected

	waitUDPRoute(t, b, "punch-a") // B learned A from the punch frames
	waitUDPRoute(t, a, "punch-b") // A confirmed via the ack

	// The bound route must actually carry envelopes: sendTo falls back to
	// the datagram plane when no conn exists.
	env, err := bus.NewEnvelope(bus.MsgHeartbeat, "punch-a", "m1", bus.HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatal(err)
	}
	env.To = "punch-b"
	if err := a.sendTo("punch-b", env); err != nil {
		t.Fatalf("sendTo over udp route: %v", err)
	}
}

// TestPunchRelayedOffer covers the mesh-coordination path: A and B hold no
// direct transport, but both are WS-connected to relay R. A's offer is
// forwarded through R's link-state next hop; B's spray then punches back to
// A's advertised candidates.
func TestPunchRelayedOffer(t *testing.T) {
	ctx := context.Background()
	a := newCoreWithNative(t, "pn-a", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	r := newCoreWithNative(t, "pn-r", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	b := newCoreWithNative(t, "pn-b", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	defer a.Shutdown(ctx)
	defer r.Shutdown(ctx)
	defer b.Shutdown(ctx)

	// R listens WS; A and B dial it so R holds live conns to both.
	rAddr := "127.0.0.1:18131"
	rd := make(chan error, 1)
	go func() { rd <- r.Listen(ctx, rAddr) }()
	for _, c := range []*Core{a, r, b} {
		if err := c.Register(ctx); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := a.DialPeer(ctx, rAddr); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for {
		if err := b.DialPeer(ctx, rAddr); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitPeer(t, a, "pn-r")
	waitPeer(t, b, "pn-r")
	waitPeer(t, r, "pn-a")
	waitPeer(t, r, "pn-b")

	// Datagram planes on A and B only — the relay needs none. Wildcard binds:
	// the offer advertises non-loopback interface candidates (loopback is
	// correctly excluded), and only a socket bound to all interfaces can
	// receive a punch aimed at the LAN address.
	if err := a.ListenUDP(ctx, ":0", nil); err != nil {
		t.Fatal(err)
	}
	if err := b.ListenUDP(ctx, ":0", nil); err != nil {
		t.Fatal(err)
	}

	// The link-state graph A and B consult: R advertises both as neighbors.
	// In production this lands via heartbeat gossip; here it is injected
	// directly so the test does not depend on heartbeat timing.
	for _, db := range []*sql.DB{a.db, b.db} {
		if err := ledger.UpsertRemote(db, "pn-r", ledger.CapabilitySummary{
			Device:    "pn-r",
			Neighbors: []string{"pn-a", "pn-b"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := a.PunchPeer(ctx, "pn-b"); err != nil {
		t.Fatalf("punch offer: %v", err)
	}

	// The offer travels A→R→B as an envelope; B then sprays at A's
	// advertised candidates (its loopback UDP port among them) and the ack
	// closes the loop.
	waitUDPRoute(t, b, "pn-a")
	waitUDPRoute(t, a, "pn-b")
}

// TestPunchSpoofedFromDropped covers the member-level hole the AEAD layer
// cannot see: a frame whose HMAC is valid under the shared secret but whose
// From names a node we never met over an authenticated channel — no session,
// no candidates, no observed IP — must not bind a route. Membership is not
// identity.
func TestPunchSpoofedFromDropped(t *testing.T) {
	ctx := context.Background()
	a := newCoreWithNative(t, "pn-vb", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	if err := a.ListenUDP(ctx, "127.0.0.1:0", nil); err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown(ctx)

	// Mallory is a real member — the signature verifies — but "pn-victim"
	// has never hello'd us, offered a punch, or opened a session.
	mallory, err := bus.ListenUDP("127.0.0.1:0", testSharedSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mallory.Close()
	f := bus.PunchFrame{Nonce: "n", From: "pn-victim", TS: time.Now().Unix()}
	f.Sig = bus.PunchSig(testSharedSecret, f.Nonce, f.From, f.TS)
	if err := mallory.SendPunch(f, a.udp.LocalAddr(), false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-time.After(300 * time.Millisecond):
	}
	if a.UDPRoute("pn-victim") != nil {
		t.Fatal("unverifiable From bound a route")
	}
}

// TestPunchBadSignature verifies a forged punch frame — no valid HMAC —
// cannot open a route.
func TestPunchBadSignature(t *testing.T) {
	ctx := context.Background()
	a := newCoreWithNative(t, "pn-va", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	if err := a.ListenUDP(ctx, "127.0.0.1:0", nil); err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown(ctx)

	// A socket keyed under a DIFFERENT secret sends a punch frame whose Sig
	// is valid for ITS key but wrong under ours — VerifyPunch uses the
	// receiver's secret, so this is a forgery.
	forger, err := bus.ListenUDP("127.0.0.1:0", "forger-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer forger.Close()
	f := bus.PunchFrame{Nonce: "n", From: "mallory", TS: time.Now().Unix()}
	f.Sig = bus.PunchSig("forger-secret", f.Nonce, f.From, f.TS)
	aAddr := a.udp.LocalAddr()
	if err := forger.SendPunch(f, aAddr, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-time.After(300 * time.Millisecond):
	}
	if a.UDPRoute("mallory") != nil {
		t.Fatal("forged punch bound a route")
	}
}
