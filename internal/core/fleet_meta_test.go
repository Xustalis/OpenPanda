// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// Track 3 fleet observability: heartbeat-carried version stamps, per-peer
// transport kinds in link metrics, and queue depth in capacity snapshots.

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// A heartbeat's Ver field stamps the sender's directory row; an old node's
// beat (no field) leaves the stored stamp alone.
func TestHeartbeatStampsPeerVersion(t *testing.T) {
	ctx := context.Background()
	s := newCoreWithNative(t, "fleet-a", "127.0.0.1:18241", ledger.NativeAbility{ID: "x", Command: "true"})
	seedNeighbor(t, s, "fleet-b", nil, true)

	env, err := bus.NewEnvelope(bus.MsgHeartbeat, "fleet-b", "m-hb-v1", bus.HeartbeatPayload{
		Status: "online", Ver: "9.9.9",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.handleHeartbeat(ctx, env)

	ver := func() string {
		nodes, err := ledger.Query(s.db, "", "")
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for _, n := range nodes {
			if n.ID == "fleet-b" {
				return n.Ver
			}
		}
		t.Fatal("fleet-b row missing")
		return ""
	}
	if got := ver(); got != "9.9.9" {
		t.Fatalf("ver = %q, want 9.9.9", got)
	}

	// Old-node beat: field absent — the stamp must survive.
	env2, err := bus.NewEnvelope(bus.MsgHeartbeat, "fleet-b", "m-hb-v2", bus.HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatal(err)
	}
	s.handleHeartbeat(ctx, env2)
	if got := ver(); got != "9.9.9" {
		t.Fatalf("ver after empty beat = %q, want 9.9.9 preserved", got)
	}
}

// A peer reachable only through a punched datagram route advertises a udp
// edge (RTT unmeasured); a ws peer would carry kind "ws" plus its ping.
func TestLinkMetricsTransportKinds(t *testing.T) {
	c := newCoreWithNative(t, "fleet-c", "127.0.0.1:18242", ledger.NativeAbility{ID: "x", Command: "true"})

	c.udpMu.Lock()
	c.udpRoutes = map[string]*net.UDPAddr{
		"udp-peer": {IP: net.ParseIP("10.0.0.9"), Port: 7836},
	}
	c.udpMu.Unlock()

	lms := c.linkMetrics()
	if len(lms) != 1 || lms[0].Peer != "udp-peer" || lms[0].Kind != "udp" || lms[0].RTTms != 0 {
		t.Fatalf("linkMetrics = %+v, want one udp edge to udp-peer with no rtt", lms)
	}
}

// The heartbeat's capacity snapshot reports the queued backlog so peers and
// the fleet panel see depth, not just in-flight counts.
func TestCapacitySnapshotQueuedDepth(t *testing.T) {
	c := newCoreWithNative(t, "fleet-d", "127.0.0.1:18243", ledger.NativeAbility{ID: "x", Command: "true"})
	ctx := context.Background()

	for i, state := range []string{"queued", "queued", "running"} {
		id := "tq-" + string(rune('a'+i))
		if _, err := c.db.Exec(`INSERT INTO tasks (task_id, title, state, owner_node, attempt_id, created_at, updated_at)
			VALUES (?, 't', ?, ?, 'a', 0, 0)`, id, state, c.nodeID); err != nil {
			t.Fatalf("insert task %s: %v", id, err)
		}
	}
	capJSON, _ := c.node.capacitySnapshot(ctx)
	var cap struct {
		QueuedTasks  int `json:"queued_tasks"`
		CurrentTasks int `json:"current_tasks"`
	}
	if err := json.Unmarshal([]byte(capJSON), &cap); err != nil {
		t.Fatalf("unmarshal capacity: %v", err)
	}
	if cap.QueuedTasks != 2 {
		t.Fatalf("queued_tasks = %d, want 2", cap.QueuedTasks)
	}
	if cap.CurrentTasks != 1 {
		t.Fatalf("current_tasks = %d, want 1", cap.CurrentTasks)
	}
}
