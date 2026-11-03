// SPDX-License-Identifier: AGPL-3.0-or-later

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
	{Version: 30, Name: "add_employee_projects_json", Apply: migrateV30},
	{Version: 31, Name: "add_employee_key_verified", Apply: migrateV31},
	{Version: 32, Name: "add_pending_nodes", Apply: migrateV32},
	{Version: 33, Name: "add_tasks_remote", Apply: migrateV33},
	{Version: 34, Name: "add_employee_ver", Apply: migrateV34},
	{Version: 35, Name: "add_task_events_sig", Apply: migrateV35},
	{Version: 36, Name: "rekey_outboxes_stable_id", Apply: migrateV36},
	{Version: 37, Name: "add_audit_log_sig", Apply: migrateV37},
	{Version: 38, Name: "add_resume_outbox", Apply: migrateV38},
	{Version: 39, Name: "add_pending_verified", Apply: migrateV39},
	{Version: 40, Name: "add_pair_sessions", Apply: migrateV40},
	{Version: 41, Name: "add_peer_addrs", Apply: migrateV41},
	{Version: 42, Name: "add_tasks_exec_work_dir", Apply: migrateV42},
	{Version: 43, Name: "add_employee_trust", Apply: migrateV43},
	{Version: 44, Name: "add_task_messages", Apply: migrateV44},
	{Version: 45, Name: "add_tasks_screen_output", Apply: migrateV45},
	{Version: 46, Name: "add_task_agent_session_adapter", Apply: migrateV46},
	{Version: 47, Name: "add_exec_locks", Apply: migrateV47},
	{Version: 48, Name: "add_exec_locks_gc", Apply: migrateV48},
	{Version: 49, Name: "add_exec_locks_run_id", Apply: migrateV49},
	{Version: 50, Name: "add_tasks_spawn_depth", Apply: migrateV50},
	{Version: 51, Name: "add_tasks_spawn_cap", Apply: migrateV51},
	{Version: 52, Name: "add_tasks_scope_paths", Apply: migrateV52},
	{Version: 53, Name: "add_tasks_isolate", Apply: migrateV53},
	// v50 collided during the merge: this branch had already shipped
	// add_employee_machine_class as 50 when upstream claimed 50-53 for the
	// parallel-orchestration columns, so the machine facts move behind them.
	// A store that ran the colliding build sits at user_version 50 with the
	// machine columns present but spawn_depth missing — migrateV54 repairs
	// that row too, since 50 will never run again on it.
	{Version: 54, Name: "add_employee_machine_class", Apply: migrateV54},
	{Version: 55, Name: "add_cancel_outbox_attempt", Apply: migrateV55},
	{Version: 56, Name: "add_employee_transit_dir", Apply: migrateV56},
	{Version: 57, Name: "add_employee_prov", Apply: migrateV57},
	{Version: 58, Name: "add_task_messages_consumed_at", Apply: migrateV58},
	{Version: 59, Name: "add_employee_dir_row", Apply: migrateV59},
	{Version: 60, Name: "add_task_messages_attachments", Apply: migrateV60},
	// The to-do board landed while upstream claimed 56-60 for the cross-network
	// mesh columns, so it moves behind them (the same collision v50 had).
	{Version: 61, Name: "add_todos", Apply: migrateV61},
	{Version: 62, Name: "add_employee_dir_signer", Apply: migrateV62},
	{Version: 63, Name: "add_hello_bindings", Apply: migrateV63},
	// The delivery contract's verification tables arrived while upstream was
	// claiming 63 for hello bindings, so they move behind it.
	{Version: 64, Name: "add_task_checks_and_verification", Apply: migrateV64},
	{Version: 65, Name: "index_tasks_by_session", Apply: migrateV65},
	{Version: 66, Name: "add_intake_seen", Apply: migrateV66},
}

