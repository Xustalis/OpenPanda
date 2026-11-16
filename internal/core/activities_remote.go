// SPDX-License-Identifier: AGPL-3.0-or-later
package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

const activityTypesSQL = "'agent_event','reasoning','exec_agent_start','supervision_round','judge_start','adapter_execution_profile','spawn_child_task','child_spawned','artifact_transfer','project_sync'"
const remoteActivityCursorEvent = "remote_activity_cursor"
const maxActivityBatchBytes = 256 << 10

func (s *TaskStore) activityCursor(ctx context.Context, taskID string) int64 {
	var cursor int64
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM task_events WHERE task_id=? AND type IN (`+activityTypesSQL+`)`, taskID).Scan(&cursor)
	return cursor
}

func (s *TaskStore) receivedActivityCursor(ctx context.Context, taskID, attempt, from string) int64 {
	var cursor int64
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(CAST(json_extract(data_json,'$.cursor') AS INTEGER)),0) FROM task_events WHERE task_id=? AND type=? AND json_extract(data_json,'$.attempt_id')=? AND json_extract(data_json,'$.from')=?`, taskID, remoteActivityCursorEvent, attempt, from).Scan(&cursor)
	return cursor
}

func activityTypeAllowed(typ string) bool {
	return strings.Contains(","+strings.ReplaceAll(activityTypesSQL, "'", "")+",", ","+typ+",")
}

func (c *Core) sendActivityMessage(ctx context.Context, typ, to string, payload any) {
	id, err := newUUID()
	if err != nil {
		return
	}
	env, err := bus.NewEnvelope(typ, c.nodeID, id, payload)
	if err != nil {
		return
	}
	env.To = to
	if err := c.sendTo(to, env); err != nil {
		c.logger.Debug("activity transport deferred", "to", to, "err", err)
	}
}

func (c *Core) requestActivities(ctx context.Context, t Task, from string, advertised int64) {
	if advertised <= 0 || from == "" {
		return
	}
	cursor := c.store.receivedActivityCursor(ctx, t.TaskID, t.AttemptID, from)
	if cursor >= advertised {
		return
	}
	c.sendActivityMessage(ctx, bus.MsgTaskActivityFetch, from, bus.TaskActivityFetchPayload{TaskID: t.TaskID, AttemptID: t.AttemptID, After: cursor})
}

func (c *Core) handleActivityFetch(ctx context.Context, env bus.Envelope) {
	var p bus.TaskActivityFetchPayload
	if env.PayloadInto(&p) != nil || p.After < 0 {
		return
	}
	task, err := c.store.Get(ctx, p.TaskID)
	if err != nil || task.AttemptID != p.AttemptID {
		return
	}
	// Only the predecessor that entrusted us with this task may fetch its
	// contents. General mesh membership is not task transcript authorization.
	predecessor := scheduler.Predecessor(task.Chain, c.nodeID)
	if predecessor == "" || predecessor != env.From {
		return
	}
	rows, err := c.store.db.QueryContext(ctx, `SELECT id,ts,type,data_json FROM task_events WHERE task_id=? AND id>? AND type IN (`+activityTypesSQL+`) ORDER BY id LIMIT 33`, task.TaskID, p.After)
	if err != nil {
		return
	}
	batch := bus.TaskActivitiesPayload{TaskID: task.TaskID, AttemptID: task.AttemptID, After: p.After, Cursor: p.After}
	total := 0
	for rows.Next() {
		var id, ts int64
		var typ, data string
		if rows.Scan(&id, &ts, &typ, &data) != nil {
			break
		}
		if len(batch.Activities) >= 32 || (total+len(data) > maxActivityBatchBytes && len(batch.Activities) > 0) {
			batch.More = true
			break
		}
		var object map[string]any
		if json.Unmarshal([]byte(data), &object) != nil {
			object = map[string]any{"ev": "activity_unavailable"}
		}
		executor, _ := object["executor_id"].(string)
		if executor == "" {
			executor = c.nodeID
		}
		activityID, _ := object["activity_id"].(string)
		if activityID == "" {
			activityID = fmt.Sprintf("%s:%d", executor, id)
		}
		if len(data) > maxActivityBatchBytes {
			object = map[string]any{"ev": "activity_truncated", "original_bytes": len(data), "attempt_id": task.AttemptID}
			dataBytes, _ := json.Marshal(object)
			data = string(dataBytes)
		}
		batch.Activities = append(batch.Activities, bus.TaskActivity{ID: activityID, Executor: executor, Type: typ, TS: ts, Data: json.RawMessage(data)})
		batch.Cursor = id
		total += len(data)
	}
	_ = rows.Close()
	c.sendActivityMessage(ctx, bus.MsgTaskActivities, env.From, batch)
}

