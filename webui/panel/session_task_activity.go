// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/core"
)

func (h *handler) attachSessionTaskActivity(ctx context.Context, session string, tasks []taskJSON) {
	if h.db == nil || len(tasks) == 0 {
		return
	}
	byID := make(map[string]*taskJSON, len(tasks))
	for i := range tasks {
		byID[tasks[i].ID] = &tasks[i]
	}
	rows, err := h.db.QueryContext(ctx, `SELECT m.task_id,r.round_id,m.rowid,r.status
		FROM ask_rounds r JOIN ask_round_tasks m ON m.round_id=r.round_id WHERE r.session_id=?`, session)
	if err == nil {
		for rows.Next() {
			var id, op, status string
			var order int64
			if rows.Scan(&id, &op, &order, &status) == nil && byID[id] != nil {
				byID[id].OperationID, byID[id].DispatchOrder, byID[id].DispatchOpen = op, order, status == "open"
			}
		}
		_ = rows.Close()
	}
	// Running in the task state machine includes preparation, the judge and
	// delivery. Only a matching adapter start without a later closing event
	// earns the concurrent execution indicator. Use that same event for the
	// call name, rather than racing a separate read against a new start.
	rows, err = h.db.QueryContext(ctx, `SELECT t.task_id,e.type,e.data_json
		FROM tasks t JOIN task_events e ON e.id=(SELECT id FROM task_events
		 WHERE task_id=t.task_id AND type IN (?,?,?,?,?,?) ORDER BY id DESC LIMIT 1)
		WHERE t.session_id=? AND t.state=?`, core.EvExecAgentStart, core.EvExecAgentStop,
		core.EvRetry, core.EvSubmit, core.EvReview, core.EvSupervise, session, core.StateRunning)
	if err == nil {
		for rows.Next() {
			var id, event, data string
			if rows.Scan(&id, &event, &data) == nil && event == core.EvExecAgentStart {
				if task := byID[id]; task != nil && task.State == core.StateRunning {
					task.CallName, task.Executing = sessionCall(data, task.AttemptID)
				}
			}
		}
		_ = rows.Close()
	}
	h.attachSessionCallNames(ctx, session, byID)
}

func (h *handler) attachSessionCallNames(ctx context.Context, session string, tasks map[string]*taskJSON) {
	rows, err := h.db.QueryContext(ctx, `SELECT t.task_id,e.data_json
		FROM tasks t JOIN task_events e ON e.id=(SELECT id FROM task_events
		 WHERE task_id=t.task_id AND type=? ORDER BY id DESC LIMIT 1)
		WHERE t.session_id=?`, core.EvExecAgentStart, session)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, data string
		if rows.Scan(&id, &data) == nil {
			if task := tasks[id]; task != nil && !task.Executing {
				task.CallName, _ = sessionCall(data, task.AttemptID)
			}
		}
	}
}

func sessionCall(data, attempt string) (string, bool) {
	var start struct {
		Attempt string `json:"attempt_id"`
		Agent   string `json:"agent"`
		Adapter string `json:"adapter"`
		Ability string `json:"ability"`
		Kind    string `json:"plan_kind"`
	}
	if json.Unmarshal([]byte(data), &start) != nil || attempt == "" || start.Attempt != attempt {
		return "", false
	}
	if start.Kind == "native" {
		return start.Ability, true
	}
	if start.Agent != "" {
		return start.Agent, true
	}
	return strings.TrimSuffix(start.Adapter, ".py"), true
}
