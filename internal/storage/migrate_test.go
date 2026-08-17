package storage

import (
	"database/sql"
	"fmt"
	"testing"
)

func TestMigrateTracksUserVersion(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if want := migrations[len(migrations)-1].Version; version != want {
		t.Fatalf("user_version = %d, want %d", version, want)
	}
}

func TestMigrateFromV0(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	createV0Schema(t, db)

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate from v0: %v", err)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if want := migrations[len(migrations)-1].Version; version != want {
		t.Fatalf("user_version = %d, want %d", version, want)
	}

	for _, tc := range []struct{ table, col string }{
		{"tasks", "authorized"},
		{"tasks", "requires_json"},
		{"employee_cache", "resource_profile_json"},
	} {
		if !columnExists(t, db, tc.table, tc.col) {
			t.Fatalf("column %s.%s missing after migration", tc.table, tc.col)
		}
	}
}

func TestMigrateIdempotentFromV0(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	createV0Schema(t, db)

	for i := 0; i < 3; i++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("migrate %d: %v", i+1, err)
		}
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if want := migrations[len(migrations)-1].Version; version != want {
		t.Fatalf("user_version = %d, want %d", version, want)
	}
}

// createV0Schema simulates a pre-versioning Phase 0 database: the baseline
// tables exist but the columns added by v2-v4 are missing.
func createV0Schema(t *testing.T, db *sql.DB) {
	t.Helper()
	baseline := []string{
		`CREATE TABLE employee_cache (
			id TEXT PRIMARY KEY, name TEXT, department TEXT, chip TEXT,
			native_json TEXT, agents_json TEXT, manual_json TEXT,
			capacity_json TEXT, status TEXT, last_seen INTEGER, scheduler_tier INTEGER
		)`,
		`CREATE TABLE tasks (
			task_id TEXT PRIMARY KEY, parent_id TEXT, project TEXT,
			title TEXT, state TEXT NOT NULL, owner_node TEXT NOT NULL,
			attempt_id TEXT NOT NULL, state_version INTEGER NOT NULL DEFAULT 0,
			lease_expires_at INTEGER, chain_json TEXT, context_type TEXT,
			context_hash TEXT, intent TEXT, spec_json TEXT, result_json TEXT,
			complexity REAL, risk TEXT, resource_json TEXT, model_tier INT,
			created_at INTEGER, updated_at INTEGER
		)`,
	}
	for _, stmt := range baseline {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create v0 table: %v", err)
		}
	}
}

func TestMigrateFromV4IsNoOp(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 999`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate on v999: %v", err)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 999 {
		t.Fatalf("user_version = %d, want 999", version)
	}
}

func columnExists(t *testing.T, db *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == col {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	return false
}
