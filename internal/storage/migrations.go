package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// Migration is a single schema change identified by a monotonic version.
// Each migration runs inside its own BEGIN IMMEDIATE transaction (see
// migrate.go) and, on success, advances PRAGMA user_version to its Version.
// The Apply body receives a MigrationExec (Exec/Query/QueryRow) rather than a
// *sql.Tx: the transaction is driven explicitly on the pooled connection.
type Migration struct {
	Version int
	Name    string
	Apply   func(MigrationExec) error
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
	{Version: 12, Name: "add_plan_stages_and_artifacts", Apply: migrateV12},
	{Version: 13, Name: "add_result_outbox", Apply: migrateV13},
	{Version: 14, Name: "add_cancel_outbox", Apply: migrateV14},
	{Version: 15, Name: "add_projects_and_settings", Apply: migrateV15},
	{Version: 16, Name: "add_delegation_metrics_cost", Apply: migrateV16},
	{Version: 17, Name: "add_task_approval_disposition", Apply: migrateV17},
	{Version: 18, Name: "add_task_outbox", Apply: migrateV18},
	{Version: 19, Name: "add_dtn_mesh_columns", Apply: migrateV19},
	{Version: 20, Name: "add_token_budget_and_link_metrics", Apply: migrateV20},
	{Version: 21, Name: "add_artifact_push_outbox", Apply: migrateV21},
	{Version: 22, Name: "add_task_outbox_via", Apply: migrateV22},
	{Version: 23, Name: "add_task_agent_session", Apply: migrateV23},
	{Version: 24, Name: "add_reminders_repeat", Apply: migrateV24},
	{Version: 25, Name: "add_dtn_relay_log", Apply: migrateV25},
	{Version: 26, Name: "add_projects_approval", Apply: migrateV26},
	{Version: 27, Name: "add_employee_contacts_json", Apply: migrateV27},
	{Version: 28, Name: "add_employee_pub_key", Apply: migrateV28},
	{Version: 29, Name: "add_tasks_auth_grant", Apply: migrateV29},
}

// migrateV28 adds employee_cache.pub_key: the peer's advertised Ed25519
// public key (hex), learned from signed hellos. It is the directory entry the
// mesh resolves when it must verify something that node signed — a tier-2
// consent grant on a delegate, or a stage artifact handoff grant — without
// trusting the payload's own claim about which key signed it.
func migrateV28(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "employee_cache", "pub_key", "TEXT NOT NULL DEFAULT ''")
}

// migrateV29 persists the tier-2 consent grant (auth_sig/auth_pub/auth_ts)
// on the task row. A relay re-dispatching an authorized task — queue
// forward, decline re-route — re-emits the origin's signed consent instead of
// degrading it to the bare Authorized flag, so the executor's verification
// survives every hop the consent legitimately travels.
func migrateV29(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	for _, col := range []struct{ name, ddl string }{
		{"auth_sig", "TEXT NOT NULL DEFAULT ''"},
		{"auth_pub", "TEXT NOT NULL DEFAULT ''"},
		{"auth_ts", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := addColumnIfMissingTx(tx, "tasks", col.name, col.ddl); err != nil {
			return err
		}
	}
	return nil
}

// migrateV27 adds employee_cache.contacts_json: the node's advertised DTN
// contact plan — scheduled transmission windows (open/close/rate/period)
// gossiped beside neighbors_json and links_json so custody routing can ask
// "which path delivers earliest" over scheduled links, not just "which
// online neighbor is cheapest" over live ones (whitepaper §8.4).
func migrateV27(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return addColumnIfMissingTx(tx, "employee_cache", "contacts_json", "TEXT NOT NULL DEFAULT ''")
}

// migrateV26 adds the per-project approval policy columns. approval_mode is a
// project-scoped override of the node's approval.mode gate; approval_scope
// picks where an approval card's "remember" writes its decision by default
// (once|session|project); approval_decision is the remembered project-level
// answer the tier-2 gate replays instead of re-prompting. All three stay
// empty on a project that never configured them — empty reads as "inherit the
// global policy and remember nothing".
func migrateV26(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "projects")
	if err != nil || !exists {
		return err
	}
	for _, c := range []struct{ name, def string }{
		{"approval_mode", "TEXT NOT NULL DEFAULT ''"},
		{"approval_scope", "TEXT NOT NULL DEFAULT ''"},
		{"approval_decision", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumnIfMissingTx(tx, "projects", c.name, c.def); err != nil {
			return err
		}
	}
	return nil
}

// migrateV23 adds tasks.agent_session_id and tasks.agent_session_node: the
// adapter's own conversation handle and the node that minted it (whitepaper
// §5.2's "breakpoint mooring" — the round boundary is the checkpoint the
// current adapters support). A task interrupted by yield/restart/redelegation
// resumes that session instead of cold-starting on the shadow copy alone.
// The node column is what keeps the handle honest: a session id only means
// something to the adapter store on the node that created it, so a delegator
// may only offer it back to that node (as resume_session_id on the wire) —
// never adopt it for itself or hand it to a different executor.
func migrateV23(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	if err := addColumnIfMissingTx(tx, "tasks", "agent_session_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return addColumnIfMissingTx(tx, "tasks", "agent_session_node", "TEXT NOT NULL DEFAULT ''")
}

// migrateV22 adds task_outbox.via: the peer a relayed bundle arrived from
// (whitepaper §8.3 multi-hop). A signed bundle cannot carry a hop list — a
// relay must not re-wrap it — so the no-echo rule is kept as row state: when
// a flush recomputes the next hop it excludes via, and a bundle can only
// ever be parked back toward its sender, never sent.
func migrateV22(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "task_outbox")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "task_outbox", "via", "TEXT NOT NULL DEFAULT ''")
}

