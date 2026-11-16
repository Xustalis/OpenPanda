// SPDX-License-Identifier: AGPL-3.0-or-later
package storage

// The request and its fully initialized child are inserted in one transaction.
// Keeping the source attempt in the key prevents an obsolete executor from
// replaying control decisions into a newer attempt.
func migrateV75(tx MigrationExec) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS task_child_requests (
 parent_id TEXT NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
 attempt_id TEXT NOT NULL, request_id TEXT NOT NULL, input_hash TEXT NOT NULL,
 child_id TEXT NOT NULL UNIQUE REFERENCES tasks(task_id) ON DELETE CASCADE,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(parent_id,attempt_id,request_id))`)
	return err
}
