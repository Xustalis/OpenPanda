// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/projects"
)

func TestDispatchChildPreservesWorkDir(t *testing.T) {
	c := newCore(t, "child-dir")
	ctx := context.Background()
	p, _, _, err := c.createTask(ctx, TaskInput{Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	in := TaskInput{Title: "child", WorkDir: t.TempDir()}
	child, err := c.SpawnChildTask(ctx, p.TaskID, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DispatchChild(ctx, child, in); err != nil {
		t.Fatal(err)
	}
	got, err := c.store.Get(ctx, child.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkDir != in.WorkDir {
		t.Fatalf("workdir erased: got %q, want %q (keys %v)", got.WorkDir, in.WorkDir, got.ResourceKeys)
	}
}

func TestRunningParentDelegatesAtCapacityOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	c := NewCore(openTestDB(t), "capacity-one", ledger.Card{Device: "capacity-one", Capacity: ledger.Capacity{MaxConcurrent: 1}, Native: []ledger.NativeAbility{{ID: "test:child", Command: "sh", Args: []string{"-c", "echo child-ok"}, Tier: 1}}}, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Register(ctx); err != nil {
		t.Fatal(err)
	}
	p, _, _, err := c.createTask(ctx, TaskInput{Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.Queue(ctx, p.TaskID, c.nodeID); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Dispatch(ctx, p.TaskID, c.nodeID, c.nodeID); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Accept(ctx, p.TaskID, c.nodeID); err != nil {
		t.Fatal(err)
	}
	p, _ = c.store.Get(ctx, p.TaskID)
	c.StartQueueScheduler(ctx)
	var pending []pendingChild
	c.delegateChildren(ctx, p, []delegateRequest{{Title: "child", Intent: "x", Requires: []string{"test:child"}}}, &pending)
	rows, err := c.store.Children(context.Background(), p.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != StateDone {
		t.Fatalf("child starved behind waiting parent: %+v", rows)
	}
	waitArtifactWrites(t, c)
}

func TestStageJoinPreservesEditsAndDeletion(t *testing.T) {
	c := newCore(t, "join-files")
	withArtifactPool(t, c)
	ctx := context.Background()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "old-a")
	writeFile(t, filepath.Join(src, "b.txt"), "old-b")
	writeFile(t, filepath.Join(src, "removed.txt"), "remove-me")
	m, err := c.artifacts.PackDir(src)
	if err != nil {
		t.Fatal(err)
	}
	base := bus.ArtifactRef{Stage: worktreeArtifactStage, Hash: m.Hash, Source: c.nodeID}
	var refs []bus.ArtifactRef
	for _, name := range []string{"left", "right"} {
		task, _, _, err := c.createTask(ctx, TaskInput{Title: name})
		if err != nil {
			t.Fatal(err)
		}
		task.PlanID, task.StageID, task.Inputs = "merge-plan", name, []bus.ArtifactRef{base}
		dir := t.TempDir()
		if err := c.fetchStageInputs(ctx, task, dir); err != nil {
			t.Fatal(err)
		}
		if name == "left" {
			writeFile(t, filepath.Join(dir, "a.txt"), "new-a")
			if err := os.Remove(filepath.Join(dir, "removed.txt")); err != nil {
				t.Fatal(err)
			}
		} else {
			writeFile(t, filepath.Join(dir, "b.txt"), "new-b")
		}
		h, err := c.packStageOutput(ctx, task, dir)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, bus.ArtifactRef{Stage: name, Hash: h, Source: c.nodeID})
	}
	dir := t.TempDir()
	if err := c.fetchStageInputs(ctx, Task{PlanID: "merge-plan", StageID: "join", Inputs: append([]bus.ArtifactRef{base}, refs...)}, dir); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "new-a" {
		t.Errorf("left edit lost: %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "b.txt")); got != "new-b" {
		t.Errorf("right edit lost: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "removed.txt")); !os.IsNotExist(err) {
		t.Errorf("deleted file resurrected: %v", err)
	}
}

func TestIsolatedConflictBlocksCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	main := t.TempDir()
	writeFile(t, filepath.Join(main, "shared.txt"), "baseline")
	c := newCoreWithNative(t, "merge-block", ledger.NativeAbility{ID: "test:conflict", Command: "sh", Args: []string{"-c", `printf child > shared.txt; printf main > "$1/shared.txt"`, "conflict", main}, Tier: 1})
	ps := projects.NewStore(c.db)
	if _, err := ps.Create("proj", main, "conflict"); err != nil {
		t.Fatal(err)
	}
	c.SetProjectStores(ps, t.TempDir())
	withArtifactPool(t, c)
	in := TaskInput{Title: "conflict", Intent: "x", Project: "proj", Isolate: true, Requires: []string{"test:conflict"}, Authorized: true}
	task, _, _, err := c.createTask(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.SetQueueMeta(context.Background(), task.TaskID, PriorityNormal, "", "", childResourceKeys(task.TaskID, in)); err != nil {
		t.Fatal(err)
	}
	task, res, err := c.runLocal(context.Background(), task, in)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != StateReview || res.OK || task.ApprovalDisposition != ApprovalNeedsChangedInput {
		t.Fatalf("conflict certified complete: state=%s disposition=%s result=%+v", task.State, task.ApprovalDisposition, res)
	}
	if res.OutputArtifact == "" {
		t.Fatal("conflicted result artifact missing")
	}
	dir, _ := c.attachedWorkDir(task.TaskID)
	if got := readFile(t, filepath.Join(dir, "shared.txt")); got != "child" {
		t.Fatal(got)
	}
	if got := readFile(t, filepath.Join(main, "shared.txt")); got != "main" {
		t.Fatal(got)
	}
}

func TestParentWaitAttemptLifecycle(t *testing.T) {
	c := newCore(t, "wait-lifecycle")
	ctx := context.Background()
	p, _, _, err := c.createTask(ctx, TaskInput{Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.ExecContext(ctx, "UPDATE tasks SET state='running' WHERE task_id=?", p.TaskID); err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		for _, count := range []func(context.Context, string) (int, error){c.store.CountActive, c.store.CountScheduledActive} {
			n, err := count(ctx, c.nodeID)
			if err != nil || n != want {
				t.Fatalf("count=%d want=%d err=%v", n, want, err)
			}
		}
	}
	check(1)
	if ok, err := c.store.waitForChildren(ctx, p); err != nil || !ok {
		t.Fatal(ok, err)
	}
	check(0)
	if _, err := c.db.ExecContext(ctx, "UPDATE tasks SET attempt_id='new-attempt' WHERE task_id=?", p.TaskID); err != nil {
		t.Fatal(err)
	}
	check(1)
	if ok, err := c.store.resumeAfterChildren(ctx, p, 1); err == nil || ok {
		t.Fatalf("stale attempt resumed: ok=%v err=%v", ok, err)
	}
	p, _ = c.store.Get(ctx, p.TaskID)
	if ok, err := c.store.waitForChildren(ctx, p); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := c.store.Recover(ctx, c.nodeID); err != nil {
		t.Fatal(err)
	}
	var waiting string
	if err := c.db.QueryRowContext(ctx, "SELECT children_wait_attempt FROM tasks WHERE task_id=?", p.TaskID).Scan(&waiting); err != nil || waiting != "" {
		t.Fatal(waiting, err)
	}
	check(0)
}

func TestPlanPublishIsAtomicForObservers(t *testing.T) {
	c := newCore(t, "atomic-plan")
	seen := 0
	ctx := WithTaskObserver(context.Background(), func(task Task) {
		seen++
		rows, err := c.store.PlanStages(context.Background(), task.PlanID)
		if err != nil || len(rows) != 2 {
			t.Errorf("observer saw partial graph: rows=%d err=%v", len(rows), err)
		}
		for _, row := range rows {
			if !row.Scheduled || row.SpecJSON == "" || len(row.ResourceKeys) == 0 {
				t.Errorf("unconfigured stage visible: %+v", row)
			}
		}
	})
	if _, err := c.StartPlan(ctx, plan.Plan{Goal: "kernel regression", Stages: []plan.Stage{{ID: "a", Intent: "a"}, {ID: "b", Intent: "b", Needs: []string{"a"}}}}, DefaultQueueSpec()); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Fatal(seen)
	}
}

func TestPlanPublishRollsBackWholeGraph(t *testing.T) {
	c := newCore(t, "atomic-rollback")
	if _, err := c.db.Exec("CREATE TRIGGER reject_plan_stage BEFORE INSERT ON tasks WHEN NEW.stage_id='b' BEGIN SELECT RAISE(ABORT,'injected publish failure'); END"); err != nil {
		t.Fatal(err)
	}
	id, err := c.StartPlan(context.Background(), plan.Plan{Goal: "kernel regression", Stages: []plan.Stage{{ID: "a", Intent: "a"}, {ID: "b", Intent: "b"}}}, DefaultQueueSpec())
	if err == nil || id != "" || !strings.Contains(err.Error(), "injected publish failure") {
		t.Fatal(id, err)
	}
	for _, table := range []string{"tasks", "task_events"} {
		var n int
		if err := c.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial %s rows=%d err=%v", table, n, err)
		}
	}
}

func TestDelegateBatchUsesImmutableSnapshot(t *testing.T) {
	c := newCore(t, "batch-snapshot")
	main := t.TempDir()
	writeFile(t, filepath.Join(main, "a.txt"), "fork")
	ps := projects.NewStore(c.db)
	if _, err := ps.Create("proj", main, "snapshot"); err != nil {
		t.Fatal(err)
	}
	c.SetProjectStores(ps, t.TempDir())
	ctx, cleanup, err := c.snapshotDelegateBatch(context.Background(), Task{Project: "proj"}, []delegateRequest{{Isolate: true}, {Isolate: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	writeFile(t, filepath.Join(main, "a.txt"), "later")
	for _, name := range []string{"one", "two"} {
		in := TaskInput{Title: name, Project: "proj", Isolate: true}
		child, _, _, err := c.createTask(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.seedDelegateSnapshot(ctx, child, in); err != nil {
			t.Fatal(err)
		}
		dir, err := c.setupIsolatedWorkDir(ctx, child, main)
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(dir, "a.txt")); got != "fork" {
			t.Fatalf("sibling did not use batch source: %q", got)
		}
	}
}

func TestInlineIsolatedConflictDoesNotWaitOnOwnLease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	main := t.TempDir()
	writeFile(t, filepath.Join(main, "a.txt"), "fork")
	c := newCoreWithNative(t, "inline-isolation", ledger.NativeAbility{ID: "test:iso", Command: "sh", Args: []string{"-c", `printf child > a.txt; printf main > "$1/a.txt"`, "iso", main}, Tier: 1})
	ps := projects.NewStore(c.db)
	if _, err := ps.Create("proj", main, "inline"); err != nil {
		t.Fatal(err)
	}
	c.SetProjectStores(ps, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	task, res, err := c.SubmitLocal(ctx, TaskInput{Title: "inline", Project: "proj", Isolate: true, Requires: []string{"test:iso"}})
	if err != nil || task.State != StateReview || res.OK {
		t.Fatalf("state=%s result=%+v err=%v", task.State, res, err)
	}
	if ctx.Err() != nil {
		t.Fatal("merge waited on own execution lease")
	}
}

func BenchmarkIsolatedTargetValidation(b *testing.B) {
	c := &Core{}
	dir := b.TempDir()
	content := []byte(strings.Repeat("x", 4096))
	for i := range 1000 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%04d", i)), content, 0644); err != nil {
			b.Fatal(err)
		}
	}
	// Host-state filtering needs no database and no runtime paths here.
	b.Run("before_full_tree", func(b *testing.B) {
		for b.Loop() {
			if _, err := c.hashWorkTree(dir); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("after_changed_path", func(b *testing.B) {
		for b.Loop() {
			if _, err := isolatedPathHash(dir, "file-0000"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestRemoteIsolatedApprovalWaitsForDelivery(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict_%v", conflict), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			entry, worker := newCore(t, "approval-origin"), newCore(t, "approval-worker")
			startPair(t, ctx, entry, worker)
			id, err := newUUID()
			if err != nil {
				t.Fatal(err)
			}
			seedAcceptWorkPair(t, ctx, entry, worker, id)
			original, _ := entry.store.Get(ctx, id)
			remote, _ := worker.store.Get(ctx, id)
			if err := worker.store.AdoptAttempt(ctx, id, remote.AttemptID, original.AttemptID); err != nil {
				t.Fatal(err)
			}
			for _, c := range []*Core{entry, worker} {
				if err := c.store.SetIsolate(ctx, id, true); err != nil {
					t.Fatal(err)
				}
			}
			main := t.TempDir()
			writeFile(t, filepath.Join(main, "a.txt"), "fork")
			ps := projects.NewStore(worker.db)
			if _, err := ps.Create("proj", main, "delivery"); err != nil {
				t.Fatal(err)
			}
			worker.SetProjectStores(ps, t.TempDir())
			remote, _ = worker.store.Get(ctx, id)
			dir, err := worker.setupIsolatedWorkDir(ctx, remote, main)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, "a.txt"), "child")
			if conflict {
				writeFile(t, filepath.Join(main, "a.txt"), "main")
			}
			task, _, err := entry.ResumeApproved(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			// The origin has no executor workspace. It waits for a merge receipt,
			// rather than interpreting that absence as success or baseline failure.
			if task.State != StateReview {
				t.Fatalf("origin completed before receipt: %+v", task)
			}
			want := StateDone
			if conflict {
				want = StateReview
			}
			for ctx.Err() == nil {
				row, err := entry.store.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if (!conflict && row.State == want) || (conflict && row.ApprovalDisposition == ApprovalNeedsChangedInput) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			row, _ := entry.store.Get(context.Background(), id)
			if row.State != want || (conflict && row.ApprovalDisposition != ApprovalNeedsChangedInput) {
				t.Fatalf("delivery did not reconcile: %+v", row)
			}
			if conflict {
				if got := readFile(t, filepath.Join(dir, "a.txt")); got != "child" {
					t.Fatal(got)
				}
				if err := entry.store.Approve(ctx, id); err == nil {
					t.Fatal("conflicted remote delivery approved")
				}
			} else if got := readFile(t, filepath.Join(main, "a.txt")); got != "child" {
				t.Fatal(got)
			}
		})
	}
}

func TestIsolatedMissingBaselineBlocks(t *testing.T) {
	c := newCore(t, "baseline-missing")
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	result := c.mergeIsolatedRun(context.Background(), Task{TaskID: id, Project: "proj"})
	if !result.Blocked || result.Note == "" {
		t.Fatalf("missing baseline accepted: %+v", result)
	}
}
