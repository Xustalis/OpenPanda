package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	{Version: 7, Name: "backfill_audit_hash_chain", Apply: migrateV7},
	{Version: 8, Name: "add_reminders", Apply: migrateV8},
	{Version: 9, Name: "add_tasks_queue_meta", Apply: migrateV9},
	{Version: 10, Name: "add_node_identity", Apply: migrateV10},
	{Version: 11, Name: "add_entry_cache", Apply: migrateV11},
}

// migrateV11 adds entry_cache: the disk cache for entry-model decisions
// (intent classification and supervise verdicts). Rows are namespaced
// ("classify" | "supervise") and keyed by the SHA-256 of the prompt side and
// the device-snapshot / result side, so a changed input naturally misses. The
// entry package evicts rows older than its TTL; the created_at index keeps
// that delete cheap.
func migrateV11(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS entry_cache (
		ns TEXT NOT NULL,
		k1 TEXT NOT NULL,
		k2 TEXT NOT NULL,
		output_json TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (ns, k1, k2)
	)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_entry_cache_created ON entry_cache(created_at)`)
	return err
}

func migrateV10(tx *sql.Tx) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	if err := addColumnIfMissingTx(tx, "employee_cache", "node_kind", "TEXT NOT NULL DEFAULT 'physical'"); err != nil {
		return err
	}
	return addColumnIfMissingTx(tx, "employee_cache", "node_identity", "TEXT")
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

// migrateV9 adds the task-queue scheduling metadata (panel queue redesign):
// priority/seq drive the board ordering, session_id links a task to its panel
// conversation, resource_keys_json declares the resources the task occupies
// (conflict detection for parallel scheduling), work_dir pins execution to a
// session worktree, and scheduled marks tasks owned by the local queue
// scheduler (as opposed to delegation re-routing). A store without the tasks
// table (event/audit-only legacy DBs) skips the whole step.
func migrateV9(tx *sql.Tx) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	for _, col := range []struct{ name, def string }{
		{"priority", "INTEGER NOT NULL DEFAULT 1"},
		{"seq", "INTEGER NOT NULL DEFAULT 0"},
		{"session_id", "TEXT"},
		{"resource_keys_json", "TEXT"},
		{"work_dir", "TEXT"},
		{"scheduled", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := addColumnIfMissingTx(tx, "tasks", col.name, col.def); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_queue ON tasks(state, scheduled)`)
	return err
}

// tableExistsTx reports whether name is an existing table.
func tableExistsTx(tx *sql.Tx, name string) (bool, error) {
	var one int
	err := tx.QueryRow(`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func migrateV8(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS reminders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		message TEXT NOT NULL,
		due_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		fired_at INTEGER,
		source TEXT NOT NULL DEFAULT 'cli'
	)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_reminders_due ON reminders(fired_at, due_at)`)
	return err
}

// migrateV7 backfills empty/NULL prev_hash values for rows that predate the hash
// chain (V6). This makes existing audit and event chains verifiable instead of
// failing on every NULL scan. The chain content is not altered — only the link
// that was missing when the column was added is recomputed from the existing
// rows in their natural order.
func migrateV7(tx *sql.Tx) error {
	if err := backfillAuditChain(tx); err != nil {
		return fmt.Errorf("backfill audit chain: %w", err)
	}
	if err := backfillTaskEventChain(tx); err != nil {
		return fmt.Errorf("backfill task event chain: %w", err)
	}
	return nil
}

func backfillAuditChain(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, COALESCE(prev_hash, ''), ts, who, what, target, result, detail FROM audit_log ORDER BY id ASC`)
	if err != nil {
		return err
	}
	type row struct {
		id       int64
		prevHash string
		ts       int64
		who      string
		what     string
		target   string
		result   string
		detail   string
	}
	var chain []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.prevHash, &r.ts, &r.who, &r.what, &r.target, &r.result, &r.detail); err != nil {
			rows.Close()
			return err
		}
		chain = append(chain, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var prevHash string
	for _, r := range chain {
		if r.prevHash == "" {
			if _, err := tx.Exec(`UPDATE audit_log SET prev_hash = ? WHERE id = ?`, prevHash, r.id); err != nil {
				return err
			}
		}
		prevHash = hashAudit(prevHash, r.ts, r.who, r.what, r.target, r.result, r.detail)
	}
	return nil
}

func backfillTaskEventChain(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, task_id, COALESCE(prev_hash, ''), ts, type, data_json FROM task_events ORDER BY task_id, id ASC`)
	if err != nil {
		return err
	}
	type row struct {
		id       int64
		taskID   string
		prevHash string
		ts       int64
		typ      string
		dataJSON string
	}
	var chain []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.taskID, &r.prevHash, &r.ts, &r.typ, &r.dataJSON); err != nil {
			rows.Close()
			return err
		}
		chain = append(chain, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	prevTask := ""
	var prevHash string
	for _, r := range chain {
		if r.taskID != prevTask {
			prevHash = ""
			prevTask = r.taskID
		}
		if r.prevHash == "" {
			if _, err := tx.Exec(`UPDATE task_events SET prev_hash = ? WHERE id = ?`, prevHash, r.id); err != nil {
				return err
			}
		}
		prevHash = hashEvent(prevHash, r.taskID, r.ts, r.typ, r.dataJSON)
	}
	return nil
}

func hashAudit(prevHash string, ts int64, who, what, target, result, detail string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%s|%s|%s|%s|%s", prevHash, ts, who, what, target, result, detail)
	return hex.EncodeToString(h.Sum(nil))
}

func hashEvent(prevHash, taskID string, ts int64, typ, dataJSON string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%s|%s|%s", prevHash, ts, taskID, typ, dataJSON)
	return hex.EncodeToString(h.Sum(nil))
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
