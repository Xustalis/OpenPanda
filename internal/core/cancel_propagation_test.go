package core

import (
	"context"
	"testing"
	"time"

	"github.com/xenith/panda/internal/ledger"
)

// slowNativeAbility is a long-running native capability used to catch a task
// mid-flight: the cancel has to land while the executor is still working.
func slowNativeAbility() ledger.NativeAbility {
	return ledger.NativeAbility{ID: "slow:run", Command: "sleep", Args: []string{"30"}}
}

// TestCancelPropagatesDownstream verifies P2-3: cancelling a delegated task at
// the delegator reaches the executor, whose local copy transitions to
// cancelled instead of running to completion and reporting into a void.
func TestCancelPropagatesDownstream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	root := newCore(t, "root", "127.0.0.1:17961")
	leaf := newCoreWithNative(t, "leaf", "127.0.0.1:17962", slowNativeAbility())

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("startup: %v", err)
		}
	}
	must(root.Register(ctx))
	must(leaf.Register(ctx))
	go func() { _ = root.Listen(ctx, "127.0.0.1:17961") }()
	go func() { _ = leaf.Listen(ctx, "127.0.0.1:17962") }()
	time.Sleep(200 * time.Millisecond)
	must(root.DialPeer(ctx, "127.0.0.1:17962"))
	time.Sleep(300 * time.Millisecond)

	// Submit blocks until a result arrives; run it in the background so the
	// test can cancel mid-flight.
	in := TaskInput{Title: "slow", Intent: "slow", Requires: []string{"slow:run"}}
	type outcome struct {
		task Task
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		tk, _, err := root.Submit(ctx, in)
		done <- outcome{tk, err}
	}()

	// Wait until the leaf is actually running the task, then cancel at root.
	var taskID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := leaf.store.ListByState(ctx, StateRunning)
		if err == nil && len(tasks) > 0 {
			taskID = tasks[0].TaskID
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if taskID == "" {
		t.Fatalf("leaf never started the task")
	}

	if err := root.CancelTree(ctx, taskID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// The cancel must propagate: the leaf's copy leaves running for cancelled.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := leaf.store.Get(ctx, taskID)
		if err == nil && tk.State == StateCancelled {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	tk, _ := leaf.store.Get(ctx, taskID)
	t.Fatalf("leaf task state = %s, want cancelled (cancel did not propagate)", tk.State)
}