// migrateV21 adds artifact_push_outbox: the durable custody record for
// chunked proactive artifact delivery (whitepaper §8.3 fat-push). The
// receiver reports contiguous progress (acked_through); the sender streams
// forward from that waterline and only retires a row on the receiver's done
// verdict — so an artifact outlives process restarts and link drops the same
// way a DTN bundle does, instead of restarting a large archive from zero.
// ttl is the shared absolute deadline (the task's deadline_unix when set,
// else mint+24h): a row that outlives it is swept like an expired bundle.
func migrateV21(tx MigrationExec) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS artifact_push_outbox (
		peer TEXT NOT NULL,
		hash TEXT NOT NULL,
		task_id TEXT NOT NULL,
		total INTEGER NOT NULL,
		sent_through INTEGER NOT NULL DEFAULT 0,
		acked_through INTEGER NOT NULL DEFAULT 0,
		ttl INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (peer, hash)
	)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_artifact_push_outbox_peer ON artifact_push_outbox(peer)`)
	return err
}

// migrateV20 completes the mesh-budget and weighted-routing persistence
// (whitepaper §4.1, §6.1):
//   - tasks.token_budget: the remaining LLM token quota a task may spend
//     across the mesh. >0 is the remaining allowance, 0 means unbounded
//     (every pre-v20 task and every task minted without a budget), and -1
//     marks the budget spent — the exhaustion marker has to be distinct
//     from unbounded or an exhausted task would re-mint quota at the next
//     hop.
//   - employee_cache.links_json: per-edge link metrics (peer → RTT ms)
//     gossiped in the capability summary beside neighbors_json, which is
//     what turns the graph's BFS shortest-hop search into a weighted
//     shortest-path one.
func migrateV20(tx MigrationExec) error {
	for _, c := range []struct{ table, column, decl string }{
		{"tasks", "token_budget", "INTEGER NOT NULL DEFAULT 0"},
		{"employee_cache", "links_json", "TEXT NOT NULL DEFAULT ''"},
	} {
		exists, err := tableExistsTx(tx, c.table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := addColumnIfMissingTx(tx, c.table, c.column, c.decl); err != nil {
			return err
		}
	}
	return nil
}

// migrateV19 gives the mesh/DTN machinery its durable fields (whitepaper
// §6.1, §8.2, §8.3, §4.1):
//   - tasks.transport / deadline_unix / delegation_budget: the DTN mode flag,
//     the absolute bundle TTL, and the remaining mesh-wide delegation budget
//     were previously wire-only/struct-only — a restart forgot them.
//   - task_outbox.payload_blob: the CBOR-encoded fat bundle, kept beside the
//     JSON payload so older peers still decode the row.
//   - employee_cache.neighbors_json: the link-state advertisement a node
//     gossips in hello — the peer ids it can currently reach — which is what
//     turns the directory from a star into a routable graph.
func migrateV19(tx MigrationExec) error {
	for _, c := range []struct{ table, column, decl string }{
		{"tasks", "transport", "TEXT NOT NULL DEFAULT 'live'"},
		{"tasks", "deadline_unix", "INTEGER NOT NULL DEFAULT 0"},
		{"tasks", "delegation_budget", "INTEGER NOT NULL DEFAULT 0"},
		{"task_outbox", "payload_blob", "BLOB"},
		{"employee_cache", "neighbors_json", "TEXT NOT NULL DEFAULT ''"},
	} {
		exists, err := tableExistsTx(tx, c.table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := addColumnIfMissingTx(tx, c.table, c.column, c.decl); err != nil {
			return err
		}
	}
	return nil
}

// migrateV18 adds task_outbox: universal relay outbox for DTN and store-and-forward tasks (whitepaper §8.2).
// Unlike result_outbox which only holds terminal results, task_outbox buffers forward
// delegation envelopes for non-live peers or opportunistic DTN relay.
func migrateV18(tx MigrationExec) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS task_outbox (
		peer TEXT NOT NULL,
		task_id TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		transport_type TEXT NOT NULL DEFAULT 'dtn',
		ttl INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (peer, task_id)
	)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_task_outbox_peer ON task_outbox(peer)`)
	return err
}

