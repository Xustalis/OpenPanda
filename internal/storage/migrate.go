package storage

import (
	"database/sql"
	"fmt"
)

// Migrate creates the schema if it does not exist. Schema versioning is
// applied via the user_version pragma; version 1 is the Phase 0 baseline.
func Migrate(db *sql.DB) error {
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
			data_json TEXT
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
			detail TEXT
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
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w\nstmt: %s", err, stmt)
		}
	}
	// Idempotent column additions for databases created before the column was
	// introduced. CREATE TABLE IF NOT EXISTS does not alter an existing table,
	// so a dev's data/panda.db from an earlier build must gain the column here.
	if err := addColumnIfMissing(db, "tasks", "authorized", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := addColumnIfMissing(db, "tasks", "requires_json", "TEXT"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := addColumnIfMissing(db, "employee_cache", "resource_profile_json", "TEXT"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// addColumnIfMissing appends a column to a table if it is not already present,
// using PRAGMA table_info so the ALTER is a no-op on a fresh database.
func addColumnIfMissing(db *sql.DB, table, col, def string) error {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
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
	_, err = db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, col, def))
	return err
}
