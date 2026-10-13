// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestTaskMessageTravelsTheWire drives the cross-node path: a child executing
// on one leaf posts to its sibling board, the envelope crosses the wire to
// the scope owner (the parent's node), the owner resolves the roster and fans
// back out, and the addressed replicas end up holding the slot.
//
// The fan-out proof is the second leaf: it never sends anything, so a slot on
// its board can only have arrived through the owner's fan-out — as can the
// sender label, which no node but the owner can resolve.
func TestTaskMessageTravelsTheWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	origin := newCore(t, "msg-origin", "127.0.0.1:18230")
	leafA := newCore(t, "msg-leaf-a", "127.0.0.1:18231")
	leafB := newCore(t, "msg-leaf-b", "127.0.0.1:18232")
	for _, c := range []*Core{origin, leafA, leafB} {
		if err := c.Register(ctx); err != nil {
			t.Fatalf("register %s: %v", c.nodeID, err)
		}
	}
	go func() { _ = origin.Listen(ctx, "127.0.0.1:18230") }()
	go func() { _ = leafA.Listen(ctx, "127.0.0.1:18231") }()
	go func() { _ = leafB.Listen(ctx, "127.0.0.1:18232") }()
	time.Sleep(150 * time.Millisecond)
	if err := origin.DialPeer(ctx, "127.0.0.1:18231"); err != nil {
		t.Fatalf("dial leaf a: %v", err)
	}
	if err := origin.DialPeer(ctx, "127.0.0.1:18232"); err != nil {
		t.Fatalf("dial leaf b: %v", err)
	}
	waitPeer(t, origin, "msg-leaf-a")
	waitPeer(t, origin, "msg-leaf-b")
	waitPeer(t, leafA, "msg-origin")
	waitPeer(t, leafB, "msg-origin")

	// The parent runs on the origin; it spawned three children, one dispatched
	// to each leaf and one kept local. Both ends hold the rows exactly as
	// SpawnChildTask + handleDelegate would have left them.
	parent := createTask(t, origin.store, "", "parent", origin.nodeID)
	sibling := createTask(t, origin.store, parent.TaskID, "sibling", origin.nodeID)
	child := createTask(t, origin.store, parent.TaskID, "child", origin.nodeID)
	peer := createTask(t, origin.store, parent.TaskID, "peer", origin.nodeID)
	if err := origin.store.RecordEvent(ctx, child.TaskID, EvDelegate, map[string]any{"target": leafA.nodeID}); err != nil {
		t.Fatalf("record child dispatch: %v", err)
	}
	if err := origin.store.RecordEvent(ctx, peer.TaskID, EvDelegate, map[string]any{"target": leafB.nodeID}); err != nil {
		t.Fatalf("record peer dispatch: %v", err)
	}
	if _, err := leafA.store.CreateWithID(ctx, child.TaskID, parent.TaskID, "proj", "child",
		origin.nodeID, []string{origin.nodeID, leafA.nodeID}, true); err != nil {
		t.Fatalf("create child row: %v", err)
	}
	if _, err := leafB.store.CreateWithID(ctx, peer.TaskID, parent.TaskID, "proj", "peer",
		origin.nodeID, []string{origin.nodeID, leafB.nodeID}, true); err != nil {
		t.Fatalf("create peer row: %v", err)
	}
	remoteChild, err := leafA.store.Get(ctx, child.TaskID)
	if err != nil {
		t.Fatalf("load child row: %v", err)
	}

	// Broadcast from leaf A — the member → owner leg crosses the wire.
	if err := leafA.deliverTaskMessage(ctx, remoteChild, msgRequest{To: "siblings", Grade: MsgGradeStatus, Body: "leaf says hi"}); err != nil {
		t.Fatalf("deliver broadcast: %v", err)
	}
	// Directed to the peer on leaf B, addressed by full id.
	if err := leafA.deliverTaskMessage(ctx, remoteChild, msgRequest{To: peer.TaskID, Grade: MsgGradeReport, Body: "peer, take the north half"}); err != nil {
		t.Fatalf("deliver directed: %v", err)
	}

	// The owner's board must hold both posts — this alone proves the member →
	// owner leg arrived over the wire, and that the owner resolved the
	// directed address against its roster.
	scope := "task:" + parent.TaskID
	deadline := time.Now().Add(8 * time.Second)
	for {
		msgs, _ := origin.store.TaskMessages(ctx, scope)
		if len(msgs) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("owner board never received the posts: %+v", msgs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	msgs, _ := origin.store.TaskMessages(ctx, scope)
	resolved := false
	for _, m := range msgs {
		if m.Rcpt == peer.TaskID && strings.Contains(m.Body, "north half") {
			resolved = true
		}
	}
	if !resolved {
		t.Fatalf("owner did not resolve the directed address: %+v", msgs)
	}

	// The fan-out leg: leaf B never sent anything, so both the addressed slot
	// and the sender label on it can only have come from the owner.
	for {
		msgs, _ = leafB.store.TaskMessages(ctx, scope)
		fanout, labeled := false, false
		for _, m := range msgs {
			if m.Rcpt == peer.TaskID && strings.Contains(m.Body, "north half") {
				fanout = true
				if m.SenderLabel == "child" {
					labeled = true
				}
			}
		}
		if fanout && labeled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("leaf B replica never received the labelled fan-out (slot=%v label=%v): %+v", fanout, labeled, msgs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The broadcast reached leaf B too (it is a member node), and the directed
	// note did not leak anywhere else on that node.
	for {
		msgs, _ = leafB.store.TaskMessages(ctx, scope)
		broadcast := false
		for _, m := range msgs {
			if m.Rcpt == "" && strings.Contains(m.Body, "leaf says hi") {
				broadcast = true
			}
		}
		if broadcast {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("leaf B never received the broadcast fan-out: %+v", msgs)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Reader-side semantics on all three nodes: the parent sees the broadcast
	// but not the directed note; the local sibling sees the broadcast alone;
	// the addressed peer sees both; the sender sees neither of its own words.
	lines := origin.drainTaskMessages(ctx, parent, msgSeen{})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "leaf says hi") || strings.Contains(joined, "north half") {
		t.Fatalf("parent board = %q", joined)
	}
	if lines := origin.drainTaskMessages(ctx, sibling, msgSeen{}); len(lines) != 1 {
		t.Fatalf("sibling board = %q", lines)
	}
	lines = leafB.drainTaskMessages(ctx, mustGet(t, leafB.store, peer.TaskID), msgSeen{})
	joined = strings.Join(lines, "\n")
	if !strings.Contains(joined, "north half") || !strings.Contains(joined, "leaf says hi") {
		t.Fatalf("peer board = %q", joined)
	}
	if lines := leafA.drainTaskMessages(ctx, remoteChild, msgSeen{}); len(lines) != 0 {
		t.Fatalf("sender echoed: %q", lines)
	}
}

// mustGet loads a row or fails — the board tests read rows the test itself
// just created, so a miss is a broken fixture, not a scenario.
func mustGet(t *testing.T, s *TaskStore, id string) Task {
	t.Helper()
	tk, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("load %s: %v", id, err)
	}
	return tk
}