func (c *Core) handleActivities(ctx context.Context, env bus.Envelope) {
	var p bus.TaskActivitiesPayload
	if env.PayloadInto(&p) != nil || len(p.Activities) > 32 || p.Cursor < p.After || p.After < 0 {
		return
	}
	task, err := c.store.Get(ctx, p.TaskID)
	if err != nil || task.AttemptID != p.AttemptID || !c.isCurrentExecutor(ctx, task, env.From) {
		return
	}
	if p.After != c.store.receivedActivityCursor(ctx, p.TaskID, p.AttemptID, env.From) {
		return
	}
	if err := c.store.importActivities(ctx, p, env.From); err != nil {
		c.logger.Warn("import remote activities", "task", p.TaskID, "err", err)
		return
	}
	// Every custodian keeps a durable copy. Its own cursor addresses these
	// local rows, while the stable activity_id preserves origin identity.
	c.relayToParent(ctx, bus.MsgTaskProgress, task.Chain, bus.TaskProgressPayload{TaskID: task.TaskID, AttemptID: task.AttemptID, ActivityCursor: c.store.activityCursor(ctx, task.TaskID)})
	if p.More && p.Cursor > p.After {
		c.requestActivities(ctx, task, env.From, p.Cursor+1)
	}
}

func (s *TaskStore) importActivities(ctx context.Context, p bus.TaskActivitiesPayload, from string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		// The attempt can rotate between wire admission and transaction entry.
		var attempt string
		if err := tx.QueryRowContext(ctx, `SELECT attempt_id FROM tasks WHERE task_id=?`, p.TaskID).Scan(&attempt); err != nil {
			return err
		}
		if attempt != p.AttemptID {
			return ErrConflict
		}
		for _, ev := range p.Activities {
			if !activityTypeAllowed(ev.Type) || ev.ID == "" || ev.Executor == "" || len(ev.ID) > 256 || len(ev.Executor) > 256 || len(ev.Data) > maxActivityBatchBytes {
				return fmt.Errorf("invalid activity batch")
			}
			var data map[string]any
			if json.Unmarshal(ev.Data, &data) != nil || data == nil {
				return fmt.Errorf("invalid activity data")
			}
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE task_id=? AND json_extract(data_json,'$.activity_id')=?`, p.TaskID, ev.ID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			data["activity_id"] = ev.ID
			data["executor_id"] = ev.Executor
			data["source_ts"] = ev.TS
			data["remote"] = true
			if _, ok := data["attempt_id"]; !ok {
				data["attempt_id"] = p.AttemptID
			}
			if err := s.recordEventTx(ctx, tx, p.TaskID, ev.Type, data); err != nil {
				return err
			}
		}
		if p.Cursor == p.After {
			return nil
		}
		return s.recordEventTx(ctx, tx, p.TaskID, remoteActivityCursorEvent, map[string]any{"attempt_id": p.AttemptID, "from": from, "cursor": p.Cursor})
	})
}

// Activity delivery is a pull protocol backed by task_events, so reconnects
// and process restarts need no in-memory outbox. Advertisements are bounded
// and include no content; the terminal result carries the final watermark.
func (c *Core) advertiseActivities(ctx context.Context, task Task) func() {
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		var last int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-tick.C:
				cursor := c.store.activityCursor(ctx, task.TaskID)
				if cursor > last {
					last = cursor
					c.relayToParent(ctx, bus.MsgTaskProgress, task.Chain, bus.TaskProgressPayload{TaskID: task.TaskID, AttemptID: task.AttemptID, ActivityCursor: cursor})
				}
			}
		}
	}()
	return func() { close(done) }
}

// Re-advertise persisted history after reconnect, including tasks that ended
// while the link was down. No volatile queue is needed to remember a gap.
func (c *Core) advertisePeerActivities(ctx context.Context, peer string) {
	rows, err := c.store.db.QueryContext(ctx, "SELECT task_id,attempt_id,chain_json FROM tasks WHERE updated_at>=? ORDER BY updated_at DESC LIMIT 256", time.Now().Add(-7*24*time.Hour).Unix())
	if err != nil {
		return
	}
	type history struct {
		task, attempt string
		chain         []string
	}
	var histories []history
	for rows.Next() {
		var h history
		var chain string
		if rows.Scan(&h.task, &h.attempt, &chain) != nil {
			continue
		}
		if json.Unmarshal([]byte(chain), &h.chain) != nil {
			continue
		}
		if scheduler.Predecessor(h.chain, c.nodeID) == peer {
			histories = append(histories, h)
		}
	}
	_ = rows.Close()
	for _, h := range histories {
		cursor := c.store.activityCursor(ctx, h.task)
		if cursor > 0 {
			c.sendActivityMessage(ctx, bus.MsgTaskProgress, peer, bus.TaskProgressPayload{TaskID: h.task, AttemptID: h.attempt, ActivityCursor: cursor})
		}
	}
}
