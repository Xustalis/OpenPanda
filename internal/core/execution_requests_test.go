// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/todos"
)

func businessRequest() ExecutionRequest {
	return ExecutionRequest{RequestID: "request-one", TaskKey: "local/local/local/1", TriggerKey: "one", Purpose: "manual", ApprovalMode: "before_submit"}
}

func requireRowCount(t *testing.T, c *Core, table string, want int) {
	t.Helper()
	var n int
	if err := c.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != want {
		t.Fatalf("%s rows=%d want=%d err=%v", table, n, want, err)
	}
}

func TestExecutionRequestConcurrentEnqueue(t *testing.T) {
	c := queueTestCore(t)
	const callers = 16
	results := make(chan EnqueueResult, callers)
	errorsCh := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			r, err := c.EnqueueRequest(context.Background(), businessRequest(), TaskInput{Title: "one business task", Requires: []string{"sys:info"}}, DefaultQueueSpec())
			results <- r
			errorsCh <- err
		})
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id string
	fresh := 0
	for r := range results {
		if id == "" {
			id = r.Task.TaskID
		}
		if r.Task.TaskID != id || r.Task.State != StateQueued || !r.Task.Scheduled {
			t.Fatalf("inconsistent result: %+v", r)
		}
		if !r.AlreadyStarted {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh publications=%d", fresh)
	}
	requireRowCount(t, c, "tasks", 1)
	requireRowCount(t, c, "execution_requests", 1)
	requireRowCount(t, c, "execution_links", 1)
	requireRowCount(t, c, "task_events", 2)
}

func TestExecutionRequestPublicationRollbackAndRecovery(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	todo, err := todos.NewStore(c.db).Create(ctx, todos.CreateInput{Title: "durable intention"})
	if err != nil {
		t.Fatal(err)
	}
	req := businessRequest()
	req.LocalTodoID, req.InputRevision = todo.ID, todos.ExecutionRevision(todo)
	req.TaskKey = fmt.Sprintf("local/local/local/%d", todo.ID)
	if _, err := c.db.Exec(`CREATE TRIGGER reject_execution_link BEFORE INSERT ON execution_links BEGIN SELECT RAISE(ABORT,'injected link failure'); END`); err != nil {
		t.Fatal(err)
	}
	in := TaskInput{Title: todo.Title}
	if _, err := c.EnqueueRequest(ctx, req, in, DefaultQueueSpec()); err == nil {
		t.Fatal("publication should fail")
	}
	for _, table := range []string{"tasks", "task_events", "execution_links"} {
		requireRowCount(t, c, table, 0)
	}
	requireRowCount(t, c, "execution_requests", 1)
	var state string
	if err := c.db.QueryRow(`SELECT state FROM execution_requests`).Scan(&state); err != nil || state != "preparing" {
		t.Fatal(state, err)
	}
	got, err := todos.NewStore(c.db).Get(ctx, todo.ID)
	if err != nil || got.TaskID != "" || got.Status != todos.StatusPending {
		t.Fatal(got, err)
	}
	if _, err := c.db.Exec(`DROP TRIGGER reject_execution_link`); err != nil {
		t.Fatal(err)
	}
	// A new Core models restart; it must use the previously persisted request.
	restarted := NewCore(c.db, c.nodeID, c.card, 5, testLogger(), c.model)
	r, err := restarted.EnqueueRequest(ctx, req, in, DefaultQueueSpec())
	if err != nil || r.AlreadyStarted {
		t.Fatal(r, err)
	}
	again, err := restarted.EnqueueRequest(ctx, req, in, DefaultQueueSpec())
	if err != nil || !again.AlreadyStarted || again.Task.TaskID != r.Task.TaskID {
		t.Fatal(again, err)
	}
	got, err = todos.NewStore(c.db).Get(ctx, todo.ID)
	if err != nil || got.TaskID != r.Task.TaskID || got.Status != todos.StatusRunning {
		t.Fatal(got, err)
	}
	requireRowCount(t, c, "tasks", 1)
}

