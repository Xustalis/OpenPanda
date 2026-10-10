// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// TestPeerReconnectReplacesStaleConn verifies P1-7: when a peer redials (new
// conn, same authenticated id) while the incumbent is stale, the registry
// swaps to the new conn and closes the stale one — and crucially, when the
// stale conn's read loop then exits, removePeerForConn must NOT delete the
// fresh registration. Before the fix, the second hello was ignored (identity
// kept pointing at the dead conn), and the dead conn's cleanup removed the
// identity outright.
//
// A zero peerLivenessWindow makes arbitration distrust the incumbent outright,
// which is what a genuinely stale incumbent is: production uses the transport
// keepalive bound, under which this conn (freshly dialed and still answering)
// would be held as a same-id sibling session — see TestPeerLiveSiblingHoldsEdge.
func TestPeerReconnectReplacesStaleConn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := newCore(t, "node-a", "127.0.0.1:17951")
	b := newCore(t, "node-b", "127.0.0.1:17952")
	// Both arbiters distrust their incumbent. Zeroing only b's leaves a
	// holding O1 ("live" in its own frame) over O2 — a closes the conn b
	// just registered, and whether the edge survives depends on whose
	// goroutine wins the close-vs-reply race. A stale-incumbent world is
	// stale on both ends.
	a.peerLivenessWindow = 0
	b.peerLivenessWindow = 0
	for _, c := range []*Core{a, b} {
		if err := c.Register(ctx); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	go func() { _ = a.Listen(ctx, "127.0.0.1:17951") }()
	go func() { _ = b.Listen(ctx, "127.0.0.1:17952") }()
	time.Sleep(200 * time.Millisecond)

	// First connection.
	if err := a.DialPeer(ctx, "127.0.0.1:17952"); err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	var first *bus.Conn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if first = b.connFor("node-a"); first != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if first == nil {
		t.Fatalf("b has no conn for node-a after first dial")
	}

	// Reconnect: same identity, new conn. b must replace, not ignore.
	if err := a.DialPeer(ctx, "127.0.0.1:17952"); err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	var second *bus.Conn
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if second = b.connFor("node-a"); second != nil && second != first {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if second == nil {
		t.Fatalf("b lost node-a after reconnect")
	}
	if second == first {
		t.Fatalf("b kept the stale conn after reconnect")
	}

	// The stale conn's read loop exits on close and runs removePeerForConn.
	// Give it a moment, then the fresh registration must still be there and
	// still sendable.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b.connFor("node-a") == second {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b.connFor("node-a") != second {
		t.Fatalf("stale conn cleanup removed the replacement registration")
	}

	env, err := bus.NewEnvelope(bus.MsgHeartbeat, "node-b", "m-reconn", bus.HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if err := b.sendTo("node-a", env); err != nil {
		t.Fatalf("send on replacement conn: %v", err)
	}
}

// TestPeerLiveSiblingHoldsEdge pins the flap fix: a second conn claiming the
// SAME authenticated id while the incumbent is demonstrably live is a sibling
// session (a CLI/TUI engine shares the daemon's stable node id), not a
// reconnect. Previously every `panda` invocation swapped the edge here, the
// evicted side redialed and swapped back — a periodic connect/disconnect
// flap that stranded in-flight delegates and churned the directory
// online/offline. Now the incumbent holds; the newcomer still got its
// identity-binding hello reply and quiesces.
func TestPeerLiveSiblingHoldsEdge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := newCore(t, "node-a", "127.0.0.1:17961")
	b := newCore(t, "node-b", "127.0.0.1:17962")
	for _, c := range []*Core{a, b} {
		if err := c.Register(ctx); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	go func() { _ = a.Listen(ctx, "127.0.0.1:17961") }()
	go func() { _ = b.Listen(ctx, "127.0.0.1:17962") }()
	time.Sleep(200 * time.Millisecond)

	if err := a.DialPeer(ctx, "127.0.0.1:17962"); err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	first := b.connFor("node-a")
	if first == nil {
		t.Fatalf("b has no conn for node-a after first dial")
	}

	// A second same-id conn while the first is live: the incumbent holds.
	// (In production the newcomer is a sibling process's socket; here a
	// second DialPeer from the same test process exercises the same path.)
	if err := a.DialPeer(ctx, "127.0.0.1:17962"); err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := b.connFor("node-a"); got != first {
		t.Fatalf("b swapped a live same-id conn — the flap is back")
	}
}

// TestPeerAuthoritativeNewcomerTakesEdge is the stray-sibling wedge: a
// borrowed-engine process (a bare `panda` invocation that found the daemon
// down and claimed the node row) holds a live edge the real daemon can never
// reclaim under the sibling rule, and every frame the edge receives lands in
// the wrong process. The daemon marks its hello Authoritative — the one
// process allowed to preempt a live same-id incumbent — and the registry
// swaps to it.
func TestPeerAuthoritativeNewcomerTakesEdge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := newCore(t, "node-a", "127.0.0.1:17971")
	b := newCore(t, "node-b", "127.0.0.1:17972")
	for _, c := range []*Core{a, b} {
		if err := c.Register(ctx); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	go func() { _ = a.Listen(ctx, "127.0.0.1:17971") }()
	go func() { _ = b.Listen(ctx, "127.0.0.1:17972") }()
	time.Sleep(200 * time.Millisecond)

	// The stray sibling connects first: no authority, edge held by liveness.
	if err := a.DialPeer(ctx, "127.0.0.1:17972"); err != nil {
		t.Fatalf("sibling dial: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	first := b.connFor("node-a")
	if first == nil {
		t.Fatalf("b has no conn for node-a after sibling dial")
	}

	// The daemon comes up and dials over the same node id, now claiming
	// authority. The incumbent is live, so only the authority bit opens the
	// swap.
	a.SetEdgeAuthority(true)
	if err := a.DialPeer(ctx, "127.0.0.1:17972"); err != nil {
		t.Fatalf("authoritative dial: %v", err)
	}
	var second *bus.Conn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if second = b.connFor("node-a"); second != nil && second != first {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if second == nil || second == first {
		t.Fatalf("authoritative newcomer could not take the edge from a live sibling")
	}

	// And the evicted sibling's cleanup must not remove the daemon's edge.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b.connFor("node-a") == second {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b.connFor("node-a") != second {
		t.Fatalf("sibling cleanup removed the authoritative registration")
	}
}
