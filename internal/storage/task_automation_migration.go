// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

func migrateV73(tx MigrationExec) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS task_flow_runs (
 request_id TEXT PRIMARY KEY, connection_id TEXT NOT NULL, account_id TEXT NOT NULL,
 task_key TEXT NOT NULL, trigger_key TEXT NOT NULL, auth_epoch TEXT NOT NULL,
 connection_generation INTEGER NOT NULL, input_revision TEXT NOT NULL,
 assignment_revision TEXT NOT NULL, automation_generation TEXT NOT NULL,
 execution_id TEXT NOT NULL DEFAULT '', record_json TEXT NOT NULL DEFAULT '{}',
 snapshot_json TEXT NOT NULL, policy_json TEXT NOT NULL,
 claim_json TEXT NOT NULL, claim_id TEXT NOT NULL,
 heartbeat_id TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'claiming', error_code TEXT NOT NULL DEFAULT '',
 deadline INTEGER NOT NULL DEFAULT 0, next_at INTEGER NOT NULL DEFAULT 0,
 prepared_json TEXT NOT NULL DEFAULT '', prepare_attempt TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 UNIQUE(connection_id,account_id,task_key,trigger_key)
);
CREATE INDEX IF NOT EXISTS idx_task_flow_runs_state ON task_flow_runs(state,connection_id,next_at);
CREATE TABLE IF NOT EXISTS task_flow_commands (
 command_id TEXT PRIMARY KEY, request_id TEXT NOT NULL REFERENCES task_flow_runs(request_id),
 kind TEXT NOT NULL, body_json TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending',
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS research_briefs (
 cache_key TEXT PRIMARY KEY, subject TEXT NOT NULL, connection_id TEXT NOT NULL,
 account_id TEXT NOT NULL, task_key TEXT NOT NULL, input_revision TEXT NOT NULL,
 auth_epoch TEXT NOT NULL, memory_hash TEXT NOT NULL, model_version TEXT NOT NULL,
 state TEXT NOT NULL, brief TEXT NOT NULL DEFAULT '', reserved_tokens INTEGER NOT NULL CHECK(reserved_tokens>=0),
 day TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_research_budget ON research_briefs(subject,day);
CREATE TABLE IF NOT EXISTS final_actions (
 action_id TEXT PRIMARY KEY, request_id TEXT NOT NULL, attempt_id TEXT NOT NULL,
 fingerprint TEXT NOT NULL, intent_json TEXT NOT NULL, state TEXT NOT NULL,
 expires_at INTEGER NOT NULL, dispatch_id TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS approval_records (
 approval_id TEXT PRIMARY KEY, action_id TEXT NOT NULL, fingerprint TEXT NOT NULL,
 approver TEXT NOT NULL, decision TEXT NOT NULL, expires_at INTEGER NOT NULL,
 created_at INTEGER NOT NULL, UNIQUE(action_id,fingerprint,approver,decision)
);
CREATE TABLE IF NOT EXISTS action_receipts (
 action_id TEXT PRIMARY KEY, dispatch_id TEXT NOT NULL, receipt_json TEXT NOT NULL,
 verified INTEGER NOT NULL, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS work_deliveries (
 delivery_id TEXT NOT NULL, revision INTEGER NOT NULL, request_id TEXT NOT NULL,
 attempt_id TEXT NOT NULL, payload_hash TEXT NOT NULL, delivery_json TEXT NOT NULL,
 state TEXT NOT NULL, created_at INTEGER NOT NULL,
 PRIMARY KEY(delivery_id,revision), UNIQUE(request_id,attempt_id)
);
CREATE TABLE IF NOT EXISTS work_delivery_decisions (
 decision_id TEXT PRIMARY KEY, delivery_id TEXT NOT NULL, revision INTEGER NOT NULL,
 payload_hash TEXT NOT NULL, account_id TEXT NOT NULL, decision TEXT NOT NULL,
 proof_json TEXT NOT NULL DEFAULT '{}', created_at INTEGER NOT NULL,
 UNIQUE(delivery_id,revision,account_id)
);
CREATE TABLE IF NOT EXISTS task_sync_outbox (
 event_id TEXT PRIMARY KEY, request_id TEXT NOT NULL, connection_id TEXT NOT NULL,
 account_id TEXT NOT NULL, auth_epoch TEXT NOT NULL, kind TEXT NOT NULL,
 path TEXT NOT NULL, body_json TEXT NOT NULL, body_hash TEXT NOT NULL,
 revision TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'pending',
 owner_id TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0, next_at INTEGER NOT NULL DEFAULT 0,
 error_code TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL
);
`)
	return err
}