// migrateV17 persists why a task entered review. Approval behavior must survive
// restarts and cannot be reconstructed safely from free-form event text: only
// an explicit authorization refusal may be resumed, while completed work may
// be accepted and deterministic failures require changed input.
func migrateV17(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	if err := addColumnIfMissingTx(tx, "tasks", "approval_disposition", "TEXT"); err != nil {
		return err
	}
	return addColumnIfMissingTx(tx, "tasks", "operation_decision_json", "TEXT")
}

// migrateV16 adds cost to delegation_metrics for token-cost accounting.
func migrateV16(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "delegation_metrics")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "delegation_metrics", "cost", "REAL")
}

// migrateV15 adds projects and settings.
//
// A project used to be nothing but a Markdown file under projects/ — tasks
// carried a project *name* and the queue could filter on it, but there was
// nowhere to record what the project is, where its files live, or which one the
// user is currently working in. So every ask had to name it again, and a task
// delegated to another machine arrived with a name whose directory that machine
// had never heard of.
//
// projects.work_dir is what makes the project portable: it is the tree a task
// runs in, and therefore the tree that travels with a delegation (see the
// artifact plane). settings is a small key/value table for state that has to
// outlive a process without belonging to the config file — active_project first
// among them, since `panda ask` is one-shot and cannot hold it in memory.
func migrateV15(tx MigrationExec) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS projects (
		name TEXT PRIMARY KEY,
		work_dir TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`)
	return err
}

// migrateV14 adds cancel_outbox: the delivery guarantee for task_cancel
// messages. Like results, cancels cross the bus fire-and-forget; a cancel
// emitted while the executor is disconnected was dropped, and the executor
// kept burning tokens on work the delegator had given up on (its lease
// renewal kept the task legitimately alive). Parked cancels are re-delivered
// on the executor's next hello; the receiver is idempotent (a cancel on a
// terminal task is a no-op), so a resend is safe.
func migrateV14(tx MigrationExec) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS cancel_outbox (
		peer TEXT NOT NULL,
		task_id TEXT NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		PRIMARY KEY (peer, task_id)
	)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_cancel_outbox_peer ON cancel_outbox(peer)`)
	return err
}

