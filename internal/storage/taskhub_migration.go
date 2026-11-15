// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

// Business task data is separate from kernel tasks. Local intentions continue
// to live in todos; remote snapshots are an account-scoped read cache.
func migrateV69(tx MigrationExec) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS task_connections (
 connection_id TEXT PRIMARY KEY, provider TEXT NOT NULL, root_url TEXT NOT NULL DEFAULT '',
 account_id TEXT NOT NULL DEFAULT '', instance_id TEXT NOT NULL DEFAULT '',
 device_id TEXT NOT NULL DEFAULT '', credential_ref TEXT NOT NULL DEFAULT '',
 auth_epoch TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'disconnected',
 feed_epoch TEXT NOT NULL DEFAULT '', cursor TEXT NOT NULL DEFAULT '',
 capabilities_json TEXT NOT NULL DEFAULT '{}', spaces_json TEXT NOT NULL DEFAULT '[]',
 updated_at INTEGER NOT NULL, last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS task_snapshots (
 connection_id TEXT NOT NULL, account_id TEXT NOT NULL, task_key TEXT NOT NULL,
 snapshot_json TEXT NOT NULL, auth_epoch TEXT NOT NULL, updated_at INTEGER NOT NULL,
 PRIMARY KEY(connection_id, account_id, task_key)
);
CREATE TABLE IF NOT EXISTS task_directories (
 connection_id TEXT NOT NULL, account_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
 space_id TEXT NOT NULL, directory_json TEXT NOT NULL, auth_epoch TEXT NOT NULL,
 PRIMARY KEY(connection_id, account_id, workspace_id, space_id)
);
CREATE TABLE IF NOT EXISTS task_project_bindings (
 connection_id TEXT NOT NULL, account_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
 space_id TEXT NOT NULL, remote_project_id TEXT NOT NULL, local_project TEXT NOT NULL,
 PRIMARY KEY(connection_id, account_id, workspace_id, space_id, remote_project_id)
);
CREATE TABLE IF NOT EXISTS task_policies (
 connection_id TEXT NOT NULL, account_id TEXT NOT NULL, space_id TEXT NOT NULL,
 policy_json TEXT NOT NULL, updated_at INTEGER NOT NULL,
 PRIMARY KEY(connection_id, account_id, space_id)
);
CREATE TABLE IF NOT EXISTS execution_requests (
 request_id TEXT PRIMARY KEY, connection_id TEXT NOT NULL DEFAULT '',
 account_id TEXT NOT NULL DEFAULT '', task_key TEXT NOT NULL,
 trigger_key TEXT NOT NULL, input_revision TEXT NOT NULL DEFAULT '',
 purpose TEXT NOT NULL, payload_hash TEXT NOT NULL DEFAULT '',
 local_todo_id INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL DEFAULT 'preparing', task_id TEXT NOT NULL DEFAULT '',
 plan_id TEXT NOT NULL DEFAULT '', approval_mode TEXT NOT NULL DEFAULT 'before_submit',
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 UNIQUE(connection_id, account_id, task_key, trigger_key)
);
CREATE TABLE IF NOT EXISTS execution_links (
 request_id TEXT NOT NULL REFERENCES execution_requests(request_id),
 task_id TEXT NOT NULL, plan_id TEXT NOT NULL DEFAULT '', stage_id TEXT NOT NULL DEFAULT '',
 role TEXT NOT NULL, created_at INTEGER NOT NULL,
 PRIMARY KEY(request_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_execution_requests_task
 ON execution_requests(connection_id, account_id, task_key, created_at);
CREATE INDEX IF NOT EXISTS idx_execution_links_task ON execution_links(task_id);
`)
	if err != nil {
		return err
	}
	todosExist, err := tableExistsTx(tx, "todos")
	if err != nil || !todosExist {
		return err
	}
	tasksExist, err := tableExistsTx(tx, "tasks")
	if err != nil {
		return err
	}
	state := "'unavailable'"
	if tasksExist {
		state = `CASE WHEN EXISTS(SELECT 1 FROM tasks WHERE tasks.task_id=todos.task_id) THEN 'published' ELSE 'unavailable' END`
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO execution_requests
 (request_id,task_key,trigger_key,purpose,local_todo_id,state,task_id,created_at,updated_at)
 SELECT 'legacy:todo:' || id, 'local/local/local/' || id, 'legacy', 'legacy', id,
 ` + state + `, task_id,created_at,updated_at
 FROM todos WHERE COALESCE(task_id,'') != '';
INSERT OR IGNORE INTO execution_links(request_id,task_id,role,created_at)
 SELECT 'legacy:todo:' || id,task_id,'legacy',created_at
 FROM todos WHERE COALESCE(task_id,'') != '';
`)
	return err
}
