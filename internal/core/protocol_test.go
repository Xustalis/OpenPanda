package core

import (
	"context"
	"testing"
	"time"

	"github.com/xenith/panda/internal/bus"
	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/ledger"
)

// TestTwoNodeProtocol spins up two cores over real WebSocket on loopback and
// verifies: hello exchange registers peers, task_delegate is routed, and the
// result returns to the delegator.
func TestTwoNodeProtocol(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry", "127.0.0.1:17836")
	worker := newCore(t, "worker", "127.0.0.1:17837")

	if err := entry.Register(ctx); err != nil {
		t.Fatalf("register entry: %v", err)
	}
	if err := worker.Register(ctx); err != nil {
		t.Fatalf("register worker: %v", err)
	}

	entryDone := make(chan error, 1)
	workerDone := make(chan error, 1)
	go func() { entryDone <- entry.Listen(ctx, "127.0.0.1:17836") }()
	go func() { workerDone <- worker.Listen(ctx, "127.0.0.1:17837") }()

	// Wait for both servers to accept.
	time.Sleep(200 * time.Millisecond)

	if err := entry.DialPeer(ctx, "127.0.0.1:17837"); err != nil {
		t.Fatalf("dial worker: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Delegate a task from entry to worker.
	env, err := bus.NewEnvelope(bus.MsgTaskDelegate, "entry", "msg-1", bus.TaskDelegatePayload{
		TaskID:   "task-proto-1",
		Title:    "say hello",
		Intent:   "print hello from worker",
		Requires: []string{"sys:info"},
		Chain:    []string{"entry"},
	})
	if err != nil {
		t.Fatalf("build delegate: %v", err)
	}
	if err := entry.sendTo("worker", env); err != nil {
		t.Fatalf("send delegate: %v", err)
	}

	// Wait for the result to be stored on the entry side.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := entry.store.Get(ctx, "task-proto-1")
		if err == nil && tk.State == StateDone {
			if tk.ResultJSON == "" {
				t.Fatalf("task done but no result recorded")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Diagnose on failure.
	if tk, err := entry.store.Get(ctx, "task-proto-1"); err == nil {
		evs, _ := entry.store.Events(ctx, tk.TaskID)
		t.Logf("entry task state=%s events=%d result=%q", tk.State, len(evs), tk.ResultJSON)
		for _, e := range evs {
			t.Logf("  event %s data=%s", e.Type, e.DataJSON)
		}
	} else {
		t.Logf("entry has no task: %v", err)
	}
	if tk, err := worker.store.Get(ctx, "task-proto-1"); err == nil {
		t.Logf("worker task state=%s", tk.State)
	} else {
		t.Logf("worker has no task: %v", err)
	}
	t.Fatalf("task did not reach done on entry within deadline")
}

func newCore(t *testing.T, id, addr string) *Core {
	t.Helper()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        id,
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:info", Command: "uname"}},
		Capacity:      ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	return NewCore(db, id, card, 5, verboseTestLogger(), config.ModelConfig{})
}