// migrateV62 adds employee_cache.dir_signer: the Ed25519 key that SIGNED a
// gossiped row, as distinct from pub_key (the key the row's id is pinned to).
//
// The share accounting needs it. dirRowBudget counts how much of the directory
// one sender occupies, and it looked at pub_key — but pub_key is only written
// for a self-certifying stable id or an already-pinned row, so a member that
// signed rows for arbitrary fresh ids contributed nothing to its own share.
// The cap then never bit, and one member could fill the whole table with
// phantom rows until an HONEST new peer had no room left to publish its own
// row at all.
//
// dir_signer is the row's own signed claim: the signature covers it, so it is
// proof of who signed the row, not a self-asserted identity.
func migrateV62(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	if err := addColumnIfMissingTx(tx, "employee_cache",
		"dir_signer", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_employee_dir_signer ON employee_cache(dir_signer)`)
	return err
}

// migrateV63 adds hello_bindings: the node ids that have sent a hello signed
// for the receiver it names. A hello's legacy signature covers only the
// sender, so a captured one replays at any node inside the freshness window; a
// bound hello names its receiver and fails anywhere else. This table is what
// stops the replay from simply being downgraded — a node known to bind, whose
// hello arrives unbound, is refused rather than falling back to the legacy
// path. Rows are written only after a bound hello authenticates, so a peer
// running a build without binding never gets one and keeps working.
func migrateV63(tx MigrationExec) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS hello_bindings (
		node_id TEXT PRIMARY KEY,
		bound_at INTEGER NOT NULL
	)`)
	return err
}

// migrateV58 adds task_messages.consumed_at: the moment the recipient's round
// loop folded that slot into its prompt (0 = never). recv_at cannot carry this
// signal — PutTaskMessage stamps it on arrival, so every row reads "received"
// the moment it is posted, and a surface could never tell "waiting for the next
// round" from "already read". The read is written back by the consumer
// (Core.takeSteer / Core.drainTaskMessages), which is why it belongs on the row
// rather than in an in-memory seen-set: a console rendering the board lives in
// another process, or another device, and both need the same answer.
func migrateV58(tx MigrationExec) error {
	_, err := tx.Exec(`ALTER TABLE task_messages
		ADD COLUMN consumed_at INTEGER NOT NULL DEFAULT 0`)
	return err
}

// migrateV59 adds the gossip-layer columns directory sync (§2) serves from:
// dir_row_json is the verbatim canonical row JSON as it arrived (or as we
// minted it for ourselves), dir_sig its Ed25519 signature extracted for the
// signed-beats-unsigned conflict compare. Serving the stored bytes keeps a
// forwarded row's signature verifiable byte-for-byte no matter how the live
// columns drifted since — the row is a credential, not a view.
func migrateV59(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	for _, col := range []struct{ name, def string }{
		{"dir_row_json", "TEXT NOT NULL DEFAULT ''"},
		{"dir_sig", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumnIfMissingTx(tx, "employee_cache", col.name, col.def); err != nil {
			return err
		}
	}
	return nil
}

// migrateV44 adds task_messages: the per-scope message board sibling tasks
// share (design: inter-task message layer). Slots are keyed (scope, sender,
// rcpt) — a sender's newest message to a recipient overwrites its previous
// slot, so a board stays bounded at one row per member no matter how chatty
// a single task gets, and a reader always sees every sender's latest word
// rather than whatever a FIFO happened to evict. sender_seq is the sender's
// own monotone counter, which lets a consumer diff "what changed since I last
// looked" without a cursor table. sender_label is the owner's resolved display
// name for the sender (its stage id, or title) carried along the fan-out, so
// a replica that never held the sender's row can still render and address it.
func migrateV44(tx MigrationExec) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS task_messages (
		scope        TEXT NOT NULL,
		sender       TEXT NOT NULL,
		rcpt         TEXT NOT NULL DEFAULT '',
		sender_seq   INTEGER NOT NULL,
		sender_label TEXT NOT NULL DEFAULT '',
		grade        TEXT NOT NULL,
		body         TEXT NOT NULL,
		sent_at      INTEGER NOT NULL,
		recv_at      INTEGER NOT NULL,
		expires_at   INTEGER NOT NULL,
		PRIMARY KEY (scope, sender, rcpt)
	)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_task_messages_scope ON task_messages(scope)`)
	return err
}

// migrateV43 adds employee_cache.trust: the operator-asserted trust-zone tier
// (mesh|guarded|open; "" = mesh) pinned to a directory row. It lives next to
// key_verified deliberately: both are human judgments about an identity, and
// both are revoked the same way — a key change under the row clears the
// stamp in recordPeerPubKey, which also demotes mesh trust to guarded.
func migrateV43(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "employee_cache", "trust", "TEXT NOT NULL DEFAULT ''")
}

// migrateV42 adds tasks.exec_work_dir: the workspace a task actually executed
// in, persisted when the run starts. The row's work_dir column is a PIN (a
// session worktree the submitter chose) and stays empty for ordinary tasks,
// whose effective directory (node work dir, stage dir, project dir) is only
// derived at run time — so "where did this run?" had no answer on the board.
func migrateV42(tx MigrationExec) error {
	// A pre-v7 database has no tasks table yet (a later migration creates it
	// with the column already present); guard like v39 does.
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "tasks", "exec_work_dir", "TEXT NOT NULL DEFAULT ''")
}

// migrateV41 adds peer_addrs: the daemon-persisted binding from a configured
// dial address to the node id its hello bound. `panda status` runs in its own
// process and can only see the directory, so without this it cannot say
// whether the peers THIS node dials are up — the mesh line counted live
// directory rows instead, and an inbound-only stranger could make a dead
// configured peer read as online.
func migrateV41(tx MigrationExec) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS peer_addrs (
		addr TEXT PRIMARY KEY,
		node_id TEXT NOT NULL,
		last_seen INTEGER NOT NULL
	)`)
	return err
}

