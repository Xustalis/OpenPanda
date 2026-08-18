package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestCapacityFullDeclinesDelegation verifies the DCPS capacity-driven
// accept/decline: a node at its MaxConcurrent limit declines an inbound
// delegation (so the delegator can re-route) instead of silently queueing.
func TestCapacityFullDeclinesDelegation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "tiny",
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:info", Command: "uname"}},
		Capacity:      ledger.Capacity{CPUCores: 2, RAMGB: 4, MaxConcurrent: 1},
	}
	c := NewCore(db, "tiny", card, 5, testLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)

	// Occupy the single execution slot with a running task owned by this node.
	busy, err := c.store.Create(ctx, "", "", "busy task", "tiny", []string{"tiny"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.store.Queue(ctx, busy.TaskID, "tiny"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, busy.TaskID, "tiny", "tiny"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, busy.TaskID, "tiny"); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// A delegated task arrives while the slot is taken.
	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, "origin", "m1", bus.TaskDelegatePayload{
		TaskID: "t-capfull", Title: "overflow", Intent: "do work",
		Requires: []string{"sys:info"}, Chain: []string{"origin"},
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleDelegate(ctx, env)

	got, err := c.store.Get(ctx, "t-capfull")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateCancelled {
		t.Fatalf("overflow task state = %s, want cancelled (declined + terminalized)", got.State)
	}

	// Once the slot frees, the same delegation is accepted (positive control).
	if err := c.store.ForceFail(ctx, busy.TaskID, "done"); err != nil {
		t.Fatalf("free slot: %v", err)
	}
	env2, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "origin", "m2", bus.TaskDelegatePayload{
		TaskID: "t-capfree", Title: "now fits", Intent: "do work",
		Requires: []string{"sys:info"}, Chain: []string{"origin"},
	})
	c.handleDelegate(ctx, env2)
	// The task must not have been terminalized this time: it is either already
	// done (fast native command) or still in flight.
	got2, err := c.store.Get(ctx, "t-capfree")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got2.State == StateCancelled {
		t.Fatalf("task with free capacity was declined")
	}
}

// TestWireHeartbeatRefreshesPeer verifies the TMB data link: a broadcast
// heartbeat updates the receiving node's last_seen + capacity for the sender,
// so the freshness discount and weighted score run on live data.
func TestWireHeartbeatRefreshesPeer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := newCore(t, "hb-a", "127.0.0.1:17921")
	b := newCore(t, "hb-b", "127.0.0.1:17922")
	startPair(t, ctx, a, b, "127.0.0.1:17921", "127.0.0.1:17922")

	// Age b's view of a artificially, then broadcast a real heartbeat from a.
	if _, err := b.db.Exec(`UPDATE employee_cache SET last_seen=? WHERE id=?`, time.Now().Unix()-7200, "hb-a"); err != nil {
		t.Fatalf("age last_seen: %v", err)
	}
	a.broadcastHeartbeat(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		nodes, err := ledger.Query(b.db, "", "hb-a")
		if err == nil && len(nodes) == 1 && nodes[0].LastSeen > time.Now().Unix()-30 {
			return // b's slot for a is fresh again
		}
		time.Sleep(50 * time.Millisecond)
	}
	nodes, _ := ledger.Query(b.db, "", "hb-a")
	t.Fatalf("heartbeat did not refresh peer slot: %+v", nodes)
}