func TestExecutionRequestChangedInputRejected(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	req := businessRequest()
	if _, err := c.EnqueueRequest(ctx, req, TaskInput{Title: "original"}, DefaultQueueSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnqueueRequest(ctx, req, TaskInput{Title: "different"}, DefaultQueueSpec()); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	requireRowCount(t, c, "tasks", 1)
}

func TestExecutionRequestDistinctRequestsShareOneActiveSlot(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Go(func() {
			req := businessRequest()
			req.RequestID = fmt.Sprintf("run-%d", i)
			req.TriggerKey = req.RequestID
			_, err := c.EnqueueRequest(ctx, req, TaskInput{Title: "same business task"}, DefaultQueueSpec())
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	published := 0
	for err := range errs {
		if err == nil {
			published++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if published != 1 {
		t.Fatalf("published %d overlapping requests", published)
	}
	requireRowCount(t, c, "tasks", 1)
	requireRowCount(t, c, "execution_links", 1)
	// Failed attempts remain retryable in the kernel and must retain the slot.
	if _, err := c.db.Exec(`UPDATE tasks SET state='failed'`); err != nil {
		t.Fatal(err)
	}
	req := businessRequest()
	req.RequestID = "after-failure"
	req.TriggerKey = req.RequestID
	if _, err := c.EnqueueRequest(ctx, req, TaskInput{Title: "same business task"}, DefaultQueueSpec()); !errors.Is(err, ErrConflict) {
		t.Fatalf("retryable attempt: %v", err)
	}
	if _, err := c.db.Exec(`UPDATE tasks SET state='cancelled'`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnqueueRequest(ctx, req, TaskInput{Title: "same business task"}, DefaultQueueSpec()); err != nil {
		t.Fatal(err)
	}
	requireRowCount(t, c, "tasks", 2)
}

func TestExecutionRequestPreparationFailureHasNoRunnableTask(t *testing.T) {
	c := queueTestCore(t)
	q := DefaultQueueSpec()
	q.WorkDir = "/"
	if _, err := c.EnqueueRequest(context.Background(), businessRequest(), TaskInput{Title: "unsafe workspace"}, q); err == nil {
		t.Fatal("expected invalid workspace")
	}
	requireRowCount(t, c, "tasks", 0)
	requireRowCount(t, c, "execution_requests", 1)
}

func TestExecutionRequestRechecksLocalInputAtPublication(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	todo, err := todos.NewStore(c.db).Create(ctx, todos.CreateInput{Title: "original"})
	if err != nil {
		t.Fatal(err)
	}
	req := businessRequest()
	req.LocalTodoID, req.InputRevision = todo.ID, todos.ExecutionRevision(todo)
	if _, err := c.db.Exec(`CREATE TRIGGER edit_intention BEFORE INSERT ON tasks BEGIN UPDATE todos SET title='changed'; END`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnqueueRequest(ctx, req, TaskInput{Title: "original"}, DefaultQueueSpec()); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	requireRowCount(t, c, "tasks", 0)
	requireRowCount(t, c, "execution_links", 0)
}

func TestExecutionRequestPlanAtomicBinding(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	p := plan.Plan{Goal: "business Plan", Stages: []plan.Stage{{ID: "a", Intent: "a"}, {ID: "b", Intent: "b", Needs: []string{"a"}}}}
	req := businessRequest()
	if _, err := c.db.Exec(`CREATE TRIGGER reject_plan_link BEFORE INSERT ON execution_links WHEN NEW.stage_id='b' BEGIN SELECT RAISE(ABORT,'injected Plan link failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartPlanRequest(ctx, req, p, DefaultQueueSpec()); err == nil {
		t.Fatal("expected atomic rollback")
	}
	for _, table := range []string{"tasks", "task_events", "execution_links"} {
		requireRowCount(t, c, table, 0)
	}
	if _, err := c.db.Exec(`DROP TRIGGER reject_plan_link`); err != nil {
		t.Fatal(err)
	}
	r, err := c.StartPlanRequest(ctx, req, p, DefaultQueueSpec())
	if err != nil || r.PlanID == "" || r.AlreadyStarted {
		t.Fatal(r, err)
	}
	again, err := c.StartPlanRequest(ctx, req, p, DefaultQueueSpec())
	if err != nil || !again.AlreadyStarted || again.PlanID != r.PlanID {
		t.Fatal(again, err)
	}
	requireRowCount(t, c, "tasks", 2)
	requireRowCount(t, c, "execution_links", 2)
}
