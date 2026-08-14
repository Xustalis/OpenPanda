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
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w\nstmt: %s", err, stmt)
		}
	}
	return nil
}
