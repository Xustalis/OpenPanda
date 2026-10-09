// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// TestSessionHelloLeavesNoGhostRow pins the fix for fleet-list litter: a peer
// dialing in under an ephemeral session id (core.EphemeralNodeID — the shape
// pre-fix CLI sessions used) must not end up in the capability directory,
// neither via its signed-hello pubkey record nor its card upsert.
func TestSessionHelloLeavesNoGhostRow(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "self", "marker")

	sessID := "peer-1a2b3c4d" // the "-8hex" suffix marks a session, not a node
	pub, _, _ := bus.GenerateNodeKey()
	c.recordPeerPubKey(ctx, sessID, hex.EncodeToString(pub))

	var n int
	if err := c.db.QueryRow(`SELECT COUNT(1) FROM employee_cache WHERE id=?`, sessID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("session id %s created a directory row", sessID)
	}
}

// TestRecordPeerPubKeyReapsStaleAliases: a peer that proves a key already
// recorded under an older id owns that identity — the stale row is reaped so
// a rename does not ghost-pin the fleet.
func TestRecordPeerPubKeyReapsStaleAliases(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "self", "marker")

	pub, _, _ := bus.GenerateNodeKey()
	keyHex := hex.EncodeToString(pub)

	// Residue: the same key seen earlier under a renamed id and a session id.
	for _, id := range []string{"peer-oldname", "peer-1a2b3c4d"} {
		if _, err := c.db.Exec(
			`INSERT INTO employee_cache (id, name, pub_key, status, last_seen) VALUES (?, ?, ?, 'offline', 0)`,
			id, id, keyHex); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	c.recordPeerPubKey(ctx, "peer", keyHex)

	for _, id := range []string{"peer-oldname", "peer-1a2b3c4d"} {
		var n int
		if err := c.db.QueryRow(`SELECT COUNT(1) FROM employee_cache WHERE id=?`, id).Scan(&n); err != nil || n != 0 {
			t.Fatalf("stale alias %s should be reaped (count=%d, err=%v)", id, n, err)
		}
	}
	var stored string
	if err := c.db.QueryRow(`SELECT pub_key FROM employee_cache WHERE id='peer'`).Scan(&stored); err != nil || stored != keyHex {
		t.Fatalf("peer row: key=%q err=%v", stored, err)
	}
}

// TestPeerPubKeyEphemeralFallback: a session signs with its host node's
// keypair, so looking up the session id must fall through to the node's
// stable row — consent grants minted by a `panda ask` seat verify the same
// as ones minted by the daemon.
func TestPeerPubKeyEphemeralFallback(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "self", "marker")

	pub, _, _ := bus.GenerateNodeKey()
	c.recordPeerPubKey(ctx, "peer", hex.EncodeToString(pub))

	got, ok := c.peerPubKey("peer-1a2b3c4d")
	if !ok || !got.Equal(pub) {
		t.Fatalf("peerPubKey(ephemeral) = %v, want node key via base id", ok)
	}
}

// TestSweepReapsSessionGhosts: the periodic sweep deletes aged session-keyed
// rows but leaves stable offline nodes — those remain valid DTN targets.
func TestSweepReapsSessionGhosts(t *testing.T) {
	ctx := context.Background()
	c := pinCore(t, "self", "marker")

	old := time.Now().Add(-time.Hour).Unix()
	for _, id := range []string{"peer-1a2b3c4d", "peer-55667788"} {
		if _, err := c.db.Exec(
			`INSERT INTO employee_cache (id, name, status, last_seen) VALUES (?, ?, 'offline', ?)`,
			id, id, old); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if _, err := c.db.Exec(
		`INSERT INTO employee_cache (id, name, status, last_seen) VALUES ('peer', 'peer', 'offline', ?)`,
		old); err != nil {
		t.Fatalf("seed peer: %v", err)
	}

	c.sweepStalePeers(ctx)

	var n int
	if err := c.db.QueryRow(
		`SELECT COUNT(1) FROM employee_cache WHERE id LIKE 'peer-%'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("session ghosts survived sweep: %d", n)
	}
	if err := c.db.QueryRow(`SELECT COUNT(1) FROM employee_cache WHERE id='peer'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stable offline row must survive (count=%d, err=%v)", n, err)
	}
}
