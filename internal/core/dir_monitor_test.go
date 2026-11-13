// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"
	"time"
)

// TestRunDirMonitorExpiresSilentPeers pins the loop an embedded mesh owner
// needs: a console-only node (`panda web`, the sidecar) holds the identity
// lock, the listener and the peer set, so no daemon sweep is coming. Before
// this loop the row of a peer that died mid-session stayed `online` for the
// console's whole lifetime — the map drew a machine that was gone, routing
// aimed it, and both remove paths refused to delete a row that claimed to be
// up.
func TestRunDirMonitorExpiresSilentPeers(t *testing.T) {
	c := pinCore(t, "self", "marker")
	c.dirSweepEvery = 20 * time.Millisecond

	// The shape a killed process leaves behind: status online, last_seen an
	// hour stale.
	old := time.Now().Add(-time.Hour).Unix()
	if _, err := c.db.Exec(
		`INSERT INTO employee_cache (id, name, status, last_seen) VALUES ('ghost', 'ghost', 'online', ?)`,
		old); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.RunDirMonitor(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := c.db.QueryRow(`SELECT status FROM employee_cache WHERE id='ghost'`).Scan(&status); err != nil {
			t.Fatalf("read ghost: %v", err)
		}
		if status == "offline" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("silent peer stayed online — the directory sweep never ran")
}

// TestRunDirMonitorLeavesTheSelfRowAlone: the sweep must not touch this
// node's own row (a console that flipped itself offline would read as down
// on every peer's map until its next beat).
func TestRunDirMonitorLeavesTheSelfRowAlone(t *testing.T) {
	c := pinCore(t, "self", "marker")
	c.dirSweepEvery = 20 * time.Millisecond

	old := time.Now().Add(-time.Hour).Unix()
	// The fixture already filed this node's row; make it look silent.
	if _, err := c.db.Exec(
		`UPDATE employee_cache SET status='online', last_seen=? WHERE id=?`,
		old, c.nodeID); err != nil {
		t.Fatalf("age self row: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.RunDirMonitor(ctx)

	// Two ticks is enough to prove the sweep ran; a third would not make the
	// negative any more true.
	time.Sleep(100 * time.Millisecond)
	var status string
	if err := c.db.QueryRow(`SELECT status FROM employee_cache WHERE id=?`, c.nodeID).Scan(&status); err != nil {
		t.Fatalf("read self row: %v", err)
	}
	if status != "online" {
		t.Fatalf("self row status = %q, want untouched", status)
	}
}
