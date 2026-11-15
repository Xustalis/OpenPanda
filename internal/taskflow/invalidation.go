// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
)

// Keep the cost ledger and execution identities for deduplication and audit;
// erase cached private text and revoke authority in the same transaction.
func invalidateAutomation(ctx context.Context, tx *sql.Tx, connection, space string) error {
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE research_briefs SET state='invalidated',brief='',memory_hash='' WHERE connection_id=? AND (?='' OR task_key IN (SELECT task_key FROM task_snapshots WHERE connection_id=? AND json_extract(snapshot_json,'$.key.spaceId')=?))`, []any{connection, space, connection, space}},
		{`UPDATE approval_records SET decision='invalidated',expires_at=0 WHERE action_id IN (SELECT a.action_id FROM final_actions a JOIN task_flow_runs f ON f.request_id=a.request_id WHERE f.connection_id=? AND (?='' OR json_extract(f.snapshot_json,'$.key.spaceId')=?))`, []any{connection, space, space}},
		{`UPDATE task_sync_outbox SET state='paused',owner_id='',lease_until=0,error_code='authorization_changed' WHERE connection_id=? AND (?='' OR request_id IN (SELECT request_id FROM task_flow_runs WHERE connection_id=? AND json_extract(snapshot_json,'$.key.spaceId')=?))`, []any{connection, space, connection, space}},
		{`UPDATE task_flow_runs SET state='paused',error_code='authorization_changed',deadline=0,lease_owner='' WHERE connection_id=? AND (?='' OR json_extract(snapshot_json,'$.key.spaceId')=?) AND state NOT IN ('succeeded','failed','cancelled','superseded')`, []any{connection, space, space}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}