// migrateV40 adds pair_sessions: the responder-side row of a Bluetooth-style
// pairing exchange. The daemon writes it when a pair_hello lands (ephemeral
// keys stay in memory — the row is the IPC between the daemon holding the
// session and the operator's `panda pair confirm` on the same machine); the
// operator flips state ready→confirmed/rejected, and the daemon's session
// goroutine watches that flip to send or refuse the sealed secret. Direction
// is always 'in' today — the initiator side is a synchronous CLI flow with
// nothing to persist.
func migrateV40(tx MigrationExec) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS pair_sessions (
		id TEXT PRIMARY KEY,
		peer_addr TEXT NOT NULL DEFAULT '',
		peer_name TEXT NOT NULL DEFAULT '',
		peer_pub TEXT NOT NULL DEFAULT '',
		sas TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'ready',
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	)`)
	return err
}

// migrateV39 adds pending_nodes.verified: whether the discovery beacon's
// Ed25519 signature proved the advertised fingerprint belongs to the
// advertiser. Unsigned (v1-format) beacons stay listable — they are the
// backward-compatible form — but they never earn the mark, and the pending
// cap evicts them before signed rows.
func migrateV39(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "pending_nodes")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "pending_nodes", "verified", "INTEGER NOT NULL DEFAULT 0")
}

// migrateV38 adds resume_outbox: the delivery guarantee for task_resume
// approvals. A resume sent while the executor's link was down used to fail
// the task outright — a user's explicit approval killed by a transient
// flap, the same class of divergence the task/result/cancel outboxes exist
// to close. Parked approvals are re-delivered on the executor's next hello,
// bounded by ttl (the lease window at persist time): a peer that never
// returns expires the row and the local copy together instead of diverging.
// The receiver is idempotent — a resume on a task no longer in review is
// answered with a failed result rather than re-run — so a resend is safe.
func migrateV38(tx MigrationExec) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS resume_outbox (
		peer TEXT NOT NULL,
		task_id TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		ttl INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (peer, task_id)
	)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_resume_outbox_peer ON resume_outbox(peer)`)
	return err
}

