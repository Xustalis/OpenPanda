package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xenith/panda/internal/ledger"
)

// TestDelegationMetricRecorded verifies that a successful remote delegation
// writes one row into delegation_metrics on the delegator, capturing success,
// latency, and abilities.
func TestDelegationMetricRecorded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entryAddr, workerAddr := "127.0.0.1:17951", "127.0.0.1:17952"
	// Entry must NOT have sys:info so the scheduler is forced to delegate.
	entry := newCoreWithNative(t, "entry-metric", entryAddr, ledger.NativeAbility{
		ID: "noop", Command: "echo", Args: []string{"noop"},
	})
	worker := newCoreWithNative(t, "worker-metric", workerAddr, ledger.NativeAbility{
		ID: "sys:info", Command: "echo", Args: []string{"ok"},
	})
	startPair(t, ctx, entry, worker, entryAddr, workerAddr)

	in := TaskInput{Title: "metric task", Intent: "run sys:info", Requires: []string{"sys:info"}}
	task, result, err := entry.Submit(ctx, in)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.OK {
		t.Fatalf("result not ok: %+v", result)
	}

	// The delegator should have recorded one metric for the edge entry -> worker.
	metrics, err := entry.store.ListDelegationMetrics(ctx)
	if err != nil {
		t.Fatalf("list metrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("got %d metrics, want 1", len(metrics))
	}
	m := metrics[0]
	if m.TaskID != task.TaskID {
		t.Fatalf("metric task_id = %q, want %q", m.TaskID, task.TaskID)
	}
	if m.Delegator != string(entry.nodeID) {
		t.Fatalf("metric delegator = %q, want %q", m.Delegator, entry.nodeID)
	}
	if m.Executor != string(worker.nodeID) {
		t.Fatalf("metric executor = %q, want %q", m.Executor, worker.nodeID)
	}
	if !m.Success {
		t.Fatalf("metric success = false, want true")
	}
	if m.LatencyMs < 0 {
		t.Fatalf("metric latency = %d, want >= 0", m.LatencyMs)
	}
	if !strings.Contains(m.AbilitiesJSON, "sys:info") {
		t.Fatalf("metric abilities = %q, want sys:info", m.AbilitiesJSON)
	}

	// The worker is the executor; it should not have recorded a metric.
	workerMetrics, err := worker.store.ListDelegationMetrics(ctx)
	if err != nil {
		t.Fatalf("list worker metrics: %v", err)
	}
	if len(workerMetrics) != 0 {
		t.Fatalf("worker recorded %d metrics, want 0", len(workerMetrics))
	}
}

// TestDelegationMetricRecordsFailure verifies that a failed remote execution
// still writes a metric row with success=false.
func TestDelegationMetricRecordsFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entryAddr, workerAddr := "127.0.0.1:17961", "127.0.0.1:17962"
	entry := newCoreWithNative(t, "entry-fail", entryAddr, ledger.NativeAbility{
		ID: "noop", Command: "echo", Args: []string{"noop"},
	})
	worker := newCoreWithNative(t, "worker-fail", workerAddr, ledger.NativeAbility{
		ID: "sys:info", Command: "false",
	})
	startPair(t, ctx, entry, worker, entryAddr, workerAddr)

	in := TaskInput{Title: "failing task", Intent: "run sys:info", Requires: []string{"sys:info"}}
	task, result, err := entry.Submit(ctx, in)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.OK {
		t.Fatalf("expected task failure, got ok")
	}

	metrics, err := entry.store.ListDelegationMetrics(ctx)
	if err != nil {
		t.Fatalf("list metrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("got %d metrics, want 1", len(metrics))
	}
	if metrics[0].TaskID != task.TaskID {
		t.Fatalf("metric task_id = %q, want %q", metrics[0].TaskID, task.TaskID)
	}
	if metrics[0].Success {
		t.Fatalf("metric success = true, want false")
	}
}
