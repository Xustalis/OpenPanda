// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/gorilla/websocket"
)

// TestSessionEncryptionNegotiated runs the real Core handshake end to end:
// two cores that share a secret must come up with the session cipher armed
// on BOTH directions of the surviving conn — hello on the wire plaintext,
// every frame after it sealed.
func TestSessionEncryptionNegotiated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a := newCore(t, "enc-a", "127.0.0.1:17990")
	b := newCore(t, "enc-b", "127.0.0.1:17991")
	if err := a.Register(ctx); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := b.Register(ctx); err != nil {
		t.Fatalf("register b: %v", err)
	}
	go func() { _ = a.Listen(ctx, "127.0.0.1:17990") }()
	go func() { _ = b.Listen(ctx, "127.0.0.1:17991") }()
	time.Sleep(150 * time.Millisecond)

	if err := a.DialPeer(ctx, "127.0.0.1:17991"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	waitPeer(t, a, "enc-b")
	waitPeer(t, b, "enc-a")

	if conn := a.connFor("enc-b"); conn == nil || !conn.Encrypted() {
		t.Fatal("dialer conn is not encrypted after hello")
	}
	if conn := b.connFor("enc-a"); conn == nil || !conn.Encrypted() {
		t.Fatal("listener conn is not encrypted after hello")
	}

	// A sealed frame must actually flow: send a heartbeat on the armed conn
	// and confirm it reaches the far read loop (a read error would drop the
	// peer, which the still-registered assert below would catch).
	env, err := bus.NewEnvelope(bus.MsgHeartbeat, "enc-a", "hb-1",
		bus.HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if err := a.sendTo("enc-b", env); err != nil {
		t.Fatalf("send on encrypted conn: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if a.connFor("enc-b") == nil || b.connFor("enc-a") == nil {
		t.Fatal("peer registration lost after encrypted send")
	}
}

// TestLegacyPeerFallsBackPlaintext simulates a pre-sessaead peer: a raw
// client that presents a validly-signed hello WITHOUT the capability. The
// link must still register (capability negotiation, never a version parse)
// and stay unencrypted — loopback satisfies the post-handshake cleartext
// gate, exactly the rule the old pre-dial check applied.
func TestLegacyPeerFallsBackPlaintext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := newCore(t, "enc-host", "127.0.0.1:17994")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	go func() { _ = c.Listen(ctx, "127.0.0.1:17994") }()
	time.Sleep(150 * time.Millisecond)

	ws, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:17994/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	ts := time.Now().Unix()
	nonce := "legacy-nonce-1"
	hello := bus.HelloPayload{
		NodeID: "legacy-peer",
		Ver:    "old",
		Ts:     ts,
		Nonce:  nonce,
		Sig:    bus.HelloSigN(testSharedSecret, "legacy-peer", ts, nonce),
		// No Caps: this peer predates both binary-data and sessaead.
	}
	env, err := bus.NewEnvelope(bus.MsgHello, "legacy-peer", "h-1", hello)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if err := ws.WriteJSON(env); err != nil {
		t.Fatalf("hello: %v", err)
	}

	waitPeer(t, c, "legacy-peer")
	conn := c.connFor("legacy-peer")
	if conn == nil {
		t.Fatal("legacy peer not registered")
	}
	if conn.Encrypted() {
		t.Fatal("peer without sessaead must stay on the plaintext path")
	}
}

// TestCleartextPolicyRefusesLAN covers the post-handshake gate on the
// outbound side: a non-encrypted conn to a non-safe address is refused —
// the enforcement the pre-dial check used to provide, now applied once the
// peer's capabilities are actually known.
func TestCleartextPolicyRefusesLAN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a := newCore(t, "pol-a", "127.0.0.1:17995")
	b := newCore(t, "pol-b", "127.0.0.1:17996")
	if err := a.Register(ctx); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := b.Register(ctx); err != nil {
		t.Fatalf("register b: %v", err)
	}
	go func() { _ = b.Listen(ctx, "127.0.0.1:17996") }()
	time.Sleep(150 * time.Millisecond)

	if err := a.DialPeer(ctx, "127.0.0.1:17996"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	waitPeer(t, a, "pol-b")

	conn := a.connFor("pol-b")
	if conn == nil {
		t.Fatal("peer not registered")
	}
	// Rewriting the recorded dial address exercises the same check the
	// post-hello path runs — a LAN target without encryption must refuse.
	conn.SetDialAddr("ws://192.168.50.7:7836")
	if err := a.enforceLinkPolicy(conn, "pol-b"); err == nil {
		t.Fatal("cleartext policy allowed a non-safe LAN address")
	}
	conn.SetDialAddr("ws://127.0.0.1:17996")
	if err := a.enforceLinkPolicy(conn, "pol-b"); err != nil {
		t.Fatalf("loopback dial addr refused: %v", err)
	}
}
