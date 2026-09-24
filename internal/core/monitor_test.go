package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestReconcileStopsOutOfBandTerminal is the zombie-execution half of "no
// silent stall": a task this process is executing can be cancelled or
// force-failed by another process sharing the store — the row goes terminal
// while the agent subprocess here keeps running. reconcileLocalWork is the
// pass that notices and kills it; without it the execution outlives its own
// verdict until the agent hard timeout.
func TestReconcileStopsOutOfBandTerminal(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c := NewCore(db, "worker", ledger.Card{}, 5, testLogger(), config.ModelConfig{})

	// A task this process is "executing": registered like a real run.
	tk, err := c.store.Create(ctx, "", "", "orphaned exec", "worker", []string{"worker"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.store.Queue(ctx, tk.TaskID, "worker"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, "worker", "worker"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, tk.TaskID, "worker"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()
	c.registerRunning(tk.TaskID, execCancel)
	c.pendingCtx.Store(tk.TaskID, &pendingContext{})

	// Another process cancels the row out of band.
	if err := c.store.Cancel(ctx, tk.TaskID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	c.reconcileLocalWork(ctx)

	select {
	case <-execCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("a task terminalized out of band must stop its local execution")
	}
	if _, ok := c.running.Load(tk.TaskID); ok {
		t.Fatal("cancelled execution should have been unregistered")
	}
	if _, ok := c.pendingCtx.Load(tk.TaskID); ok {
		t.Fatal("the parked-context entry must be dropped with its reservation")
	}
}

// TestReconcileLeavesLiveExecutions guards the other direction: a task whose
// row is still active is this process's own work — reconcile must not touch
// it no matter how stale it looks here.
func TestReconcileLeavesLiveExecutions(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c := NewCore(db, "worker", ledger.Card{}, 5, testLogger(), config.ModelConfig{})

	tk, err := c.store.Create(ctx, "", "", "live exec", "worker", []string{"worker"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.store.Queue(ctx, tk.TaskID, "worker"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.Dispatch(ctx, tk.TaskID, "worker", "worker"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.store.Accept(ctx, tk.TaskID, "worker"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()
	c.registerRunning(tk.TaskID, execCancel)
	c.pendingCtx.Store(tk.TaskID, &pendingContext{})

	c.reconcileLocalWork(ctx)

	select {
	case <-execCtx.Done():
		t.Fatal("a live task must never be reconciled away")
	default:
	}
	if _, ok := c.running.Load(tk.TaskID); !ok {
		t.Fatal("the live execution's registration must stay")
	}
}
