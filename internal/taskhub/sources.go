// SPDX-License-Identifier: AGPL-3.0-or-later

package taskhub

import (
	"context"
	"strings"
)

type Source struct {
	ConnectionID string `json:"connectionId"`
	TaskKey      string `json:"taskKey"`
	RequestID    string `json:"requestId"`
}

// Sources batches source backlinks and excludes revoked remote identities.
func (s *Store) Sources(ctx context.Context, ids []string) (map[string]Source, error) {
	out := make(map[string]Source)
	for start := 0; start < len(ids); start += 400 {
		end := min(start+400, len(ids))
		args := make([]any, 0, end-start)
		for _, id := range ids[start:end] {
			args = append(args, id)
		}
		marks := strings.TrimRight(strings.Repeat("?,", len(args)), ",")
		rows, err := s.DB.QueryContext(ctx, `SELECT l.task_id,r.connection_id,r.task_key,r.request_id
 FROM execution_links l JOIN execution_requests r ON r.request_id=l.request_id
 WHERE l.task_id IN (`+marks+`) AND (r.connection_id='' OR EXISTS(
 SELECT 1 FROM task_connections c JOIN task_snapshots s ON s.connection_id=c.connection_id
 AND s.account_id=c.account_id AND s.auth_epoch=c.auth_epoch WHERE c.connection_id=r.connection_id
 AND c.account_id=r.account_id AND s.task_key=r.task_key
 AND `+readableSnapshotSQL+`)) ORDER BY r.created_at,r.request_id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var source Source
			if err = rows.Scan(&id, &source.ConnectionID, &source.TaskKey, &source.RequestID); err != nil {
				rows.Close()
				return nil, err
			}
			if source.ConnectionID == "" {
				source.ConnectionID = "local"
			}
			out[id] = source
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
