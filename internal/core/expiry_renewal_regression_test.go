// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"testing"
)

// The hook is the deterministic unlocked Get -> transactional UPDATE window.
// Both retry arms must lose to a committed renewal, without a cancellation.
func TestRegressionExpiryRenewalAtWriteBoundary(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("exhausted=%t/deadline=%t", exhausted, deadline), func(t *testing.T) {
				ctx := context.Background()
				c := newCore(t, "expiry-write-boundary")
				task := runToRunning(t, c, "expiry-write-boundary", "worker")
				if exhausted {
					for i := 0; i < maxPersistedRetries; i++ {
						if err := c.store.RecordEvent(ctx, task.TaskID, EvRetry, nil); err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, err := c.store.db.ExecContext(ctx, `UPDATE tasks SET lease_expires_at=? WHERE task_id=?`, c.store.now()-2, task.TaskID); err != nil {
					t.Fatal(err)
				}
				scanned, err := c.store.ScanExpired(ctx)
				if err != nil || len(scanned) != 1 {
					t.Fatalf("scan: %v %v", scanned, err)
				}
				if deadline {
					if _, err := c.store.db.ExecContext(ctx, `UPDATE tasks SET deadline_unix=? WHERE task_id=?`, c.store.now()-1, task.TaskID); err != nil {
						t.Fatal(err)
					}
				}
				hit := false
				c.store.preTxHook = func() {
					c.store.preTxHook = nil
					hit = true
					if err := c.store.SetLeaseAttempt(ctx, task.TaskID, 60000, task.AttemptID); err != nil {
						t.Fatal(err)
					}
				}
				live, cancel := context.WithCancel(ctx)
				defer cancel()
				unregister := c.registerRunning(task.TaskID, cancel)
				defer unregister()
				outcome := c.settleExpired(ctx, scanned[0])
				after, err := c.store.Get(ctx, task.TaskID)
				if err != nil || !hit {
					t.Fatalf("write boundary not exercised: hit=%t err=%v", hit, err)
				}
				if deadline {
					if outcome != ExpiryFailed || after.State != StateFailed || live.Err() == nil {
						t.Fatalf("deadline must end renewed work: %s %s %v", outcome, after.State, live.Err())
					}
				} else {
					if outcome != ExpirySkipped || after.State != StateRunning || after.AttemptID != task.AttemptID || live.Err() != nil {
						t.Fatalf("renewal lost: %s %+v %v", outcome, after, live.Err())
					}
					retries, err := c.store.HardRetries(ctx, task.TaskID)
					want := 0
					if exhausted {
						want = maxPersistedRetries
					}
					if err != nil || retries != want {
						t.Fatalf("renewal consumed a retry: %d want %d, %v", retries, want, err)
					}
				}
			})
		}
	}
}

func TestRegressionExpiryScanHonorsRenewedLease(t *testing.T) {
	for _, renew := range []bool{false, true} {
		name := "expired control"
		if renew {
			name = "renewed same attempt"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c := newCore(t, "lease-live-probe")
			task := runToRunning(t, c, "lease-live-probe", "long-running worker")
			if _, err := c.store.db.ExecContext(ctx, `UPDATE tasks SET lease_expires_at=? WHERE task_id=?`, c.store.now()-2, task.TaskID); err != nil {
				t.Fatal(err)
			}
			scanned, err := c.store.ScanExpired(ctx)
			if err != nil || len(scanned) != 1 {
				t.Fatalf("scan fixture: rows=%d error=%v", len(scanned), err)
			}
			if renew {
				if err := c.store.SetLeaseAttempt(ctx, task.TaskID, 60000, task.AttemptID); err != nil {
					t.Fatal(err)
				}
				live, err := c.store.Get(ctx, task.TaskID)
				if err != nil || live.LeaseExpires <= c.store.now() {
					t.Fatalf("renewal must be committed before settle: %d %v", live.LeaseExpires, err)
				}
			}
			liveCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			unregister := c.registerRunning(task.TaskID, cancel)
			defer unregister()
			outcome := c.settleExpired(ctx, scanned[0])
			after, err := c.store.Get(ctx, task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("renew=%t outcome=%s state=%s attempt_rotated=%t live_execution_cancelled=%t", renew, outcome, after.State, after.AttemptID != task.AttemptID, liveCtx.Err() != nil)
			if renew {
				if outcome != ExpirySkipped || after.State != StateRunning || after.AttemptID != task.AttemptID || liveCtx.Err() != nil {
					t.Error("stale expiry scan rerouted and cancelled an execution with an already-renewed lease")
				}
			} else if outcome != ExpiryRerouted || after.State != StateQueued || after.AttemptID == task.AttemptID || liveCtx.Err() == nil {
				t.Fatal("expired control must reroute and cancel the expired execution")
			}
		})
	}
}
