// SPDX-License-Identifier: AGPL-3.0-or-later

package taskhub

import (
	"context"
	"net/url"
	"strings"
)

type TeamTaskSummary struct {
	Title        string `json:"title"`
	Space        string `json:"space"`
	Project      string `json:"project,omitempty"`
	Status       string `json:"status"`
	AssignedToMe bool   `json:"assignedToMe"`
	URL          string `json:"url"`
}

// SearchTeam reads grants, bindings and tasks in a single snapshot. An exact
// project mapping takes precedence over a whole-space binding.
func (s *Store) SearchTeam(ctx context.Context, project, query string, mine bool, offset int) ([]TeamTaskSummary, error) {
	rows, err := s.DB.QueryContext(ctx, `WITH visible AS (
 SELECT s.connection_id,s.task_key,s.updated_at,
 json_extract(s.snapshot_json,'$.title') AS title,
 coalesce(json_extract(s.snapshot_json,'$.spaceName'),'') AS space_name,
 coalesce(json_extract(s.snapshot_json,'$.projectName'),'') AS project_name,
 json_extract(s.snapshot_json,'$.status') AS status,
 EXISTS(SELECT 1 FROM json_each(s.snapshot_json,'$.assigneeAccountIds') WHERE value=c.account_id) AS mine
 FROM task_snapshots s JOIN task_connections c ON c.connection_id=s.connection_id
 WHERE `+readableSnapshotSQL+` AND c.provider='tsync' AND (?1='' OR ?1=(
 SELECT b.local_project FROM task_project_bindings b WHERE b.connection_id=c.connection_id AND b.account_id=c.account_id
 AND b.workspace_id=json_extract(s.snapshot_json,'$.key.workspaceId') AND b.space_id=json_extract(s.snapshot_json,'$.key.spaceId')
 AND (b.remote_project_id='' OR b.remote_project_id=json_extract(s.snapshot_json,'$.projectId'))
 ORDER BY length(b.remote_project_id) DESC LIMIT 1)))
 SELECT connection_id,task_key,substr(title,1,400),substr(space_name,1,160),substr(project_name,1,160),status,mine FROM visible
 WHERE (NOT ?2 OR mine) AND (instr(lower(title),lower(?3)) OR instr(lower(space_name),lower(?3)) OR instr(lower(project_name),lower(?3)))
 ORDER BY updated_at DESC,connection_id,task_key LIMIT 21 OFFSET ?4`, project, mine, strings.TrimSpace(query), offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamTaskSummary{}
	for rows.Next() {
		var v TeamTaskSummary
		var id, key string
		if err := rows.Scan(&id, &key, &v.Title, &v.Space, &v.Project, &v.Status, &v.AssignedToMe); err != nil {
			return nil, err
		}
		v.URL = "#/work/tasks/" + url.PathEscape(key) + "?connection=" + url.QueryEscape(id)
		out = append(out, v)
	}
	return out, rows.Err()
}
