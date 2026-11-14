// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const mergeLeaseSeconds = 60

// A review row has no execution heartbeat. Give its filesystem delivery an
// independent, attempt-scoped lease, and retain a receipt so repeated approval
// cannot reapply an already delivered workspace.
func (s *TaskStore) claimMergeLease(ctx context.Context, t Task, run string) (acquired, delivered bool, err error) {
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		acquired, delivered = false, false
		var attempt, state string
		if err := tx.QueryRowContext(ctx, `SELECT attempt_id,state FROM tasks WHERE task_id=?`, t.TaskID).Scan(&attempt, &state); err != nil {
			return err
		}
		if attempt != t.AttemptID || (state != StateRunning && state != StateReview) {
			return ErrConflict
		}
		var previousAttempt, previousRun string
		var expires int64
		var done bool
		err := tx.QueryRowContext(ctx, `SELECT attempt_id,run_id,expires_at,delivered FROM task_merge_leases WHERE task_id=?`, t.TaskID).Scan(&previousAttempt, &previousRun, &expires, &done)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && previousAttempt == attempt {
			if done {
				delivered = true
				return nil
			}
			if expires > s.now() {
				return nil
			}
		}
		// Remove only the expired merge's keys before making the replacement
		// lease live. Otherwise its own abandoned rows would look live again.
		if previousRun != "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM exec_locks WHERE task_id=? AND run_id=?`, t.TaskID, previousRun); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_merge_leases(task_id,attempt_id,run_id,expires_at,delivered) VALUES(?,?,?,?,0)
 ON CONFLICT(task_id) DO UPDATE SET attempt_id=excluded.attempt_id,run_id=excluded.run_id,expires_at=excluded.expires_at,delivered=0`, t.TaskID, attempt, run, s.now()+mergeLeaseSeconds)
		acquired = err == nil
		return err
	})
	return
}

func (s *TaskStore) renewMergeLease(ctx context.Context, t Task, run string, delivered bool) error {
	r, err := s.db.ExecContext(ctx, `UPDATE task_merge_leases SET expires_at=?,delivered=MAX(delivered,?) WHERE task_id=? AND attempt_id=? AND run_id=? AND expires_at>?
 AND EXISTS(SELECT 1 FROM tasks t WHERE t.task_id=task_merge_leases.task_id AND t.attempt_id=task_merge_leases.attempt_id AND t.state IN ('running','review'))`, s.now()+mergeLeaseSeconds, delivered, t.TaskID, t.AttemptID, run, s.now())
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return ErrConflict
	}
	return nil
}

func (c *Core) holdMergeLease(ctx context.Context, t Task) (context.Context, string, bool, func(), error) {
	run, err := newUUID()
	if err != nil {
		return ctx, "", false, nil, err
	}
	tick := time.NewTicker(execLockPollEvery)
	defer tick.Stop()
	for {
		acquired, delivered, err := c.store.claimMergeLease(ctx, t, run)
		if err != nil || delivered {
			return ctx, run, delivered, func() {}, err
		}
		if acquired {
			break
		}
		select {
		case <-ctx.Done():
			return ctx, run, false, nil, ctx.Err()
		case <-tick.C:
		}
	}
	leaseCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(mergeLeaseSeconds * time.Second / 3)
		defer tick.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-tick.C:
				if err := c.store.renewMergeLease(leaseCtx, t, run, false); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return leaseCtx, run, false, func() {
		cancel()
		<-stopped
		wCtx, stop := c.storeWriteCtx(context.Background())
		defer stop()
		if _, err := c.db.ExecContext(wCtx, `UPDATE task_merge_leases SET run_id='',expires_at=0 WHERE task_id=? AND attempt_id=? AND run_id=?`, t.TaskID, t.AttemptID, run); err != nil {
			c.logger.Warn("release merge lease", "task", t.TaskID, "err", err)
		}
	}, nil
}
