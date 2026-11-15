// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

func migrateV70(tx MigrationExec) error {
	if err := addColumnIfMissingTx(tx, "todos", "work_revision", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	_, err := tx.Exec(`
CREATE TRIGGER IF NOT EXISTS todos_work_revision AFTER UPDATE ON todos
 WHEN NEW.work_revision=OLD.work_revision BEGIN
 UPDATE todos SET work_revision=OLD.work_revision+1 WHERE id=NEW.id;
END;
CREATE TABLE IF NOT EXISTS task_mutations (
 idempotency_key TEXT PRIMARY KEY, payload_hash TEXT NOT NULL,
 response_json TEXT NOT NULL, created_at INTEGER NOT NULL
);
`)
	return err
}