// migrateV13 adds result_outbox: the delivery guarantee for terminal task
// results. A task_result is sent fire-and-forget over the bus; if the peer is
// disconnected at that instant the outcome was silently dropped and the two
// ends diverged forever (review P0-2). Terminal results that cannot be sent
// are persisted here keyed by (peer, task) and re-delivered the next time the
// peer's hello is accepted. The receiving side is idempotent (handleResult
// ignores results for tasks it no longer owns), so a resend is safe.
func migrateV13(tx MigrationExec) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS result_outbox (
		peer TEXT NOT NULL,
		task_id TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (peer, task_id)
	)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_result_outbox_peer ON result_outbox(peer)`)
	return err
}

// migrateV12 adds the plan plane and the data plane to the task table rather
// than beside it. A stage of a plan is an ordinary task — it inherits the CAS
// state machine, the task_events audit chain, the lease, retry and approval —
// so what a stage needs beyond a task is only: which plan it belongs to, which
// stage of it, what it waits for, and the artifacts it consumes and produces.
//
// artifacts is the local pool's index, not the bytes: the archives live under
// storage.artifact_path named by their hash (a trained model is measured in GB
// and has no business in SQLite). The row records size, manifest and the task
// that produced it, so the pool can be listed and pruned without unpacking
// every archive on disk.
func migrateV12(tx MigrationExec) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS artifacts (
		hash TEXT PRIMARY KEY,
		size INTEGER NOT NULL,
		task_id TEXT,
		created_at INTEGER NOT NULL,
		manifest_json TEXT
	)`); err != nil {
		return err
	}
	// The tasks-side half is guarded on the table existing, as migrateV10 is for
	// employee_cache: a partially-built database (a test fixture, or a dev
	// database restored from a subset of the baseline) must still migrate rather
	// than wedge the version at 11 forever.
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	for _, col := range []string{
		"plan_id",              // stages of one plan share it
		"stage_id",             // the stage's name within that plan
		"needs_json",           // the stage_ids this stage waits for
		"input_artifacts_json", // [{stage,hash,source}] this stage starts from
		"output_artifact",      // hash of the tree this stage produced
	} {
		if err := addColumnIfMissingTx(tx, "tasks", col, "TEXT"); err != nil {
			return err
		}
	}
	// The orchestrator's hot query is "the stages of this plan", asked every
	// time a stage finishes to decide what became ready.
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_plan ON tasks(plan_id, stage_id)`)
	return err
}

// migrateV11 adds entry_cache: the disk cache for entry-model decisions
// (intent classification and supervise verdicts). Rows are namespaced
// ("classify" | "supervise") and keyed by the SHA-256 of the prompt side and
// the device-snapshot / result side, so a changed input naturally misses. The
// entry package evicts rows older than its TTL; the created_at index keeps
// that delete cheap.
func migrateV11(tx MigrationExec) error {
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

func migrateV10(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	if err := addColumnIfMissingTx(tx, "employee_cache", "node_kind", "TEXT NOT NULL DEFAULT 'physical'"); err != nil {
		return err
	}
	return addColumnIfMissingTx(tx, "employee_cache", "node_identity", "TEXT")
}

func migrateV1(tx MigrationExec) error {
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

func migrateV2(tx MigrationExec) error {
	return addColumnIfMissingTx(tx, "tasks", "authorized", "INTEGER NOT NULL DEFAULT 0")
}

func migrateV3(tx MigrationExec) error {
	return addColumnIfMissingTx(tx, "tasks", "requires_json", "TEXT")
}

func migrateV4(tx MigrationExec) error {
	return addColumnIfMissingTx(tx, "employee_cache", "resource_profile_json", "TEXT")
}

func migrateV5(tx MigrationExec) error {
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

func migrateV6(tx MigrationExec) error {
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
func migrateV9(tx MigrationExec) error {
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
func tableExistsTx(tx MigrationExec, name string) (bool, error) {
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

func migrateV8(tx MigrationExec) error {
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

// migrateV24 adds repeat_seconds to reminders: 0 keeps the one-shot
// semantics; >0 reschedules the row on every claim instead of retiring it.
// A database created between v8's introduction and any wipe may predate the
// table entirely, so the ALTER only runs when it exists.
func migrateV24(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "reminders")
	if err != nil || !exists {
		return err
	}
	_, err = tx.Exec(`ALTER TABLE reminders ADD COLUMN repeat_seconds INTEGER NOT NULL DEFAULT 0`)
	return err
}

// migrateV25 adds dtn_relay_log: the durable form of the per-node loop bound
// for store-and-forward custody (whitepaper §8.3). A signed bundle cannot
// carry a hop list, so each relay counts its own forwards of a bundle id —
// and that count must survive a restart, or a rebooted node re-arms the bound
// and a triangle of restarts can ping-pong a bundle until its TTL. until is
// the bundle's deadline (defaulted when the origin set none): the row expires
// with the bundle it bounds.
func migrateV25(tx MigrationExec) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS dtn_relay_log (
		bundle_id TEXT PRIMARY KEY,
		hops INTEGER NOT NULL DEFAULT 0,
		until INTEGER NOT NULL DEFAULT 0
	)`)
	return err
}

// migrateV7 backfills empty/NULL prev_hash values for rows that predate the hash
// chain (V6). This makes existing audit and event chains verifiable instead of
// failing on every NULL scan. The chain content is not altered — only the link
// that was missing when the column was added is recomputed from the existing
// rows in their natural order.
func migrateV7(tx MigrationExec) error {
	if err := backfillAuditChain(tx); err != nil {
		return fmt.Errorf("backfill audit chain: %w", err)
	}
	if err := backfillTaskEventChain(tx); err != nil {
		return fmt.Errorf("backfill task event chain: %w", err)
	}
	return nil
}

func backfillAuditChain(tx MigrationExec) error {
	// Every scanned column is COALESCE'd: the schema declares all of them plain
	// TEXT (nullable), and a NULL anywhere — a hand-edited row, an import —
	// fails the whole scan with "converting NULL to string", which aborts the
	// migration and leaves the store unopenable. Hashing treats NULL as the
	// empty string, matching how the writers' empty fields hash.
	rows, err := tx.Query(`SELECT id, COALESCE(prev_hash, ''), ts,
		COALESCE(who, ''), COALESCE(what, ''), COALESCE(target, ''),
		COALESCE(result, ''), COALESCE(detail, '')
		FROM audit_log ORDER BY id ASC`)
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

func backfillTaskEventChain(tx MigrationExec) error {
	// Same NULL-safety as backfillAuditChain: every column is nullable TEXT.
	rows, err := tx.Query(`SELECT id, COALESCE(task_id, ''), COALESCE(prev_hash, ''), ts,
		COALESCE(type, ''), COALESCE(data_json, '')
		FROM task_events ORDER BY task_id, id ASC`)
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
func addColumnIfMissingTx(tx MigrationExec, table, col, def string) error {
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
