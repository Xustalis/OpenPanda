// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

// Keep replay input beside the durable request, rather than relying on a
// caller to retain it across a process crash. Existing hash-only requests
// remain readable and can be repaired by an identical explicit resubmission.
func migrateV71(tx MigrationExec) error {
	_, err := tx.Exec(`
ALTER TABLE execution_requests ADD COLUMN payload_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE execution_requests ADD COLUMN payload_json TEXT NOT NULL DEFAULT '';
ALTER TABLE execution_requests ADD COLUMN preparation_error TEXT NOT NULL DEFAULT '';
ALTER TABLE execution_requests ADD COLUMN recovery_allowed INTEGER NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_execution_requests_recovery ON execution_requests(state,connection_id,created_at);
`)
	return err
}
