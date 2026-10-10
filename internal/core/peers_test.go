// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// dummyListener is the stand-in peer for reconcile tests that never need a
// hello — a TCP accept-and-drop socket on an ephemeral port.
func dummyListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { conn.Close() }()
		}
	}()
	return ln.Addr().String()
}

func TestSyncPeersAddRemove(t *testing.T) {
	c := newCore(t, "self", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := dummyListener(t)
	c.SyncPeers(ctx, []string{up})
	c.peerLoopsMu.Lock()
	_, ok := c.peerLoops[up]
	c.peerLoopsMu.Unlock()
	if !ok {
		t.Fatal("peer loop not started for added addr")
	}

	// Removing the addr cancels its loop.
	c.SyncPeers(ctx, nil)
	c.peerLoopsMu.Lock()
	_, ok = c.peerLoops[up]
	c.peerLoopsMu.Unlock()
	if ok {
		t.Fatal("peer loop survived removal")
	}
}

func TestSyncPeersKeepsLiveLoop(t *testing.T) {
	c := newCore(t, "self", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := dummyListener(t)
	c.SyncPeers(ctx, []string{up})
	c.peerLoopsMu.Lock()
	first := c.peerLoops[up]
	c.peerLoopsMu.Unlock()
	if first == nil {
		t.Fatal("peer loop not started")
	}

	// Re-syncing the same list must not respawn the loop — a config reload
	// that tears down healthy edges would flap the mesh on every SIGHUP.
	c.SyncPeers(ctx, []string{up})
	c.peerLoopsMu.Lock()
	second := c.peerLoops[up]
	c.peerLoopsMu.Unlock()
	if second == nil {
		t.Fatal("peer loop vanished on re-sync")
	}
	if reflect.ValueOf(first).Pointer() != reflect.ValueOf(second).Pointer() {
		t.Fatal("re-sync respawned a live peer loop")
	}
}

func TestProbePeerEncrypted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	server := newCore(t, "server", "127.0.0.1:17997")
	if err := server.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	go func() { _ = server.Listen(ctx, "127.0.0.1:17997") }()
	time.Sleep(150 * time.Millisecond)

	// The probe runs unsigned (nil keypair): reachability + session
	// negotiation are what the check answers, not consent.
	res, err := ProbePeer(ctx, "prober", ledger.Card{Device: "prober"}, config.ModelConfig{},
		config.NetworkConfig{SharedSecret: testSharedSecret}, "127.0.0.1:17997", nil, nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.PeerID != "server" {
		t.Fatalf("peer id = %q, want server", res.PeerID)
	}
	if !res.Encrypted {
		t.Fatal("probe reached a sessaead peer but the session did not arm")
	}
}

func TestProbePeerUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// Reserved TEST-NET address: the dial errors well inside the probe's
	// budget — the test is that "unreachable" is reported, not hung on.
	_, err := ProbePeer(ctx, "prober", ledger.Card{Device: "prober"}, config.ModelConfig{},
		config.NetworkConfig{SharedSecret: testSharedSecret}, "192.0.2.1:1", nil, nil)
	if err == nil {
		t.Fatal("probe of a dead address unexpectedly succeeded")
	}
}
