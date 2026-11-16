// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
)

// countExecutionSlots is used inside admission transactions, so a parent
// resuming and another process claiming a queued child cannot take the last
// slot together. Waiting parents keep their state, heartbeat and actuator locks.
func countExecutionSlots(ctx context.Context, tx *sql.Tx, node string) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `
 SELECT COUNT(*) FROM tasks t WHERE
 (t.owner_node=? AND t.state IN ('running','waiting_context') AND (t.children_wait_attempt='' OR t.children_wait_attempt != t.attempt_id))
 OR (t.scheduled=1 AND t.state='dispatched' AND COALESCE(
 (SELECT json_extract(e.data_json,'$.target') FROM task_events e
 WHERE e.task_id=t.task_id AND e.type=? ORDER BY e.id DESC LIMIT 1),?)=?)`, node, EvDelegate, node, node).Scan(&n)
	return n, err
}

func (s *TaskStore) waitForChildren(ctx context.Context, parent Task, exclusive ...bool) (bool, error) {
	if parent.AttemptID == "" {
		return false, nil
	}
	query := `UPDATE tasks SET children_wait_attempt=?
 WHERE task_id=? AND attempt_id=? AND state='running' AND owner_node=?`
	if len(exclusive) > 0 && exclusive[0] {
		query += " AND (children_wait_attempt='' OR children_wait_attempt != attempt_id)"
	}
	res, err := s.db.ExecContext(ctx, query, parent.AttemptID, parent.TaskID, parent.AttemptID, parent.OwnerNode)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *TaskStore) resumeAfterChildren(ctx context.Context, parent Task, capacity int) (bool, error) {
	resumed := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var waiting string
		if err := tx.QueryRowContext(ctx, `SELECT children_wait_attempt FROM tasks WHERE task_id=? AND attempt_id=? AND state='running' AND owner_node=?`, parent.TaskID, parent.AttemptID, parent.OwnerNode).Scan(&waiting); err != nil {
			return err
		}
		if waiting != parent.AttemptID {
			return ErrConflict
		}
		if capacity > 0 {
			n, err := countExecutionSlots(ctx, tx, parent.OwnerNode)
			if err != nil {
				return err
			}
			if n >= capacity {
				return nil
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE tasks SET children_wait_attempt='' WHERE task_id=? AND attempt_id=?`, parent.TaskID, parent.AttemptID)
		resumed = err == nil
		return err
	})
	return resumed, err
}

func (c *Core) yieldForChildren(ctx context.Context, parent Task, exclusive ...bool) (func() error, error) {
	yielded, err := c.store.waitForChildren(ctx, parent, exclusive...)
	if err != nil {
		return nil, fmt.Errorf("yield child capacity: %w", err)
	}
	if !yielded && len(exclusive) > 0 && exclusive[0] {
		return nil, fmt.Errorf("parent attempt cannot begin another child wait")
	}
	keys := queue.DefaultKeys(parent.ResourceKeys, parent.Project, parent.WorkDir, parent.TaskID)
	held := c.queueReleaseFor(parent.TaskID)
	dbKeys, dbHeld := c.execReleaseFor(parent.TaskID)
	c.queueWake()
	return func() error {
		if yielded {
			tick := time.NewTicker(execLockPollEvery)
			defer tick.Stop()
			for {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				release, ok := c.reserveCapacity(ctx)
				if ok {
					resumed, rerr := c.store.resumeAfterChildren(ctx, parent, c.executionConcurrency())
					release()
					if rerr != nil {
						c.logger.Warn("resume parent capacity", "task", parent.TaskID, "err", rerr)
						c.cancelRunning(parent.TaskID)
						return rerr
					}
					if resumed {
						break
					}
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-tick.C:
				}
			}
		}
		if held {
			c.queueAcquireFor(ctx, parent.TaskID, keys)
		}
		if dbHeld {
			if err := c.execAcquireFor(ctx, parent.TaskID, dbKeys); err != nil {
				c.logger.Warn("resume parent locks", "task", parent.TaskID, "err", err)
				c.cancelRunning(parent.TaskID)
				return err
			}
		}
		return ctx.Err()
	}, nil
}
