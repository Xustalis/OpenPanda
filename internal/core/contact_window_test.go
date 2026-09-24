package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// The plan is what creates the route: s holds a bundle for a destination
// with no live path anywhere, and the sweep cannot move it — until r1's
// advertised contact to the destination lands in the directory. Then custody
// moves one hop to the relay that will wake for the window, even though the
// window has not opened yet.
func TestDTNContactPlanMovesCustodyToScheduledHop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newCoreWithNative(t, "dtn-s5", "127.0.0.1:17990", ledger.NativeAbility{ID: "x", Command: "true"})
	r1 := newCoreWithNative(t, "dtn-r5", "127.0.0.1:17991", ledger.NativeAbility{ID: "x", Command: "true"})
	startPair(t, ctx, s, r1, "127.0.0.1:17990", "127.0.0.1:17991")

	const dest = "dtn-d5"
	now := time.Now().Unix()
	// r1 publishes a daily-pass-style window to d that opens in ten minutes:
	// the plan is the promise a relay can hold custody against.
	r1.SetContacts([]ledger.Contact{{Peer: dest, Start: now + 600, End: now + 3600}})
	window := []ledger.Contact{{Peer: dest, Start: now + 600, End: now + 3600}}

	// s's directory: r1 online but with NO plan yet, d offline behind nobody.
	seedNeighbor(t, s, r1.nodeID, []string{s.nodeID}, true)
	seedNeighbor(t, s, dest, nil, false)
	seedAdjacency(t, s, []string{r1.nodeID})

	// Park a bundle for d on s through the same path a live dispatch takes.
	s.taskOutboxPersist(ctx, dest, bus.TaskDelegatePayload{TaskID: "t-cw-1"}, "dtn", now+7200)
	if _, ok := parkedRow(t, s, dest, "t-cw-1"); !ok {
		t.Fatal("bundle did not park on s")
	}

	// No plan anywhere: the sweep has no route and custody stays on s.
	s.relayParked(ctx, "")
	if _, ok := parkedRow(t, s, dest, "t-cw-1"); !ok {
		t.Fatal("custody moved without any contact plan — live routing should have found nothing")
	}
	if _, ok := parkedRow(t, r1, dest, "t-cw-1"); ok {
		t.Fatal("bundle reached r1 without a contact plan")
	}

	// Gossip r1's plan into s's directory (what the next heartbeat carries).
	raw, _ := json.Marshal(window)
	if err := ledger.UpdateAdjacency(s.db, r1.nodeID, "", "", string(raw)); err != nil {
		t.Fatalf("seed r1 contacts: %v", err)
	}
	s.relayParked(ctx, "")

	// Custody moved to r1 — the scheduled hop — and parks there keyed to d,
	// waiting for the window. s holds nothing.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := parkedRow(t, r1, dest, "t-cw-1"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("custody never moved to the scheduled hop")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok := parkedRow(t, s, dest, "t-cw-1"); ok {
		t.Fatal("s kept custody after forwarding to the scheduled hop")
	}
	if via, _ := parkedRow(t, r1, dest, "t-cw-1"); via != s.nodeID {
		t.Fatalf("r1 parked via = %q, want %s", via, s.nodeID)
	}
}

// A peer that deletes its contact plan must clear the directories that
// cached it — an empty contacts array is a statement ("no plan"), while a
// heartbeat from an old node that lacks the field entirely must not touch
// the stored plan.
func TestHeartbeatContactPlanClearSemantics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newCoreWithNative(t, "dtn-s6", "127.0.0.1:17992", ledger.NativeAbility{ID: "x", Command: "true"})
	seedNeighbor(t, s, "dtn-peer6", nil, true)

	window := []ledger.Contact{{Peer: "dtn-d6", Start: 1000, End: 2000}}
	raw, _ := json.Marshal(window)
	if err := ledger.UpdateAdjacency(s.db, "dtn-peer6", "", "", string(raw)); err != nil {
		t.Fatalf("seed contacts: %v", err)
	}
	readPlan := func() []ledger.Contact {
		nodes, err := ledger.Query(s.db, "", "")
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for _, n := range nodes {
			if n.ID == "dtn-peer6" {
				return n.Contacts
			}
		}
		t.Fatal("peer6 row missing")
		return nil
	}
	if len(readPlan()) != 1 {
		t.Fatal("seeded plan did not land")
	}

	// An old node's beat has no contacts field at all: stored plan untouched.
	env := bus.Envelope{From: "dtn-peer6", Payload: json.RawMessage(`{"status":"online"}`)}
	s.handleHeartbeat(ctx, env)
	if len(readPlan()) != 1 {
		t.Fatal("absent contacts field cleared the stored plan")
	}

	// A new node emits "contacts":[] — the stored plan clears.
	env2, err := bus.NewEnvelope(bus.MsgHeartbeat, "dtn-peer6", "m-hb-6", bus.HeartbeatPayload{
		Status: "online", Contacts: []bus.Contact{},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.handleHeartbeat(ctx, env2)
	if got := readPlan(); len(got) != 0 {
		t.Fatalf("empty plan did not clear: %+v", got)
	}
}
