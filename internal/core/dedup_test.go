package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestMutualDialDedup verifies the mutual-dial tie-break: when two nodes dial
// each other simultaneously (the common deployment — peers configured on both
// sides), each side ends up with one outbound and one inbound conn to the same
// peer. Deterministic dedup must keep exactly one conn per side, both sides
// must agree on which one, and the connections must stay stable — before the
// fix, the second registration closed the first, whose reconnect loop redialed
// a second later and displaced the other side's conn in an endless flap that
// churned the capability directory offline/online.
func TestMutualDialDedup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	// Both sides dial each other, daemon-style (MaintainPeer loops).
	done := make(chan error, 2)
	go func() { done <- a.MaintainPeer(ctx, "127.0.0.1:17962") }()
	go func() { done <- b.MaintainPeer(ctx, "127.0.0.1:17961") }()

	// Settling window: handshakes plus one former flap period.
	time.Sleep(2 * time.Second)

	firstA, firstB := a.connFor("node-b"), b.connFor("node-a")
	if firstA == nil || firstB == nil {
		t.Fatalf("peer not connected: a→b=%v b→a=%v", firstA, firstB)
	}

	// The registrations must hold steady — no replacement churn.
	time.Sleep(1500 * time.Millisecond)
	if got := a.connFor("node-b"); got != firstA {
		t.Fatalf("a's conn for node-b flapped: %p → %p", firstA, got)
	}
	if got := b.connFor("node-a"); got != firstB {
		t.Fatalf("b's conn for node-a flapped: %p → %p", firstB, got)
	}

	// MaintainPeer must still be blocked (the edge is alive), not returned
	// into a redial loop.
	select {
	case err := <-done:
		t.Fatalf("MaintainPeer returned while the edge was alive: %v", err)
	default:
	}

	// The capability directory must show both nodes online on both sides —
	// the flap used to stamp peers offline mid-churn.
	for _, tc := range []struct {
		c    *Core
		want string
	}{{a, "node-b"}, {b, "node-a"}} {
		nodes, err := ledger.Query(tc.c.db, "online", "")
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		found := false
		for _, n := range nodes {
			if n.ID == tc.want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s directory: %s not online (got %d online nodes)", tc.c.nodeID, tc.want, len(nodes))
		}
	}
}
