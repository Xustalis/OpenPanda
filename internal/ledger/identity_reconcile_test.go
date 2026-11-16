// SPDX-License-Identifier: AGPL-3.0-or-later

package ledger

import (
	"testing"
)

// TestUpsertReconcilesRenamedIdentity pins the directory's one-box-one-row
// rule: a row filed under an older id but carrying the same hardware identity
// is retired when the box registers under its current id. Without this a
// renamed machine stays in the directory as a second device until the 30-day
// prune — the workspace showing "this machine" twice.
func TestUpsertReconcilesRenamedIdentity(t *testing.T) {
	db := openLedgerDB(t)
	seed := func(id, kind, identity, status string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO employee_cache (id, name, node_kind, node_identity, status, last_seen)
			VALUES (?, ?, ?, ?, ?, 1)`, id, id, kind, identity, status); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("old-name", "physical", "machine-abc", "offline")
	seed("other-box", "physical", "machine-def", "offline")
	seed("old-vm", "vm", "machine-abc", "offline")
	if _, err := db.Exec(`INSERT INTO hello_bindings (node_id, bound_at) VALUES ('old-name', 1)`); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	if err := Register(db, Card{Device: "new-name", NodeKind: "physical", NodeIdentity: "machine-abc"}, "new-name", 1); err != nil {
		t.Fatalf("register: %v", err)
	}

	ids := map[string]bool{}
	rows, err := db.Query(`SELECT id FROM employee_cache`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids[id] = true
	}
	if ids["old-name"] {
		t.Fatal("the renamed row should be retired, not kept beside the new one")
	}
	if !ids["new-name"] {
		t.Fatal("the freshly registered row must survive")
	}
	if !ids["other-box"] {
		t.Fatal("a different identity is a different box and must not be touched")
	}
	if !ids["old-vm"] {
		t.Fatal("the same identity under another kind is a guest, not a rename")
	}

	var bindings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hello_bindings WHERE node_id='old-name'`).Scan(&bindings); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if bindings != 0 {
		t.Fatal("the retired row's replay mark should go with it")
	}
}

// TestRegisterWithoutIdentityLeavesRowsAlone: a card that declares no identity
// has the id filled in for it, and an id is not a hardware key — it must never
// be used to delete another row.
func TestRegisterWithoutIdentityLeavesRowsAlone(t *testing.T) {
	db := openLedgerDB(t)
	if _, err := db.Exec(`INSERT INTO employee_cache (id, name, node_kind, node_identity, status, last_seen)
		VALUES ('peer-a', 'peer-a', 'physical', 'peer-a', 'offline', 1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Register(db, Card{Device: "peer-b"}, "peer-b", 1); err != nil {
		t.Fatalf("register: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM employee_cache`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("rows = %d, want both rows kept (no identity to reconcile on)", n)
	}
}
