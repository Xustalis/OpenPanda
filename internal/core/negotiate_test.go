package core

import (
	"context"
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
