// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/hex"
	"net"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

func udpTestCore(t *testing.T, id string) *Core {
	t.Helper()
	c := NewCore(openTestDB(t), id, ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	return c
}

// recordIdentity simulates what a signed hello does: it lands the peer's
// Ed25519 and X25519 keys in the directory.
func recordIdentity(t *testing.T, from, of *Core, xPubHex string) {
	t.Helper()
	pub, _, ok := of.nodeKeyPair()
	if !ok {
		t.Fatal("peer has no ed25519 key")
	}
	if xPubHex == "" {
		_, x, ok := of.nodeXKey()
		if !ok {
			t.Fatal("peer has no x25519 key")
		}
		xPubHex = hex.EncodeToString(x.Bytes())
	}
	from.recordPeerPubKey(context.Background(), of.nodeID, hex.EncodeToString(pub), xPubHex)
}

// TestUDPKeysDeriveTheSamePairKey is the core-level half of the pair-seal
// property: two nodes that have exchanged identities arrive at the same AEAD,
// so a datagram sealed by one opens under the other.
func TestUDPKeysDeriveTheSamePairKey(t *testing.T) {
	a := udpTestCore(t, "node-a")
	b := udpTestCore(t, "node-b")
	recordIdentity(t, a, b, "")
	recordIdentity(t, b, a, "")

	ka, kb := newUDPKeys(a), newUDPKeys(b)
	aeadA, ok := ka.PairKey("node-b")
	if !ok {
		t.Fatal("node-a derived no key for node-b")
	}
	aeadB, ok := kb.PairKey("node-a")
	if !ok {
		t.Fatal("node-b derived no key for node-a")
	}
	nonce := make([]byte, 12)
	sealed := aeadA.Seal(nil, nonce, []byte("datagram"), []byte("hdr"))
	if _, err := aeadB.Open(nil, nonce, sealed, []byte("hdr")); err != nil {
		t.Fatalf("the two nodes did not derive the same key: %v", err)
	}
	if _, ok := ka.PairKey("node-unknown"); ok {
		t.Fatal("derived a key for a peer that published nothing")
	}
}

// TestUDPKeysInvalidatedByKeyChange: a cached pair key must not outlive the
// identity it was derived from. A peer that first appears without an X25519
// key leaves a cached miss (mesh-key fallback); the hello that finally carries
// one has to make the pair key appear without a restart.
func TestUDPKeysInvalidatedByKeyChange(t *testing.T) {
	a := udpTestCore(t, "node-a")
	b := udpTestCore(t, "node-b")
	recordIdentity(t, a, b, "00") // malformed x_pub → stored as absent
	k := newUDPKeys(a)
	// Attach it the way ListenUDP does: recordPeerPubKey is what clears the
	// cache when a key changes, and it can only reach an installed cache.
	a.udpMu.Lock()
	a.udpKeys = k
	a.udpMu.Unlock()
	if _, ok := k.PairKey("node-b"); ok {
		t.Fatal("a peer with no usable x_pub produced a pair key")
	}
	recordIdentity(t, a, b, "") // the real key arrives
	if _, ok := k.PairKey("node-b"); !ok {
		t.Fatal("the pair key did not appear after the peer published its x_pub")
	}
}

// TestUDPKeysPeerAtAddr pins the reverse lookup the legacy-frame rule uses.
func TestUDPKeysPeerAtAddr(t *testing.T) {
	c := udpTestCore(t, "node-a")
	k := newUDPKeys(c)
	c.udpMu.Lock()
	c.udpRoutes = map[string]*net.UDPAddr{
		"node-b": {IP: net.ParseIP("203.0.113.7"), Port: 7836},
	}
	c.udpPeerIP = map[string]string{"node-c": "198.51.100.9:7836"}
	c.udpMu.Unlock()

	if peer, ok := k.PeerAtAddr(&net.UDPAddr{IP: net.ParseIP("203.0.113.7"), Port: 9999}); !ok || peer != "node-b" {
		t.Fatalf("route lookup = %q,%v; want node-b,true", peer, ok)
	}
	if peer, ok := k.PeerAtAddr(&net.UDPAddr{IP: net.ParseIP("198.51.100.9"), Port: 1}); !ok || peer != "node-c" {
		t.Fatalf("observed-address lookup = %q,%v; want node-c,true", peer, ok)
	}
	if peer, ok := k.PeerAtAddr(&net.UDPAddr{IP: net.ParseIP("192.0.2.1")}); ok {
		t.Fatalf("unknown address resolved to %q", peer)
	}
	if _, ok := k.PeerAtAddr(nil); ok {
		t.Fatal("nil address resolved to a peer")
	}
}