// migrateV37 adds audit_log.sig / sig_pub (P2-9): the Ed25519 signature of
// the entry's chain hash and the public key that made it. The audit chain
// used to be unkeyed SHA-256 — anyone with DB write access could rewrite an
// entry and re-hash the chain end to end. A signed entry cannot be forged
// without the node's private key. Empty on pre-migration rows: unsigned
// means unsigned, and the verifier only demands signatures once the chain
// has begun signing.
func migrateV37(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "audit_log")
	if err != nil || !exists {
		return err
	}
	if err := addColumnIfMissingTx(tx, "audit_log", "sig", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return addColumnIfMissingTx(tx, "audit_log", "sig_pub", "TEXT NOT NULL DEFAULT ''")
}

// migrateV36 re-keys every outbox destination from the peer's instance id to
// its stable identity ("k:" + Ed25519 pub) wherever the directory already
// holds a proven key (Batch-6 restart continuity, confirmed-issues §14). Rows
// whose peer is unknown or key-less keep their instance id — for those peers
// the instance id IS the stable key, and they flush exactly as before. The
// rewrite is INSERT OR REPLACE + DELETE rather than a blind UPDATE: a node
// that helloed under two instance ids can already hold rows under both, and
// collapsing them to one stable key must merge, not fail, on the (peer,
// task_id) primary key.
func migrateV36(tx MigrationExec) error {
	// Databases migrating forward from before employee_cache existed have no
	// identity records to re-key by — every row keeps its instance key, which
	// is exactly what "no stable identity known" means anyway.
	dirExists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !dirExists {
		return err
	}
	stmts := []struct{ table, cols, vals string }{
		{"result_outbox",
			"peer, task_id, payload_json, created_at",
			"'k:' || e.pub_key, o.task_id, o.payload_json, o.created_at"},
		{"cancel_outbox",
			"peer, task_id, reason, created_at",
			"'k:' || e.pub_key, o.task_id, o.reason, o.created_at"},
		{"task_outbox",
			"peer, task_id, payload_json, transport_type, ttl, created_at, payload_blob, via",
			"'k:' || e.pub_key, o.task_id, o.payload_json, o.transport_type, o.ttl, o.created_at, o.payload_blob, o.via"},
		{"artifact_push_outbox",
			"peer, hash, task_id, total, sent_through, acked_through, ttl, created_at",
			"'k:' || e.pub_key, o.hash, o.task_id, o.total, o.sent_through, o.acked_through, o.ttl, o.created_at"},
	}
	for _, s := range stmts {
		exists, err := tableExistsTx(tx, s.table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		// ORDER BY makes the merge winner deterministic: two instance rows
		// collapsing onto one stable (peer, task_id) key apply oldest-first,
		// so INSERT OR REPLACE leaves the newest payload standing rather than
		// whichever row SQLite happened to visit last.
		if _, err := tx.Exec(fmt.Sprintf(
			`INSERT OR REPLACE INTO %s (%s)
			 SELECT %s FROM %s o
			 JOIN employee_cache e ON e.id = o.peer AND e.pub_key != ''
			 ORDER BY o.created_at ASC, o.rowid ASC`,
			s.table, s.cols, s.vals, s.table)); err != nil {
			return err
		}
		if _, err := tx.Exec(fmt.Sprintf(
			`DELETE FROM %s WHERE peer IN (
				 SELECT id FROM employee_cache WHERE pub_key != '')`,
			s.table)); err != nil {
			return err
		}
	}
	return nil
}

// migrateV35 adds task_events.sig / sig_pub (P2-9): the Ed25519 signature of
// the event's chain hash and the public key that made it. An unkeyed SHA-256
// chain can be rewritten wholesale by anyone with DB write access and
// re-hashed end to end — a signed row cannot be forged without the node's
// private key, so verify gains "this node's key attested this row" on top of
// "the rows link up". Empty on pre-migration rows: unsigned means unsigned.
func migrateV35(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "task_events")
	if err != nil || !exists {
		return err
	}
	if err := addColumnIfMissingTx(tx, "task_events", "sig", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return addColumnIfMissingTx(tx, "task_events", "sig_pub", "TEXT NOT NULL DEFAULT ''")
}

// migrateV34 adds employee_cache.ver: the software version a peer advertised
// in its hello/heartbeat. Fleet observability (v0.0.10 Track 3) renders the
// version skew so an operator can see a stale node at a glance instead of
// debugging a protocol mismatch frame by frame. Empty on rows that predate
// the column until the next hello re-stamps it.
func migrateV34(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "employee_cache", "ver", "TEXT NOT NULL DEFAULT ''")
}

