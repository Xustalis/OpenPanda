package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// TestPeerReconnectReplacesStaleConn verifies P1-7: when a peer redials (new
// conn, same authenticated id), the registry swaps to the new conn and closes
// the stale one — and crucially, when the stale conn's read loop then exits,
// removePeerForConn must NOT delete the fresh registration. Before the fix,
// the second hello was ignored (identity kept pointing at the dead conn), and
// the dead conn's cleanup removed the identity outright.
func TestPeerReconnectReplacesStaleConn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := newCore(t, "node-a", "127.0.0.1:17951")
	b := newCore(t, "node-b", "127.0.0.1:17952")
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
	time.Sleep(300 * time.Millisecond)
	first := b.connFor("node-a")
	if first == nil {
		t.Fatalf("b has no conn for node-a after first dial")
	}

	// Reconnect: same identity, new conn. b must replace, not ignore.
	if err := a.DialPeer(ctx, "127.0.0.1:17952"); err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	second := b.connFor("node-a")
	if second == nil {
		t.Fatalf("b lost node-a after reconnect")
	}
	if second == first {
		t.Fatalf("b kept the stale conn after reconnect")
	}

	// The stale conn's read loop exits on close and runs removePeerForConn.
	// Give it a moment, then the fresh registration must still be there and
	// still sendable.
	deadline := time.Now().Add(2 * time.Second)
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
