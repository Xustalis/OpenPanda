// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

func migrateV76(tx MigrationExec) error {
	exists, err := tableExistsTx(tx, "delegation_metrics")
	if err != nil {
		return err
	}
	if exists {
		if err = addColumnIfMissingTx(tx, "delegation_metrics", "usage_source", "TEXT NOT NULL DEFAULT 'task'"); err != nil {
			return err
		}
	}
	for _, q := range []string{
		`CREATE TABLE conversation_proposals (session_id TEXT NOT NULL, version INTEGER NOT NULL, proposal TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(session_id,version))`,
		`CREATE TABLE conversation_plan_runs (session_id TEXT NOT NULL, version INTEGER NOT NULL, proposal TEXT NOT NULL,
 round_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending',
 goal_id TEXT NOT NULL DEFAULT '', goal_generation INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(session_id,version))`,
		`CREATE TABLE conversation_controls (
 session_id TEXT PRIMARY KEY, planning INTEGER NOT NULL DEFAULT 0, mode_generation INTEGER NOT NULL DEFAULT 0,
 proposal_version INTEGER NOT NULL DEFAULT 0, proposal TEXT NOT NULL DEFAULT '',
 confirmed_version INTEGER NOT NULL DEFAULT 0, deleted INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE conversation_goals (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL, objective TEXT NOT NULL,
 status TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', generation INTEGER NOT NULL DEFAULT 1,
 round_id TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
 rounds INTEGER NOT NULL DEFAULT 0, tokens INTEGER NOT NULL DEFAULT 0, token_budget INTEGER,
 blocker TEXT NOT NULL DEFAULT '', blocked_streak INTEGER NOT NULL DEFAULT 0,
 empty_streak INTEGER NOT NULL DEFAULT 0, completion_requested INTEGER NOT NULL DEFAULT 0,
 paused_by_plan INTEGER NOT NULL DEFAULT 0, waiting_for TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE UNIQUE INDEX conversation_goal_current ON conversation_goals(session_id) WHERE status!='cleared'`,
		`CREATE TABLE conversation_goal_rounds (
 round_id TEXT PRIMARY KEY REFERENCES ask_rounds(round_id), goal_id TEXT NOT NULL REFERENCES conversation_goals(id),
 generation INTEGER NOT NULL, entry_tokens INTEGER NOT NULL DEFAULT 0, task_tokens INTEGER NOT NULL DEFAULT 0,
 settled INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE conversation_events (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL,
 kind TEXT NOT NULL, payload TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE INDEX conversation_events_session ON conversation_events(session_id,id)`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func migrateV77(tx MigrationExec) error {
	// Keep the round ledger for settlement and late usage. The cleared goal
	// itself no longer needs to hold the controller's active-round pointer.
	_, err := tx.Exec(`UPDATE conversation_goals SET round_id='',lease_owner='',lease_until=0 WHERE status='cleared' AND round_id!=''`)
	return err
}
