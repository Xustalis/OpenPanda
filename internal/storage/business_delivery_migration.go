// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

func migrateV74(tx MigrationExec) error {
	// A single attempt may publish successive immutable delivery revisions.
	// The v73 auto-index cannot be dropped independently of its constraint.
	_, err := tx.Exec(`
CREATE TABLE work_deliveries_v74 (
 delivery_id TEXT NOT NULL, revision INTEGER NOT NULL, request_id TEXT NOT NULL,
 attempt_id TEXT NOT NULL, payload_hash TEXT NOT NULL, delivery_json TEXT NOT NULL,
 state TEXT NOT NULL, created_at INTEGER NOT NULL,
 PRIMARY KEY(delivery_id,revision), UNIQUE(request_id,attempt_id,revision)
);
INSERT INTO work_deliveries_v74 SELECT * FROM work_deliveries;
DROP TABLE work_deliveries;
ALTER TABLE work_deliveries_v74 RENAME TO work_deliveries;
CREATE INDEX idx_work_delivery_request ON work_deliveries(request_id,revision);
ALTER TABLE final_actions ADD COLUMN delivery_id TEXT NOT NULL DEFAULT '';
ALTER TABLE final_actions ADD COLUMN delivery_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE final_actions ADD COLUMN proof_json TEXT NOT NULL DEFAULT '{}';
CREATE UNIQUE INDEX idx_final_action_delivery ON final_actions(delivery_id,delivery_revision) WHERE delivery_id!='';
ALTER TABLE action_receipts ADD COLUMN report_status TEXT NOT NULL DEFAULT '';
CREATE TABLE task_sync_event_history (event_id TEXT PRIMARY KEY, request_id TEXT NOT NULL, kind TEXT NOT NULL, body_json TEXT NOT NULL, body_hash TEXT NOT NULL, replaced_by TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE INDEX idx_task_sync_outbox_pending ON task_sync_outbox(state,next_at,request_id);
`)
	return err
}
