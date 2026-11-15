// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

func migrateV72(tx MigrationExec) error {
	for name, spec := range map[string]string{
		"generation":   "INTEGER NOT NULL DEFAULT 1",
		"requested_at": "INTEGER NOT NULL DEFAULT 0",
		"synced_at":    "INTEGER NOT NULL DEFAULT 0",
		"next_sync_at": "INTEGER NOT NULL DEFAULT 0",
		"failures":     "INTEGER NOT NULL DEFAULT 0",
		"scopes_json":  "TEXT NOT NULL DEFAULT '[]'",
	} {
		if err := addColumnIfMissingTx(tx, "task_connections", name, spec); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS task_space_cursors (
 connection_id TEXT NOT NULL, account_id TEXT NOT NULL, space_id TEXT NOT NULL,
 workspace_id TEXT NOT NULL, feed_epoch TEXT NOT NULL, auth_epoch TEXT NOT NULL,
 cursor TEXT NOT NULL, updated_at INTEGER NOT NULL,
 PRIMARY KEY(connection_id,account_id,space_id)
);
CREATE TABLE IF NOT EXISTS task_sync_events (
 connection_id TEXT NOT NULL, account_id TEXT NOT NULL, event_id TEXT NOT NULL,
 space_id TEXT NOT NULL, task_key TEXT NOT NULL DEFAULT '', event_type TEXT NOT NULL,
 payload_json TEXT NOT NULL, auth_epoch TEXT NOT NULL, created_at INTEGER NOT NULL,
 PRIMARY KEY(connection_id,account_id,event_id)
);
CREATE TABLE IF NOT EXISTS task_runtime_leases (
 lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_task_sync_events_space ON task_sync_events(connection_id,account_id,space_id);
`)
	return err
}
