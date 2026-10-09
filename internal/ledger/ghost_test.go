// SPDX-License-Identifier: AGPL-3.0-or-later

package ledger

import (
	"database/sql"
	"testing"
	"time"
)

// seedRow inserts a minimal directory row with the given liveness stamp.
func seedRow(t *testing.T, db *sql.DB, id, pubKey, status string, lastSeen int64) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO employee_cache (id, name, pub_key, status, last_seen) VALUES (?, ?, ?, ?, ?)`,
		id, id, pubKey, status, lastSeen); err != nil {
		t.Fatalf("seed row %s: %v", id, err)
	}
}

func TestSessionRowID(t *testing.T) {
	for id, want := range map[string]bool{
		"macbook-1f3a2b4c":        true,
		"macbook-DEADBEEF":        true,
		"macbook":                 false,
		"macbook@vm-1f3a2b4c5d6e": false, // vm identity suffix is 12 hex — stable
		"macbook-1f3a2b4":         false, // 7 hex — not the session form
		"macbook-1f3a2b4cc":       false, // 9 hex
		"macbook-1f3a2b4g":        false, // non-hex
		"-1f3a2b4c":               false, // empty base
	} {
		if got := SessionRowID(id); got != want {
			t.Errorf("SessionRowID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestReapGhosts(t *testing.T) {
	db := openLedgerDB(t)
	old := time.Now().Add(-time.Hour).Unix()
	fresh := time.Now().Unix()
	// An old session ghost — the canonical litter.
	seedRow(t, db, "peer-1a2b3c4d", "k1", "offline", old)
	// A ghost-shaped row that is still fresh — the age floor protects a
	// session mid-handshake.
	seedRow(t, db, "peer-9e8d7c6b", "k2", "offline", fresh)
	// A stable offline node — kept forever (DTN pins target it).
	seedRow(t, db, "peer", "k3", "offline", old)
	// A live-connected ghost id — excluded by the caller.
	seedRow(t, db, "peer-55667788", "k4", "online", old)

	reaped, err := ReapGhosts(db, 900, []string{"peer-55667788"})
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != "peer-1a2b3c4d" {
		t.Fatalf("reaped = %v, want [peer-1a2b3c4d]", reaped)
	}
	for _, id := range []string{"peer-9e8d7c6b", "peer", "peer-55667788"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(1) FROM employee_cache WHERE id=?`, id).Scan(&n); err != nil || n != 1 {
			t.Fatalf("row %s should survive (count=%d, err=%v)", id, n, err)
		}
	}
}

func TestReapKeySiblings(t *testing.T) {
	db := openLedgerDB(t)
	// The live row: the node just proved this key under the id "peer".
	seedRow(t, db, "peer", "samekey", "online", time.Now().Unix())
	// Its stale aliases: a rename and a session ghost that recorded the key.
	seedRow(t, db, "peer-old", "samekey", "offline", 100)
	seedRow(t, db, "peer-1a2b3c4d", "samekey", "offline", 100)
	// Untouched: a different node's row, and the self row under protection.
	seedRow(t, db, "other", "otherkey", "offline", 100)
	seedRow(t, db, "self", "samekey", "offline", 100)

	reaped, err := ReapKeySiblings(db, "peer", "samekey", "self")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(reaped) != 2 {
		t.Fatalf("reaped = %v, want peer-old + peer-1a2b3c4d", reaped)
	}
	for _, id := range []string{"peer", "other", "self"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(1) FROM employee_cache WHERE id=?`, id).Scan(&n); err != nil || n != 1 {
			t.Fatalf("row %s should survive (count=%d, err=%v)", id, n, err)
		}
	}
}