// migrateV33 adds tasks.remote: set at intake when the row was created by a
// delegate handler (the intent text came off the wire) rather than by local
// submit. Provenance must be persisted, not derived from chain[0] — the chain
// is peer-supplied, and a peer can write chain=[victim, self] to make its
// task look locally authored. The executor uses this flag to hold remote,
// unconsented agent runs to the restricted tool face.
func migrateV33(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "tasks", "remote", "INTEGER NOT NULL DEFAULT 0")
}

// migrateV32 adds pending_nodes: the LAN discovery hint list. A UDP beacon
// is unauthenticated by design — it may say "a node lives at this address"
// and nothing more — so its rows sit here, NOT in employee_cache, until the
// operator pairs the address (shared secret + signed hello). Rows expire:
// a node that stopped broadcasting should fall off the list instead of
// accumulating ghosts.
func migrateV32(tx MigrationExec) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS pending_nodes (
		id TEXT PRIMARY KEY,
		addr TEXT NOT NULL DEFAULT '',
		pub_key TEXT NOT NULL DEFAULT '',
		ver TEXT NOT NULL DEFAULT '',
		first_seen INTEGER NOT NULL DEFAULT 0,
		last_seen INTEGER NOT NULL DEFAULT 0)`)
	return err
}

// migrateV31 adds employee_cache.key_verified: the unix timestamp a human
// confirmed this node's advertised fingerprint (panda nodes verify <id>),
// which is what turns a TOFU-recorded Ed25519 key into a checked one. A key
// that changes under a still-verified row clears the stamp — the new key has
// never been compared, and pretending otherwise would make the check
// decorative.
func migrateV31(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "employee_cache", "key_verified", "INTEGER NOT NULL DEFAULT 0")
}

// migrateV30 adds employee_cache.projects_json: the names of the projects a
// node holds a checkout of, gossiped on card and heartbeat so routing can
// prefer a resident node for a project task. Residence is an optimization the
// scorer weighs — the tree can still travel as an artifact — but landing the
// task where the code already lives skips the round trip entirely.
func migrateV30(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "employee_cache", "projects_json", "TEXT NOT NULL DEFAULT ''")
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

// migrateV45 adds tasks.screen_output: the runtime mark an executor stamps
// when the run resolved a screen_sensing actuator — and that result
// adoption stamps on every delegator hop — so the artifact transfer gate
// can recognize display content even for operator-defined actuator ids the
// static requires/spec predicates cannot name. Peer-written, but only ever
// making a task MORE restricted — a forged flag can shrink the audience,
// never widen it.
func migrateV45(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "tasks", "screen_output", "INTEGER NOT NULL DEFAULT 0")
}

// migrateV46 adds tasks.agent_session_adapter: which adapter file minted the
// session handle in agent_session_id. V23 already pins the handle to its node;
// the adapter column is the second half of that affinity — an agent fallback
// or a retry that lands on a different adapter must not resume a foreign
// handle (opencode rejects non-"ses" ids with a 400, so a claude_uuid-shaped
// handle burned a whole cold-start run on every retry before the task died).
func migrateV46(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "tasks", "agent_session_adapter", "TEXT NOT NULL DEFAULT ''")
}

// migrateV47 adds exec_locks: the cross-process execution lock table. The
// queue's resource registry orders starts inside ONE process; every execution
// path — queued runs, inbound delegates, inline submits, resumes — converges
// on claimForRun, and that is where this table is taken, inside the same
// BEGIN IMMEDIATE transaction every writer uses. That is what makes "same
// project worktree admits one writer" and "one driver per physical actuator"
// hold across every core process sharing the database (a daemon plus an
// embedded REPL/panel core on one host), not just inside one. Rows are held
// while a task runs and deleted on completion; a crashed holder's rows are
// recognized as stale (its task terminal, in review, or its lease lapsed) and
// stolen by the next acquirer, so a dead process can never wedge a resource.
func migrateV47(tx MigrationExec) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS exec_locks (
		key TEXT PRIMARY KEY,
		task_id TEXT NOT NULL,
		node_id TEXT NOT NULL DEFAULT '',
		acquired_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_exec_locks_task ON exec_locks(task_id)`)
	return err
}

