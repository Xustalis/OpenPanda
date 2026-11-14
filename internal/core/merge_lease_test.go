// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

func openKernelStores(t *testing.T) (*TaskStore, *TaskStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kernel.db")
	open := func() *sql.DB {
		t.Helper()
		db, err := storage.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := storage.Migrate(db); err != nil {
			t.Fatal(err)
		}
		return db
	}
	return NewTaskStore(open(), testLogger()), NewTaskStore(open(), testLogger())
}

func TestReviewMergeLeaseFencesOtherProcesses(t *testing.T) {
	s1, s2 := openKernelStores(t)
	ctx := context.Background()
	task := createTask(t, s1, "", "merge", "node")
	if _, err := s1.db.Exec(`UPDATE tasks SET state='review' WHERE task_id=?`, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if acquired, done, err := s1.claimMergeLease(ctx, task, "merge-1"); err != nil || !acquired || done {
		t.Fatal(acquired, done, err)
	}
	keys := []string{"project:merge"}
	if ok, err := s1.TryAcquireExecLocks(ctx, task.TaskID, "node", "merge-1", keys); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s2.TryAcquireExecLocks(ctx, "other", "node", "other-run", keys); err != nil || ok {
		t.Fatalf("review merge lock stolen: acquired=%v err=%v", ok, err)
	}
	if acquired, done, err := s2.claimMergeLease(ctx, task, "duplicate"); err != nil || acquired || done {
		t.Fatal(acquired, done, err)
	}
	if _, err := s1.db.Exec(`UPDATE task_merge_leases SET expires_at=0 WHERE task_id=?`, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if acquired, _, err := s2.claimMergeLease(ctx, task, "merge-2"); err != nil || !acquired {
		t.Fatal(acquired, err)
	}
	if ok, err := s2.TryAcquireExecLocks(ctx, task.TaskID, "node", "merge-2", keys); err != nil || !ok {
		t.Fatalf("expired merge deadlocked on its abandoned keys: %v %v", ok, err)
	}
	if err := s1.ReleaseExecLocks(ctx, task.TaskID, "merge-1"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s1.TryAcquireExecLocks(ctx, "other", "node", "other-run", keys); err != nil || ok {
		t.Fatalf("stale release removed replacement merge lock: %v %v", ok, err)
	}
	if err := s1.renewMergeLease(ctx, task, "merge-1", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale merge published receipt: %v", err)
	}
	if err := s2.renewMergeLease(ctx, task, "merge-2", true); err != nil {
		t.Fatal(err)
	}
	if err := s2.renewMergeLease(ctx, task, "merge-2", false); err != nil {
		t.Fatal(err)
	}
	if acquired, done, err := s1.claimMergeLease(ctx, task, "merge-3"); err != nil || acquired || !done {
		t.Fatalf("delivery receipt lost: acquired=%v done=%v err=%v", acquired, done, err)
	}
	if _, err := s1.db.Exec(`UPDATE tasks SET attempt_id='rotated' WHERE task_id=?`, task.TaskID); err != nil {
		t.Fatal(err)
	}
	if ok, err := s2.TryAcquireExecLocks(ctx, "other", "node", "other-run", keys); err != nil || !ok {
		t.Fatalf("stale attempt kept merge lock: %v %v", ok, err)
	}
}

func TestCapacityClaimAndParentResumeAreAtomicAcrossStores(t *testing.T) {
	s1, s2 := openKernelStores(t)
	ctx := context.Background()
	parent := createTask(t, s1, "", "parent", "node")
	if _, err := s1.db.Exec(`UPDATE tasks SET state='running' WHERE task_id=?`, parent.TaskID); err != nil {
		t.Fatal(err)
	}
	child := createTask(t, s1, "", "child", "node")
	if err := s1.SetQueueMeta(ctx, child.TaskID, PriorityNormal, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s1.Queue(ctx, child.TaskID, "node"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := s1.db.Exec(`UPDATE tasks SET state='queued' WHERE task_id=?`, child.TaskID); err != nil {
			t.Fatal(err)
		}
		if ok, err := s1.waitForChildren(ctx, parent); err != nil || !ok {
			t.Fatal(ok, err)
		}
		start := make(chan struct{})
		results := make(chan bool, 2)
		errs := make(chan error, 2)
		go func() {
			<-start
			ok, err := s1.resumeAfterChildren(ctx, parent, 1)
			errs <- err
			results <- ok
		}()
		go func() {
			<-start
			err := s2.ClaimLocal(ctx, child.TaskID, "node", 1)
			results <- err == nil
			if errors.Is(err, ErrConflict) {
				err = nil
			}
			errs <- err
		}()
		close(start)
		won := 0
		for j := 0; j < 2; j++ {
			if <-results {
				won++
			}
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		if won != 1 {
			t.Fatalf("capacity race admitted %d contenders, want 1", won)
		}
	}
}

func TestPlanObserverUsesPersistedRoundSession(t *testing.T) {
	c := newCore(t, "round-plan")
	ctx := context.Background()
	if err := c.store.BeginAskRound(ctx, "round", "round-session", "en"); err != nil {
		t.Fatal(err)
	}
	stages := []Task{{TaskID: "round-stage", AttemptID: "attempt", OwnerNode: c.nodeID, SessionID: "old-session"}}
	if err := c.store.createPlanStages(WithAskRound(ctx, "round"), stages, "round goal", nil, ""); err != nil {
		t.Fatal(err)
	}
	persisted, err := c.store.Get(ctx, stages[0].TaskID)
	if err != nil || persisted.SessionID != "round-session" || stages[0].SessionID != persisted.SessionID {
		t.Fatalf("observer session=%q persisted=%q err=%v", stages[0].SessionID, persisted.SessionID, err)
	}
}
