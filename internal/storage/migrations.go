package storage

import (
	"database/sql"
	"fmt"
)

// Migration is a single schema change identified by a monotonic version.
// Each migration runs inside its own transaction and, on success, advances
// PRAGMA user_version to its Version.
type Migration struct {
	Version int
	Name    string
	Apply   func(*sql.Tx) error
}

// migrations is the ordered list of schema changes from the Phase 0 baseline
// (v1) to the current version. The slice must be kept sorted by Version; the
// caller in migrate.go applies every migration whose Version is greater than
// the current PRAGMA user_version.
var migrations = []Migration{
	{Version: 1, Name: "phase0_baseline", Apply: migrateV1},
	{Version: 2, Name: "add_tasks_authorized", Apply: migrateV2},
	{Version: 3, Name: "add_tasks_requires_json", Apply: migrateV3},
	{Version: 4, Name: "add_employee_resource_profile_json", Apply: migrateV4},
	{Version: 5, Name: "add_delegation_metrics", Apply: migrateV5},
	{Version: 6, Name: "add_audit_hash_chain", Apply: migrateV6},
}

func migrateV1(tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS employee_cache (
			id TEXT PRIMARY KEY,
			name TEXT, department TEXT, chip TEXT,
			native_json TEXT, agents_json TEXT, manual_json TEXT,
			capacity_json TEXT,
			resource_profile_json TEXT,
			status TEXT, last_seen INTEGER,
			scheduler_tier INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			task_id TEXT PRIMARY KEY,
			parent_id TEXT,
			project TEXT,
			title TEXT,
			state TEXT NOT NULL,
			owner_node TEXT NOT NULL,
			attempt_id TEXT NOT NULL,
			state_version INTEGER NOT NULL DEFAULT 0,
			lease_expires_at INTEGER,
			chain_json TEXT,
			context_type TEXT,
			context_hash TEXT,
			intent TEXT,
			spec_json TEXT,
			result_json TEXT,
			complexity REAL,
			risk TEXT,
			resource_json TEXT,
			authorized INTEGER NOT NULL DEFAULT 0,
			requires_json TEXT,
			model_tier INT,
			created_at INTEGER,
			updated_at INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS task_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT,
			ts INTEGER,
			type TEXT,
			data_json TEXT,
			prev_hash TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_owner ON tasks(owner_node)`,
		`CREATE INDEX IF NOT EXISTS idx_events_task ON task_events(task_id)`,
		`CREATE TABLE IF NOT EXISTS context (
			ctx_hash TEXT PRIMARY KEY,
			ctx_type TEXT,
			data_blob BLOB,
			refs_json TEXT,
			created_at INTEGER,
			last_access INTEGER,
			access_count INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts INTEGER,
			who TEXT,
			what TEXT,
			target TEXT,
			result TEXT,
			detail TEXT,
			prev_hash TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts)`,
		`CREATE TABLE IF NOT EXISTS push_subscriptions (
			endpoint TEXT PRIMARY KEY,
			p256dh TEXT NOT NULL,
			auth TEXT NOT NULL,
			created_at INTEGER
		)`,
	}

	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec: %w\nstmt: %s", err, stmt)
		}
	}
	return nil
}

func migrateV2(tx *sql.Tx) error {
	return addColumnIfMissingTx(tx, "tasks", "authorized", "INTEGER NOT NULL DEFAULT 0")
}

func migrateV3(tx *sql.Tx) error {
	return addColumnIfMissingTx(tx, "tasks", "requires_json", "TEXT")
}

func migrateV4(tx *sql.Tx) error {
	return addColumnIfMissingTx(tx, "employee_cache", "resource_profile_json", "TEXT")
}

func migrateV5(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS delegation_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL,
		delegator TEXT NOT NULL,
		executor TEXT NOT NULL,
		abilities_json TEXT,
		success INTEGER NOT NULL,
		latency_ms INTEGER NOT NULL,
		tokens INTEGER,
		created_at INTEGER NOT NULL
	)`)
	return err
}

func migrateV6(tx *sql.Tx) error {
	if err := addColumnIfMissingTx(tx, "task_events", "prev_hash", "TEXT"); err != nil {
		return err
	}
	if err := addColumnIfMissingTx(tx, "audit_log", "prev_hash", "TEXT"); err != nil {
		return err
	}
	return nil
}

// addColumnIfMissingTx appends a column to a table if it is not already present,
// using PRAGMA table_info so the ALTER is a no-op on a fresh database. It is
// kept as the bridge for historical dev databases that already have the Phase 0
// schema but are missing columns added after the baseline.
func addColumnIfMissingTx(tx *sql.Tx, table, col, def string) error {
	rows, err := tx.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == col {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, col, def))
	return err
}