// migrateV48 adds the exec_locks garbage-collection trigger: whenever a task
// row leaves the executing states, its exec_locks rows die in the same
// transaction, on EVERY transition path — guarded transitions, bulk recovery
// sweeps, requeues, cancels — because they all run through UPDATE OF state.
// Without it, a lock row can outlive its run (a failed execLockUnlock write,
// or a crash in the acquire→accept window) and sit under a dispatched row
// that reads "live" to the next acquirer: a retried task would then wait on
// its own leftovers until a sweep cycled the row to queued. With the trigger
// the leftover dies atomically with the transition itself. The live set
// mirrors liveExecHolder: dispatched/running/waiting_context keep their rows;
// anything else releases them. UPDATE OF state fires on column assignment,
// not on value change: a same-value write to a live state fails the WHEN and
// deletes nothing; one to a non-live state deletes rows that were deletable
// anyway.
func migrateV48(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil {
		return err
	}
	if !exists {
		return nil // event/audit-only legacy DBs have no state column to fire on
	}
	_, err = tx.Exec(`CREATE TRIGGER IF NOT EXISTS exec_locks_gc
		AFTER UPDATE OF state ON tasks
		WHEN NEW.state NOT IN ('dispatched', 'running', 'waiting_context')
		BEGIN
			DELETE FROM exec_locks WHERE task_id = NEW.task_id;
		END`)
	return err
}

// migrateV49 adds exec_locks.run_id: the per-run nonce every acquisition
// stamps on its rows. Releases predicate on it, so a stale release write —
// the deferred unlock of a run whose terminal transition already fired the
// gc trigger — can only delete rows its own acquisition stamped, never the
// fresh rows a re-execution of the same task just took under a new nonce.
// Rows written before this migration carry an empty run_id and are only
// ever stale leftovers (their holder predates the upgrade), so the steal
// path reclaims them and the keyed release simply never matches.
func migrateV49(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "exec_locks")
	if err != nil {
		return err
	}
	if !exists {
		return nil // pre-47 DBs have nothing to alter
	}
	return addColumnIfMissingTx(tx, "exec_locks", "run_id", "TEXT NOT NULL DEFAULT ''")
}

// migrateV50 adds tasks.spawn_depth: the sub-agent generation the task sits
// at in its causal tree (user-submitted root = 0, its delegate children = 1,
// grandchildren = 2…). The wire's Depth field (bus.TaskDelegatePayload) has
// documented this semantics since §6.1 — "Parent.Depth+1 at every spawn hop"
// — but producers stamped the chain length instead and receivers never
// persisted it, so a task's true generation was lost at the first hop. The
// column makes delegation.max_depth enforceable on every node a subtree
// walks through: the spawn check reads the row, not local memory.
func migrateV50(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "tasks", "spawn_depth", "INTEGER NOT NULL DEFAULT 0")
}

// migrateV51 adds tasks.spawn_cap: the tightest delegation.max_depth seen
// along this task's delegation path (0 = none inherited — the receiving
// node's own config applies). A mesh subtree keeps the most conservative
// bound: node A (max_depth 3) delegating into node B (max_depth 1) produces
// a subtree that can never out-spawn what B's operator allowed.
func migrateV51(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "tasks", "spawn_cap", "INTEGER NOT NULL DEFAULT 0")
}

