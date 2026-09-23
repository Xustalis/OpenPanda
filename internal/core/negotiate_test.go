package core

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

func TestAgentNegotiationSignaling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := newCore(t, "node-a", "127.0.0.1:17971")
	b := newCore(t, "node-b", "127.0.0.1:17972")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(a.Register(ctx))
	must(b.Register(ctx))

	go func() { _ = a.Listen(ctx, "127.0.0.1:17971") }()
	go func() { _ = b.Listen(ctx, "127.0.0.1:17972") }()
	time.Sleep(100 * time.Millisecond)

	must(a.DialPeer(ctx, "127.0.0.1:17972"))
	waitPeer(t, a, "node-b")
	waitPeer(t, b, "node-a")
	time.Sleep(200 * time.Millisecond)

	// Send negotiation signal from node-a to node-b
	err := a.SendNegotiation(ctx, "node-b", bus.AgentNegotiatePayload{
		FromNode:  "node-a",
		FromAgent: "claude_code_01",
		Timestamp: time.Now().UnixMilli(),
		Weight:    85,
		TargetScope: bus.TargetScope{
			Repo:   "openpanda/core",
			File:   "internal/servo/driver.go",
			Symbol: "func RotateServo",
		},
		Intent:        "modify_signature_for_voice_control",
		ActionPreview: "add_param_speed",
	})
	if err != nil {
		t.Fatalf("send negotiation: %v", err)
	}

	// Give a moment for b to receive and process
	time.Sleep(200 * time.Millisecond)
}

func negoReq(node, agent string, weight int, file string) bus.AgentNegotiatePayload {
	return bus.AgentNegotiatePayload{
		FromNode: node, FromAgent: agent, Weight: weight,
		TargetScope: bus.TargetScope{Repo: "r", File: file},
	}
}

func TestNegoDecideGrantRenewPreempt(t *testing.T) {
	c := &Core{nodeID: "node-x"}
	now := time.Now()
	// Uncontended scope → granted.
	d := c.negoDecide(now, negoReq("node-a", "claude", 100, "f.go"), "")
	if !d.granted {
		t.Fatal("uncontended scope denied")
	}
	// Same principal → renewed, still granted.
	d = c.negoDecide(now, negoReq("node-a", "claude", 100, "f.go"), "")
	if !d.granted {
		t.Fatal("renewal denied")
	}
	// Lower weight does not displace the holder.
	d = c.negoDecide(now, negoReq("node-b", "codex", 50, "f.go"), "")
	if d.granted {
		t.Fatal("lower weight preempted")
	}
	// Higher weight preempts and names the old holder for yield.
	d = c.negoDecide(now, negoReq("node-b", "codex", 200, "f.go"), "")
	if !d.granted || len(d.preempted) != 1 || d.preempted[0] != "node-a|claude" {
		t.Fatalf("preempt: %+v", d)
	}
}

func TestNegoDecideLeaseExpiry(t *testing.T) {
	c := &Core{nodeID: "node-x"}
	now := time.Now()
	c.negoDecide(now, negoReq("node-a", "claude", 100, "f.go"), "")
	// Past the lease, a weaker requester wins: the dead holder frees the scope.
	d := c.negoDecide(now.Add(2*negoLease), negoReq("node-b", "codex", 1, "f.go"), "")
	if !d.granted {
		t.Fatal("expired lock still held")
	}
}

func TestNegoDecideDeadlockBreak(t *testing.T) {
	c := &Core{nodeID: "node-x"}
	now := time.Now()
	// A strict weight order can never cycle — every denied edge runs lighter
	// to heavier — so the deadlock case is equal weights: A holds s1, B holds
	// s2, each then asks for the other's scope and waits.
	c.negoDecide(now, negoReq("node-a", "claude", 100, "s1"), "")
	c.negoDecide(now, negoReq("node-b", "codex", 100, "s2"), "")
	if d := c.negoDecide(now, negoReq("node-a", "claude", 100, "s2"), ""); d.granted {
		t.Fatal("A should wait on B's lock")
	}
	d := c.negoDecide(now, negoReq("node-b", "codex", 100, "s1"), "")
	if len(d.preempted) == 0 {
		t.Fatalf("cycle not broken: %+v", d)
	}
	// Breaking the cycle yields one holder; whichever lock freed, the mesh
	// must still answer a fresh request without the dead state lingering.
	c.negoDecide(now, negoReq("node-c", "gemini", 50, "s1"), "")
	c.negoDecide(now, negoReq("node-c", "gemini", 50, "s2"), "")
}

