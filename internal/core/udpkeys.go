// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/cipher"
	"encoding/hex"
	"net"
	"sync"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// udpKeys supplies the datagram plane's per-pair AEADs (bus.UDPKeySource).
//
// The mesh-wide key every member shares proves membership and nothing more: a
// member holding it can mint a frame claiming to be any other member, and the
// only thing standing in the way was a source-address match — which two peers
// behind one NAT, or two hosts on one LAN, both satisfy. Sealing to the pair
// replaces that with proof: the key comes out of the two X25519 identity keys,
// so only the peer named in the frame could have produced it.
//
// Derivation costs one ECDH plus one HKDF, and peerXPub is a store read, so a
// derived key is cached. recordPeerPubKey drops the entry whenever a peer's
// advertised key changes; nothing else can make a cached key wrong.
type udpKeys struct {
	c  *Core
	mu sync.Mutex
	m  map[string]cipher.AEAD // nil value caches "publishes no identity key"
}

func newUDPKeys(c *Core) *udpKeys { return &udpKeys{c: c, m: make(map[string]cipher.AEAD)} }

// PairKey implements bus.UDPKeySource.
func (k *udpKeys) PairKey(peer string) (cipher.AEAD, bool) {
	if peer == "" {
		return nil, false
	}
	k.mu.Lock()
	aead, cached := k.m[peer]
	k.mu.Unlock()
	if cached {
		return aead, aead != nil
	}
	derived := k.derive(peer)
	k.mu.Lock()
	k.m[peer] = derived
	k.mu.Unlock()
	return derived, derived != nil
}

// PeerAtAddr names the peer whose confirmed endpoint is src — the reverse of
// udpPeerAddrOK, used only to refuse a legacy mesh-key frame from an address
// that belongs to a peer able to seal to the pair.
func (k *udpKeys) PeerAtAddr(src *net.UDPAddr) (string, bool) {
	if src == nil {
		return "", false
	}
	c := k.c
	c.udpMu.Lock()
	defer c.udpMu.Unlock()
	for peer, r := range c.udpRoutes {
		if r.IP.Equal(src.IP) {
			return peer, true
		}
	}
	for peer, obs := range c.udpPeerIP {
		h, _, err := net.SplitHostPort(obs)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(h); ip != nil && ip.Equal(src.IP) {
			return peer, true
		}
	}
	return "", false
}

// forget drops a cached key for one peer (identity changed, or the row moved).
func (k *udpKeys) forget(peer string) {
	k.mu.Lock()
	delete(k.m, peer)
	k.mu.Unlock()
}

// derive computes the pair AEAD from this node's X25519 key and the peer's
// advertised one. A peer with no published key returns nil, and the plane
// falls back to the mesh key for it — the pre-upgrade behaviour, and the only
// option toward a build that never publishes one.
func (k *udpKeys) derive(peer string) cipher.AEAD {
	priv, _, ok := k.c.nodeXKey()
	if !ok {
		return nil
	}
	// The lookup is an indexed read on employee_cache, well inside a
	// datagram's budget; the cache above is what keeps it off the hot path.
	pub, ok := k.c.peerXPub(context.Background(), peer)
	if !ok {
		return nil
	}
	aead, err := bus.UDPPairAEAD(priv, pub, k.c.nodeID, peer)
	if err != nil {
		k.c.logger.Warn("udp: derive pair key", "peer", peer, "err", err)
		return nil
	}
	k.c.logger.Debug("udp: pair key derived", "peer", peer,
		"x_pub", hex.EncodeToString(pub.Bytes())[:16])
	return aead
}
