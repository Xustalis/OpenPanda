// SPDX-License-Identifier: AGPL-3.0-or-later

package bus

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"net"
	"testing"
	"time"
)

// fakeKeys is a UDPKeySource backed by a fixed table: a peer listed in pair has
// an identity key, everyone else gets the mesh-key fallback. atAddr maps a
// source IP to the peer whose confirmed endpoint lives there.
type fakeKeys struct {
	pair   map[string]cipher.AEAD
	atAddr map[string]string
}

func (f *fakeKeys) PairKey(peer string) (cipher.AEAD, bool) {
	k, ok := f.pair[peer]
	return k, ok
}

func (f *fakeKeys) PeerAtAddr(src *net.UDPAddr) (string, bool) {
	if src == nil {
		return "", false
	}
	p, ok := f.atAddr[src.IP.String()]
	return p, ok
}

// testAEAD builds a distinct GCM per tag so two directions of one test cannot
// accidentally share a key.
func testAEAD(t *testing.T, tag string) cipher.AEAD {
	t.Helper()
	key := sha256.Sum256([]byte("udp-pair-test/" + tag))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return aead
}

func listenTestUDP(t *testing.T, secret string) *UDPConn {
	t.Helper()
	c, err := ListenUDP("127.0.0.1:0", secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestUDPPairKeyRoundTrip is the property the pair keys exist for: a datagram
// sealed to the pair opens under the far side's derivation of the same key.
func TestUDPPairKeyRoundTrip(t *testing.T) {
	a := listenTestUDP(t, "mesh")
	b := listenTestUDP(t, "mesh")
	shared := testAEAD(t, "a-b")
	a.SetKeySource(&fakeKeys{pair: map[string]cipher.AEAD{"node-b": shared}}, "node-a")
	b.SetKeySource(&fakeKeys{
		pair:   map[string]cipher.AEAD{"node-a": shared},
		atAddr: map[string]string{"127.0.0.1": "node-a"},
	}, "node-b")

	got := make(chan Envelope, 1)
	b.OnEnvelope = func(env Envelope, src *net.UDPAddr) { got <- env }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.ReadLoop(ctx)

	env, err := NewEnvelope(MsgHeartbeat, "node-a", "m-pair", HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatal(err)
	}
	env.To = "node-b"
	if err := a.SendEnvelope(env, b.LocalAddr(), "node-b"); err != nil {
		t.Fatal(err)
	}
	select {
	case gotEnv := <-got:
		if gotEnv.From != "node-a" || gotEnv.MsgID != "m-pair" {
			t.Fatalf("envelope mismatch: %+v", gotEnv)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pair-sealed envelope not received")
	}
}

// TestUDPMeshKeyForgeryRejected is the regression this change exists for. The
// attacker knows the mesh secret — it is a member — and seals a frame claiming
// to be node-a. Two members behind one NAT share a source IP, so the address
// check cannot tell them apart; the pair key is what settles it. node-a
// publishes an identity key, so a mesh-key frame from its address is dropped.
func TestUDPMeshKeyForgeryRejected(t *testing.T) {
	attacker := listenTestUDP(t, "mesh")
	victim := listenTestUDP(t, "mesh")
	victim.SetKeySource(&fakeKeys{
		pair:   map[string]cipher.AEAD{"node-a": testAEAD(t, "a-b")},
		atAddr: map[string]string{"127.0.0.1": "node-a"},
	}, "node-b")

	got := make(chan Envelope, 1)
	victim.OnEnvelope = func(env Envelope, src *net.UDPAddr) { got <- env }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go victim.ReadLoop(ctx)

	forged, err := NewEnvelope(MsgHeartbeat, "node-a", "m-forged", HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatal(err)
	}
	forged.To = "node-b"
	if err := attacker.SendEnvelope(forged, victim.LocalAddr(), ""); err != nil {
		t.Fatal(err)
	}
	select {
	case env := <-got:
		t.Fatalf("mesh-key frame claiming node-a was dispatched: %+v", env)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestUDPMeshKeyHonouredForLegacyPeer is the compatibility half: a peer that
// publishes no identity key still reaches us under the mesh key, which is the
// best either side can do until it upgrades.
func TestUDPMeshKeyHonouredForLegacyPeer(t *testing.T) {
	old := listenTestUDP(t, "mesh")
	ours := listenTestUDP(t, "mesh")
	ours.SetKeySource(&fakeKeys{
		pair:   map[string]cipher.AEAD{"node-b": testAEAD(t, "b-c")},
		atAddr: map[string]string{"127.0.0.1": "node-old"},
	}, "node-b")

	got := make(chan Envelope, 1)
	ours.OnEnvelope = func(env Envelope, src *net.UDPAddr) { got <- env }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ours.ReadLoop(ctx)

	env, err := NewEnvelope(MsgHeartbeat, "node-old", "m-legacy", HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatal(err)
	}
	env.To = "node-b"
	if err := old.SendEnvelope(env, ours.LocalAddr(), "node-b"); err != nil {
		t.Fatal(err)
	}
	select {
	case gotEnv := <-got:
		if gotEnv.From != "node-old" {
			t.Fatalf("envelope From = %q, want node-old", gotEnv.From)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("legacy mesh-key frame from a peer with no identity key was dropped")
	}
}

// TestUDPPairAEADBindsThePair proves the key itself carries the identity: the
// two ends derive the same bytes from opposite sides, order-of-ids does not
// matter, and a third node's derivation is different — which is what stops a
// member from sealing a frame that opens as someone else.
func TestUDPPairAEADBindsThePair(t *testing.T) {
	ka, err := GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	kb, err := GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	kc, err := GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	ab, err := UDPPairAEAD(ka, kb.PublicKey(), "node-a", "node-b")
	if err != nil {
		t.Fatal(err)
	}
	ba, err := UDPPairAEAD(kb, ka.PublicKey(), "node-b", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, udpNonceLen)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	sealed := ab.Seal(nil, nonce, []byte("datagram"), []byte("hdr"))
	if _, err := ba.Open(nil, nonce, sealed, []byte("hdr")); err != nil {
		t.Fatalf("the two ends did not derive the same key: %v", err)
	}

	// A member that is neither party derives something else, so its forged
	// frame cannot open under the pair key.
	cb, err := UDPPairAEAD(kc, kb.PublicKey(), "node-c", "node-b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cb.Open(nil, nonce, sealed, []byte("hdr")); err == nil {
		t.Fatal("a third node opened a frame sealed for a pair it is not part of")
	}
	// Binding is on the identity pair, not on one key: c's derivation differs
	// from a's even though both target b.
	other, err := UDPPairAEAD(ka, kb.PublicKey(), "node-a", "node-c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(nil, nonce, sealed, []byte("hdr")); err == nil {
		t.Fatal("the same keypair with a different peer id opened the frame")
	}
}

// TestUDPSelectorMismatchRejected covers the second half of the same rule: a
// frame that opens under the key for the peer it names, but whose body claims
// a different From, never dispatches. The identity the body asserts was not
// the identity the key proved.
func TestUDPSelectorMismatchRejected(t *testing.T) {
	sender := listenTestUDP(t, "mesh")
	receiver := listenTestUDP(t, "mesh")
	shared := testAEAD(t, "a-b")
	// The receiver keys node-a; the sender seals with that key but writes
	// node-c into the envelope.
	sender.SetKeySource(&fakeKeys{pair: map[string]cipher.AEAD{"node-b": shared}}, "node-a")
	receiver.SetKeySource(&fakeKeys{
		pair:   map[string]cipher.AEAD{"node-a": shared},
		atAddr: map[string]string{"127.0.0.1": "node-a"},
	}, "node-b")

	got := make(chan Envelope, 1)
	receiver.OnEnvelope = func(env Envelope, src *net.UDPAddr) { got <- env }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go receiver.ReadLoop(ctx)

	env, err := NewEnvelope(MsgHeartbeat, "node-c", "m-mismatch", HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatal(err)
	}
	env.To = "node-b"
	if err := sender.SendEnvelope(env, receiver.LocalAddr(), "node-b"); err != nil {
		t.Fatal(err)
	}
	select {
	case gotEnv := <-got:
		t.Fatalf("envelope with a mismatched From was dispatched: %+v", gotEnv)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestUDPPairSealFollowsTheRecipient covers the relay case: an envelope whose
// destination is node-c is handed to node-b, the next hop. The frame has to be
// sealed to node-b — the node that opens it — because node-b holds no key for
// node-c. Sealing to the envelope's destination instead made the sender fall
// back to the mesh key, which the hop then refused as an impersonation from
// node-a's address, so a punch-only relay silently carried nothing.
func TestUDPPairSealFollowsTheRecipient(t *testing.T) {
	sender := listenTestUDP(t, "mesh")
	hop := listenTestUDP(t, "mesh")
	shared := testAEAD(t, "a-b")
	sender.SetKeySource(&fakeKeys{pair: map[string]cipher.AEAD{"node-b": shared}}, "node-a")
	hop.SetKeySource(&fakeKeys{
		pair:   map[string]cipher.AEAD{"node-a": shared},
		atAddr: map[string]string{"127.0.0.1": "node-a"},
	}, "node-b")

	got := make(chan Envelope, 1)
	hop.OnEnvelope = func(env Envelope, src *net.UDPAddr) { got <- env }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hop.ReadLoop(ctx)

	env, err := NewEnvelope(MsgHeartbeat, "node-a", "m-relay", HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatal(err)
	}
	env.To = "node-c"
	if err := sender.SendEnvelope(env, hop.LocalAddr(), "node-b"); err != nil {
		t.Fatal(err)
	}
	select {
	case gotEnv := <-got:
		if gotEnv.To != "node-c" {
			t.Fatalf("envelope destination = %q, want node-c", gotEnv.To)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pair-sealed frame for a relay hop was dropped")
	}
}