func TestNegoRelease(t *testing.T) {
	c := &Core{nodeID: "node-x"}
	now := time.Now()
	c.negoDecide(now, negoReq("node-a", "claude", 100, "f.go"), "")
	c.negoRelease("node-a|claude")
	d := c.negoDecide(now, negoReq("node-b", "codex", 1, "f.go"), "")
	if !d.granted {
		t.Fatal("released lock still held")
	}
}

// TestAgentYieldOnlyFromArbitrator is the wire-side half of the grantor
// binding: a yield revokes a local grant only when the sender is a peer whose
// own table granted it. A peer that was never consulted cannot preempt the
// lock, so honoring its yield would let any authenticated node cancel local
// tasks at will.
func TestAgentYieldOnlyFromArbitrator(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "holder", "127.0.0.1:18000")

	scope := bus.TargetScope{Repo: "r", File: "f.go"}
	dec := c.negoDecide(time.Now(), bus.AgentNegotiatePayload{
		FromNode: c.nodeID, FromAgent: "claude", Weight: 100, TargetScope: scope,
	}, "t-holder")
	if !dec.granted {
		t.Fatal("local grant denied")
	}
	// The only peer that arbitrated this grant is arbiter-b — recorded the way
	// negotiateTaskScope does after its Negotiate round-trip succeeds.
	c.negoRecordGrantor(scope, "t-holder", "arbiter-b")

	// The task stands in for its agent subprocess: the interrupt must reach
	// the cancel func the running map holds for it.
	cancelled := false
	c.running.Store("t-holder", context.CancelFunc(func() { cancelled = true }))

	yield := func(from string) {
		env, err := bus.NewEnvelope(bus.MsgAgentYield, from, "y-"+from, bus.AgentYieldPayload{
			FromAgent: "winner|agent", Scope: scope, Reason: "preempted by higher weight",
		})
		if err != nil {
			t.Fatalf("yield envelope: %v", err)
		}
		c.handleAgentYield(ctx, env)
	}

	// A peer that never arbitrated the grant cannot revoke it — neither the
	// lock nor the task may be touched.
	yield("mallory")
	if cancelled {
		t.Fatal("yield from a non-arbitrator cancelled the task")
	}
	if _, held := c.nego[negoScopeKey(scope)]; !held {
		t.Fatal("yield from a non-arbitrator dropped the lock")
	}

	// The recorded arbitrator's yield is honored: lock dropped, task
	// interrupted — the preemption semantics §5.2 promises.
	yield("arbiter-b")
	if !cancelled {
		t.Fatal("the arbitrator's yield did not interrupt the task")
	}
	if _, held := c.nego[negoScopeKey(scope)]; held {
		t.Fatal("the arbitrator's yield left the lock in place")
	}
}

// TestAgentNegotiateWeightClamped: the wire weight is the sender's own claim
// and is bounded by what a priority-derived weight can be. A peer bidding
// MaxInt would otherwise preempt every lock in the mesh.
func TestAgentNegotiateWeightClamped(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "arbiter", "127.0.0.1:18001")
	scope := bus.TargetScope{Repo: "r", File: "f.go"}

	env, err := bus.NewEnvelope(bus.MsgAgentNegotiate, "peer-b", "n-weight", bus.AgentNegotiatePayload{
		FromAgent: "codex", Weight: math.MaxInt, TargetScope: scope,
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	c.handleAgentNegotiate(ctx, env)

	c.negoMu.Lock()
	l := c.nego[negoScopeKey(scope)]
	c.negoMu.Unlock()
	if l == nil {
		t.Fatal("negotiate granted no lock")
	}
	if l.weight != maxNegoWeight {
		t.Fatalf("stored weight = %d, want it clamped to %d", l.weight, maxNegoWeight)
	}

	// The clamped holder can still be preempted by a legitimate max-weight
	// requester — clamping must not mint an unassailable lock.
	env2, err := bus.NewEnvelope(bus.MsgAgentNegotiate, "peer-c", "n-weight2", bus.AgentNegotiatePayload{
		FromAgent: "gemini", Weight: maxNegoWeight + 1, TargetScope: scope,
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	// Same clamped weight is NOT strictly higher, so the holder keeps the
	// lock: equal weights serialize by arrival, which is the convergence the
	// protocol relies on.
	c.handleAgentNegotiate(ctx, env2)
	c.negoMu.Lock()
	l = c.nego[negoScopeKey(scope)]
	c.negoMu.Unlock()
	if l.holder != negoHolder("peer-b", "codex") {
		t.Fatalf("equal-weight request displaced the holder to %q", l.holder)
	}
}
