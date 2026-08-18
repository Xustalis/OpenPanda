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

func TestMigrateBackfillsHashChain(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Create a V6-era schema: tables exist, prev_hash columns exist, but rows were
	// written with NULL prev_hash before the backfill logic was available.
	baseline := []string{
		`CREATE TABLE audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER, who TEXT,
			what TEXT, target TEXT, result TEXT, detail TEXT, prev_hash TEXT
		)`,
		`CREATE TABLE task_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT, ts INTEGER,
			type TEXT, data_json TEXT, prev_hash TEXT
		)`,
	}
	for _, stmt := range baseline {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 6`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	// Insert two audit rows with NULL prev_hash.
	if _, err := db.Exec(`INSERT INTO audit_log (ts, who, what, target, result, detail, prev_hash)
		VALUES (1, 'a', 'x', 't1', 'ok', 'd1', NULL)`); err != nil {
		t.Fatalf("insert audit 1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO audit_log (ts, who, what, target, result, detail, prev_hash)
		VALUES (2, 'a', 'y', 't2', 'ok', 'd2', NULL)`); err != nil {
		t.Fatalf("insert audit 2: %v", err)
	}

	// Insert events for two tasks: first event NULL, second event NULL.
	if _, err := db.Exec(`INSERT INTO task_events (task_id, ts, type, data_json, prev_hash)
		VALUES ('task-a', 1, 'submit', '{}', NULL)`); err != nil {
		t.Fatalf("insert event 1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_events (task_id, ts, type, data_json, prev_hash)
		VALUES ('task-a', 2, 'result', '{}', NULL)`); err != nil {
		t.Fatalf("insert event 2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_events (task_id, ts, type, data_json, prev_hash)
		VALUES ('task-b', 3, 'submit', '{}', NULL)`); err != nil {
		t.Fatalf("insert event 3: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate from v6: %v", err)
	}

	// Verify the audit chain is intact.
	rows, err := db.Query(`SELECT prev_hash, ts, who, what, target, result, detail FROM audit_log ORDER BY id ASC`)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	var prevHash string
	for rows.Next() {
		var h string
		var ts int64
		var who, what, target, result, detail string
		if err := rows.Scan(&h, &ts, &who, &what, &target, &result, &detail); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		if h != prevHash {
			t.Fatalf("audit prev_hash mismatch: got %q, want %q", h, prevHash)
		}
		prevHash = hashAudit(h, ts, who, what, target, result, detail)
	}

	// Verify task-a's event chain: first event empty, second equals hash of first.
	var first, second string
	if err := db.QueryRow(`SELECT prev_hash FROM task_events WHERE task_id = 'task-a' ORDER BY id ASC LIMIT 1`).Scan(&first); err != nil {
		t.Fatalf("scan task-a first: %v", err)
	}
	if first != "" {
		t.Fatalf("task-a genesis prev_hash = %q, want empty", first)
	}
	if err := db.QueryRow(`SELECT prev_hash FROM task_events WHERE task_id = 'task-a' ORDER BY id ASC LIMIT 1 OFFSET 1`).Scan(&second); err != nil {
		t.Fatalf("scan task-a second: %v", err)
	}
	wantSecond := hashEvent("", "task-a", 1, "submit", "{}")
	if second != wantSecond {
		t.Fatalf("task-a second prev_hash = %q, want %q", second, wantSecond)
	}

	// Verify task-b's genesis event is empty.
	var b string
	if err := db.QueryRow(`SELECT prev_hash FROM task_events WHERE task_id = 'task-b'`).Scan(&b); err != nil {
		t.Fatalf("scan task-b: %v", err)
	}
	if b != "" {
		t.Fatalf("task-b genesis prev_hash = %q, want empty", b)
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