// migrateV52 adds tasks.scope_paths_json: the project-relative directories a
// delegated child declared it owns (the PANDA_DELEGATE paths claim). The
// derived scope leaf locks already persist in resource_keys_json, but the
// declaration itself must survive too: a queued child forwarded to another
// node rebuilds its payload from the row, and without the paths the receiver
// would fall back to the whole-project lock — silently serializing work the
// origin had explicitly partitioned.
func migrateV52(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "tasks", "scope_paths_json", "TEXT NOT NULL DEFAULT ''")
}

// migrateV53 adds tasks.isolate: a delegated child's isolated-execution mode
// (private copy of the project tree, nothing shared locked, changes merged
// back on completion). The flag must survive requeues, forwards and restarts
// — a row that lost it would re-run against the shared tree while its lock
// set still protects nothing.
func migrateV53(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "tasks", "isolate", "INTEGER NOT NULL DEFAULT 0")
}

// migrateV54 adds employee_cache.os and employee_cache.device_class: the two
// machine facts the workspace topology draws with — which system a peer runs
// (darwin/windows/linux) and what kind of box it is (laptop/desktop/mini/
// server/vm). Neither is in the capability card: a card is a declaration the
// operator can edit, while these are properties of the machine itself, filled
// by the daemon at registration and carried in the hello summary. Rows that
// predate this migration read as unknown until the peer speaks again.
//
// It also re-asserts tasks.spawn_depth. This branch briefly carried this
// migration as version 50, and a store that ran that build recorded
// user_version 50 without upstream's 50 ever running — so spawn_depth would
// stay missing forever on exactly the boxes that upgraded through the
// collision. Every statement here is idempotent, so a clean database that ran
// 50-53 sees no change.
func migrateV54(tx MigrationExec) error {
	if exists, err := tableExistsTx(tx, "tasks"); err != nil {
		return err
	} else if exists {
		if err := addColumnIfMissingTx(tx, "tasks", "spawn_depth", "INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	if err := addColumnIfMissingTx(tx, "employee_cache", "os", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return addColumnIfMissingTx(tx, "employee_cache", "device_class", "TEXT NOT NULL DEFAULT ''")
}

// migrateV55 adds cancel_outbox.attempt_id: the dispensation a parked cancel
// stands down. Dispensation-scoped cancels (lease expiry, reroute supersede)
// carry the lapsed attempt so a copy already superseded by a newer delegate
// survives the kill order; rows parked before this column existed carry an
// empty attempt_id and stay tree-scoped, matching the wire payload's
// empty-attempt contract.
func migrateV55(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "cancel_outbox")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "cancel_outbox", "attempt_id", "TEXT NOT NULL DEFAULT ''")
}

