// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"testing"
	"time"
)

func TestStartedRuntimeRecoversAfterTransientDatabaseFailure(t *testing.T) {
	s, source := newSyncTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		joined, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		if err := s.Wait(joined); err != nil {
			t.Error(err)
		}
	})
	waitUntil := func(condition func() bool) {
		t.Helper()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for !condition() {
			select {
			case <-s.done:
				t.Fatal("started runtime exited after a recoverable database failure")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-tick.C:
			}
		}
	}
	waitUntil(func() bool { return source.calls.Load() > 0 })
	if _, err := s.db.Exec(`ALTER TABLE task_connections RENAME TO temporarily_unavailable_connections`); err != nil {
		t.Fatal(err)
	}
	// The failed owner must release its lease before a successor can recover.
	waitUntil(func() bool {
		return scalar(t, s.db, `SELECT COUNT(*) FROM task_runtime_leases WHERE lease_key='sync'`) == "0"
	})
	if _, err := s.db.Exec(`ALTER TABLE temporarily_unavailable_connections RENAME TO task_connections`); err != nil {
		t.Fatal(err)
	}
	before := source.calls.Load()
	if err := s.RequestSync(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	waitUntil(func() bool { return source.calls.Load() > before })
}
