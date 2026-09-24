package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// seedNeighbor writes a directory row with the given advertised adjacency —
// the link-state input DTN routing reads. online=false leaves the row marked
// offline, which is the state a store-and-forward destination is usually in.
func seedNeighbor(t *testing.T, c *Core, id string, neighbors []string, online bool) {
	t.Helper()
	if err := ledger.UpsertRemote(c.db, id, ledger.CapabilitySummary{Neighbors: neighbors}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	if !online {
		if err := ledger.Heartbeat(c.db, id, "offline", ""); err != nil {
			t.Fatalf("offline %s: %v", id, err)
		}
	}
}

// seedAdjacency sets this node's own advertised edge set, the self row's
// half of the link-state graph.
func seedAdjacency(t *testing.T, c *Core, neighbors []string) {
	t.Helper()
	raw, _ := json.Marshal(neighbors)
	if err := ledger.UpdateAdjacency(c.db, c.nodeID, string(raw), ""); err != nil {
		t.Fatalf("adjacency: %v", err)
	}
}

func mkTestBundle(t *testing.T, c *Core, bundleID, taskID, dest string) (*bus.Bundle, []byte) {
	t.Helper()
	payload, err := json.Marshal(bus.TaskDelegatePayload{TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	bnd, err := bus.NewBundle(bundleID, bus.EID(c.nodeID), bus.EID(dest),
		bus.MsgTaskDelegate, int64(time.Hour.Seconds()), payload, []byte(testSharedSecret))
	if err != nil {
		t.Fatal(err)
	}
	return bnd, bnd.Marshal()
}

func parkedRow(t *testing.T, c *Core, peer, taskID string) (via string, found bool) {
	t.Helper()
	err := c.db.QueryRow(`SELECT via FROM task_outbox WHERE peer = ? AND task_id = ?`,
		peer, taskID).Scan(&via)
	return via, err == nil
}

// S → R1 → R2 → D(offline): a bundle handed to R1 must move one custody hop
// closer to its dead destination — verbatim, un-resigned — and land parked
// at R2, the online node that advertises the last edge to D.
func TestDTNRelayMultiHopMovesCustody(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newCoreWithNative(t, "dtn-s", "127.0.0.1:17980", ledger.NativeAbility{ID: "x", Command: "true"})
	r1 := newCoreWithNative(t, "dtn-r1", "127.0.0.1:17981", ledger.NativeAbility{ID: "x", Command: "true"})
	r2 := newCoreWithNative(t, "dtn-r2", "127.0.0.1:17982", ledger.NativeAbility{ID: "x", Command: "true"})
	startPair(t, ctx, s, r1, "127.0.0.1:17980", "127.0.0.1:17981")
	startPair(t, ctx, r1, r2, "127.0.0.1:17981", "127.0.0.1:17982")

	// R1's view: s and r2 live; d exists offline behind r2.
	seedNeighbor(t, r1, s.nodeID, []string{r1.nodeID}, true)
	seedNeighbor(t, r1, r2.nodeID, []string{r1.nodeID, "dtn-d"}, true)
	seedNeighbor(t, r1, "dtn-d", []string{r2.nodeID}, false)
	seedAdjacency(t, r1, []string{s.nodeID, r2.nodeID})
	// R2's view: r1 live, d offline and directly adjacent.
	seedNeighbor(t, r2, r1.nodeID, []string{r2.nodeID}, true)
	seedNeighbor(t, r2, "dtn-d", []string{r2.nodeID}, false)
	seedAdjacency(t, r2, []string{r1.nodeID, "dtn-d"})

	bnd, blob := mkTestBundle(t, s, "bnd-mh-1", "t-mh-1", "dtn-d")
	env, err := bus.NewEnvelope(bus.MsgDTNBundle, s.nodeID, "m-mh-1", bus.DTNBundlePayload{Blob: blob})
	if err != nil {
		t.Fatal(err)
	}
	r1.handleDTNBundle(ctx, env)

	// R2 parks custody for dtn-d with via=r1; R1 holds nothing — custody moved.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if via, ok := parkedRow(t, r2, "dtn-d", "t-mh-1"); ok {
			if via != r1.nodeID {
				t.Fatalf("r2 parked via = %q, want %s", via, r1.nodeID)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bundle never parked at r2")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok := parkedRow(t, r1, "dtn-d", "t-mh-1"); ok {
		t.Fatal("r1 kept custody after forwarding")
	}
	if _, ok := parkedRow(t, s, "dtn-d", "t-mh-1"); ok {
		t.Fatal("bundle echoed back to sender")
	}
	_ = bnd
}

// When the only route runs back through the arriving peer, the relay parks
// instead of echoing — and records via so a later flush keeps the rule.
func TestDTNRelayParksRatherThanEchoes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newCoreWithNative(t, "dtn-s2", "127.0.0.1:17983", ledger.NativeAbility{ID: "x", Command: "true"})
	r1 := newCoreWithNative(t, "dtn-r1b", "127.0.0.1:17984", ledger.NativeAbility{ID: "x", Command: "true"})
	startPair(t, ctx, s, r1, "127.0.0.1:17983", "127.0.0.1:17984")

	// R1's whole map to d leads back through s.
	seedNeighbor(t, r1, s.nodeID, []string{r1.nodeID, "dtn-d2"}, true)
	seedNeighbor(t, r1, "dtn-d2", []string{s.nodeID}, false)
	seedAdjacency(t, r1, []string{s.nodeID})

	_, blob := mkTestBundle(t, s, "bnd-mh-2", "t-mh-2", "dtn-d2")
	env, err := bus.NewEnvelope(bus.MsgDTNBundle, s.nodeID, "m-mh-2", bus.DTNBundlePayload{Blob: blob})
	if err != nil {
		t.Fatal(err)
	}
	r1.handleDTNBundle(ctx, env)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if via, ok := parkedRow(t, r1, "dtn-d2", "t-mh-2"); ok {
			if via != s.nodeID {
				t.Fatalf("parked via = %q, want %s", via, s.nodeID)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bundle not parked at r1")
		}
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if _, ok := parkedRow(t, s, "dtn-d2", "t-mh-2"); ok {
		t.Fatal("echoed bundle parked on the sender")
	}
}

// A parked bundle must find its next hop when the topology improves: the row
// is keyed to the dead destination, and the sweep recomputes the route.
func TestDTNRelaySweepMovesParkedCustody(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r1 := newCoreWithNative(t, "dtn-r1c", "127.0.0.1:17985", ledger.NativeAbility{ID: "x", Command: "true"})
	r2 := newCoreWithNative(t, "dtn-r2c", "127.0.0.1:17986", ledger.NativeAbility{ID: "x", Command: "true"})
	if err := r1.Register(ctx); err != nil {
		t.Fatal(err)
	}
	// Park a bundle for an unreachable dest before r2 exists in the graph.
	bnd, blob := mkTestBundle(t, r1, "bnd-mh-3", "t-mh-3", "dtn-d3")
	seedNeighbor(t, r1, "dtn-d3", []string{"nowhere"}, false)
	seedAdjacency(t, r1, []string{"ghost"})
	r1.parkBundle(ctx, "ghost", "dtn-d3", bnd, blob)

	// r2 comes online adjacent to r1 and advertises the last edge to d3.
	startPair(t, ctx, r1, r2, "127.0.0.1:17985", "127.0.0.1:17986")
	seedNeighbor(t, r1, r2.nodeID, []string{r1.nodeID, "dtn-d3"}, true)
	seedAdjacency(t, r1, []string{r2.nodeID})
	seedNeighbor(t, r2, "dtn-d3", []string{r2.nodeID}, false)
	seedAdjacency(t, r2, []string{r1.nodeID, "dtn-d3"})

	r1.relayParked(ctx, "")

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := parkedRow(t, r2, "dtn-d3", "t-mh-3"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("parked bundle never moved to r2")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok := parkedRow(t, r1, "dtn-d3", "t-mh-3"); ok {
		t.Fatal("r1 kept custody after the sweep forwarded it")
	}
}

// relayForwardOK is the per-node loop bound: a bundle may be forwarded at
// most dtnRelayMaxHops times before the node must hold it for direct contact.
// The bound is persisted — a restart must not re-arm it, or a rebooted relay
// can be ping-ponged until the TTL.
func TestRelayForwardOKBoundsLoops(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithNative(t, "dtn-bound", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	deadline := time.Now().Add(time.Hour).Unix()
	for i := 0; i < dtnRelayMaxHops; i++ {
		if !c.relayForwardOK(ctx, "b-loop", deadline) {
			t.Fatalf("forward %d refused before bound", i)
		}
	}
	if c.relayForwardOK(ctx, "b-loop", deadline) {
		t.Fatal("forward past the bound allowed")
	}
	// A different bundle is unaffected.
	if !c.relayForwardOK(ctx, "b-other", deadline) {
		t.Fatal("fresh bundle refused")
	}
	// The bound lives in the DB, not the process: wiping the in-memory
	// fallback must not re-arm the bound.
	c.mu.Lock()
	c.relayLog = nil
	c.mu.Unlock()
	if c.relayForwardOK(ctx, "b-loop", deadline) {
		t.Fatal("bound re-armed after restart simulation")
	}
	var hops int
	if err := c.db.QueryRow(`SELECT hops FROM dtn_relay_log WHERE bundle_id = 'b-loop'`).Scan(&hops); err != nil {
		t.Fatalf("relay bound not persisted: %v", err)
	}
	if hops != dtnRelayMaxHops {
		t.Fatalf("persisted hops = %d, want %d", hops, dtnRelayMaxHops)
	}
	// An expired record resurrects: a bundle whose deadline passed gets a
	// fresh bound under its new clock.
	past := time.Now().Add(-time.Hour).Unix()
	if !c.relayForwardOK(ctx, "b-expired", past) {
		t.Fatal("unseen bundle refused")
	}
	if !c.relayForwardOK(ctx, "b-expired", deadline) {
		t.Fatal("resurrected record lost its remaining budget")
	}
}

// A bundle with no wire deadline must still get an expiring relay record —
// the loop bound's memory cannot outlive the bundle forever just because the
// origin never set one.
func TestRelayForwardOKBoundsDeadlinelessBundles(t *testing.T) {
	c := newCoreWithNative(t, "dtn-bound", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	if !c.relayForwardOK(context.Background(), "b-nodeadline", 0) {
		t.Fatal("deadline-less bundle refused")
	}
	var until int64
	if err := c.db.QueryRow(`SELECT until FROM dtn_relay_log WHERE bundle_id = 'b-nodeadline'`).Scan(&until); err != nil {
		t.Fatalf("deadline-less bundle recorded nothing: %v", err)
	}
	if until <= time.Now().Unix() {
		t.Fatalf("deadline-less bundle recorded no expiry: until=%d", until)
	}
}

// A bundle parked once may legitimately arrive again over a different path —
// and the no-echo rule must follow the LAST inbound peer, not the first on
// record. The park upsert updates via.
func TestParkBundleUpdatesVia(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithNative(t, "dtn-via", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	bnd, blob := mkTestBundle(t, c, "bnd-via-1", "t-via-1", "dtn-d")

	c.parkBundle(ctx, "peer-a", "dtn-d", bnd, blob)
	if via, ok := parkedRow(t, c, "dtn-d", "t-via-1"); !ok || via != "peer-a" {
		t.Fatalf("first park via = %q ok=%v, want peer-a", via, ok)
	}
	c.parkBundle(ctx, "peer-b", "dtn-d", bnd, blob)
	if via, ok := parkedRow(t, c, "dtn-d", "t-via-1"); !ok || via != "peer-b" {
		t.Fatalf("re-park via = %q ok=%v, want peer-b", via, ok)
	}
}