// migrateV56 adds the cross-network mesh directory columns (author design
// 2026-10-21 §6.2). x_pub is the peer's X25519 identity key (hex) learned
// from signed hellos next to pub_key — transit sealing needs it to open an
// ECIES-style channel to that node. addrs/relay carry the peer's
// self-advertised dial candidates and relay capability block as JSON from
// its capability summary. learned_via records how the row reached us
// (” = direct hello; populated when directory gossip lands in P2).
// seen_at is the source-minted freshness the signed row carries — distinct
// from last_seen, which is OUR local observation clock. content_hash is the
// change-detection digest over the row's signed content (minus seen_at/sig):
// directory gossip compares it instead of shipping whole rows.
func migrateV56(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	for _, col := range []struct{ name, def string }{
		{"x_pub", "TEXT NOT NULL DEFAULT ''"},
		{"addrs", "TEXT NOT NULL DEFAULT ''"},
		{"relay", "TEXT NOT NULL DEFAULT ''"},
		{"learned_via", "TEXT NOT NULL DEFAULT ''"},
		{"seen_at", "INTEGER NOT NULL DEFAULT 0"},
		{"content_hash", "BLOB"},
	} {
		if err := addColumnIfMissingTx(tx, "employee_cache", col.name, col.def); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_employee_seen ON employee_cache(seen_at)`)
	return err
}

// migrateV57 adds employee_cache.prov: the peer's self-asserted build
// provenance (release | self | unsigned), stamped from
// hello/heartbeat `prov` the same way `ver` follows the wire. The trust
// layer reads it when weighing a self-signed origin's work
// (accept.<tier>.self_signed); rows from pre-column peers stay empty —
// "never reported", distinct from an honest "unsigned".
func migrateV57(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "employee_cache")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "employee_cache", "prov", "TEXT NOT NULL DEFAULT ''")
}

// migrateV60 adds task_messages.attachments: the files a user rode along with
// a queue/inject note, as a JSON array of workspace-relative paths. The body
// already carries them as `@path` refs for the agent (see PostUserNote); this
// column is what lets a console draw the note's chips and let the user edit
// the prose without re-typing the attachment block. Empty = no attachments,
// which is every row written before this migration.
func migrateV60(tx MigrationExec) error {
	_, err := tx.Exec(`ALTER TABLE task_messages
		ADD COLUMN attachments TEXT NOT NULL DEFAULT ''`)
	return err
}

// migrateV61 adds the user's to-do board (internal/todos). A todo is not a
// task: tasks carry a state machine and an executor, while a todo is a note
// that only becomes work when its owner starts it — so it lives in its own
// table instead of borrowing task states. `seq` is the manual lane order the
// board's drag writes (0 = never dragged, which sorts last); `task_id` links
// a todo to the task started from it, empty until then.
func migrateV61(tx MigrationExec) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS todos (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending',
		priority TEXT NOT NULL DEFAULT 'normal',
		due_at INTEGER NOT NULL DEFAULT 0,
		started_at INTEGER NOT NULL DEFAULT 0,
		done_at INTEGER NOT NULL DEFAULT 0,
		tags TEXT NOT NULL DEFAULT '[]',
		project TEXT NOT NULL DEFAULT '',
		task_id TEXT NOT NULL DEFAULT '',
		seq INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	// The board always filters by lane and usually by project; the due index
	// backs the "overdue first" read the page does on every paint.
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_todos_status ON todos(status, seq)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_todos_project ON todos(project)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_todos_due ON todos(due_at)`)
	return err
}

// migrateV63 adds the acceptance layer: tasks.verification carries the
// contract verdict (verified / failed / partial / skipped, empty for tasks
// that never had one), and task_checks records every check the orchestrator
// ran, by phase. The rows are evidence, not a cache: the baseline phase is
// what lets the report say "this suite was already red" instead of blaming
// the run, and the final phase is what a task is actually judged on.
func migrateV64(tx MigrationExec) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS task_checks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL,
		phase TEXT NOT NULL,
		check_id TEXT NOT NULL,
		kind TEXT NOT NULL DEFAULT 'command',
		command TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		exit_code INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		output_head TEXT NOT NULL DEFAULT '',
		output_tail TEXT NOT NULL DEFAULT '',
		reason TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_task_checks_task ON task_checks(task_id, id)`); err != nil {
		return err
	}
	// Legacy fixtures migrate from a partial schema; without a tasks table
	// there is nothing to stamp. The same guard every column migration uses.
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	return addColumnIfMissingTx(tx, "tasks", "verification", "TEXT NOT NULL DEFAULT ''")
}

// migrateV65 indexes tasks.session_id. A conversation reads its own task set
// on every change frame — the live progress rows and the round roll-up — and
// that lookup was a full table scan. taskColumns drags spec_json and
// result_json along with every row, so the cost grew with the whole board
// rather than with the thread being watched.
func migrateV65(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "tasks")
	if err != nil || !exists {
		return err
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_session ON tasks(session_id)`)
	return err
}

// migrateV66 adds intake_seen: the idempotence ledger for inbound source
// events. A source retries a delivery when a webhook answers slowly or not at
// all, and a replay must not open a second task for the same comment. The row
// is keyed by (source, delivery id); seen_at lets a later claim drop keys past
// the retention window without a second table.
func migrateV66(tx MigrationExec) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS intake_seen (
		source TEXT NOT NULL,
		key TEXT NOT NULL,
		seen_at INTEGER NOT NULL,
		PRIMARY KEY (source, key)
	)`); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_intake_seen_at ON intake_seen(seen_at)`)
	return err
}
