package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// queueTestCore builds a node with one tier-1 native ability so enqueued
// tasks can execute without an agent CLI.
func queueTestCore(t *testing.T) *Core {
	t.Helper()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "queue-node",
		ResourceClass: "Standard",
		Native: []ledger.NativeAbility{
			{ID: "sys:info", Command: "echo", Args: []string{"queue-ok"}},
		},
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2},
	}
	return NewCore(db, "queue-node", card, 5, testLogger(), config.ModelConfig{})
}

// TestEnqueueRunsViaScheduler verifies the full async path: Enqueue parks the
// task in queued, the scheduler adopts it, executes it, and it reaches done
// with the native command's output recorded.
func TestEnqueueRunsViaScheduler(t *testing.T) {
	c := queueTestCore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartQueueScheduler(ctx)

	in := TaskInput{Title: "echo via queue", Intent: "run echo", Requires: []string{"sys:info"}, Authorized: true}
	tk, err := c.Enqueue(ctx, in, DefaultQueueSpec())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if tk.State != StateQueued {
		t.Fatalf("state after enqueue = %s, want queued", tk.State)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := c.store.Get(ctx, tk.TaskID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.State == StateDone {
			if !strings.Contains(got.ResultJSON, "queue-ok") {
				t.Fatalf("result = %s, want stdout queue-ok", got.ResultJSON)
			}
			return
		}
		if got.State == StateFailed || got.State == StateReview {
			t.Fatalf("task ended in %s (result %s)", got.State, got.ResultJSON)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task never reached done within deadline")
}

// TestEnqueuePersistsQueueMeta verifies the scheduling metadata round-trips
// through the store, and that priority/seq updates land on the row.
func TestEnqueuePersistsQueueMeta(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	// No scheduler started: the task must stay queued forever here.

	q := DefaultQueueSpec()
	q.Priority = PriorityHigh
	q.SessionID = "sess-1"
	q.WorkDir = "/tmp/wt"
	q.ResourceKeys = []string{"node:opi"}
	tk, err := c.Enqueue(ctx, TaskInput{Title: "meta", Intent: "x", Requires: []string{"sys:info"}}, q)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	got, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Priority != PriorityHigh || got.SessionID != "sess-1" || got.WorkDir != "/tmp/wt" || !got.Scheduled {
		t.Fatalf("queue meta = %+v", got)
	}
	if len(got.ResourceKeys) != 1 || got.ResourceKeys[0] != "node:opi" {
		t.Fatalf("resource keys = %v", got.ResourceKeys)
	}

	if err := c.store.SetPriority(ctx, tk.TaskID, PriorityLow); err != nil {
		t.Fatalf("set priority: %v", err)
	}
	if err := c.store.SetSeq(ctx, tk.TaskID, 7); err != nil {
		t.Fatalf("set seq: %v", err)
	}
	got, _ = c.store.Get(ctx, tk.TaskID)
	if got.Priority != PriorityLow || got.Seq != 7 {
		t.Fatalf("after update: priority=%d seq=%d", got.Priority, got.Seq)
	}

	ready, err := c.store.ListReady(ctx)
	if err != nil || len(ready) != 1 {
		t.Fatalf("list ready = %v, %v", ready, err)
	}
}

// TestSameResourceSerializes verifies two tasks sharing a resource key never
// run at once: the second stays queued while the first holds the lock. The
// first task sleeps so the overlap window is observable.
func TestSameResourceSerializes(t *testing.T) {
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "queue-node",
		ResourceClass: "Standard",
		Native: []ledger.NativeAbility{
			{ID: "sys:info", Command: "echo", Args: []string{"ok"}},
			{ID: "sys:slow", Command: "sleep", Args: []string{"1"}},
		},
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 4},
	}
	c := NewCore(db, "queue-node", card, 5, testLogger(), config.ModelConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartQueueScheduler(ctx)

	q1 := DefaultQueueSpec()
	q1.ResourceKeys = []string{"node:opi"}
	first, err := c.Enqueue(ctx, TaskInput{Title: "slow", Intent: "x", Requires: []string{"sys:slow"}, Authorized: true}, q1)
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	q2 := DefaultQueueSpec()
	q2.ResourceKeys = []string{"node:opi"}
	second, err := c.Enqueue(ctx, TaskInput{Title: "fast", Intent: "x", Requires: []string{"sys:info"}, Authorized: true}, q2)
	if err != nil {
		t.Fatalf("enqueue second: %v", err)
	}

	// Wait until the slow task is actually running, then assert the fast one
	// is still queued despite free budget (same resource lock).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f, _ := c.store.Get(ctx, first.TaskID)
		if f.State == StateRunning {
			s, _ := c.store.Get(ctx, second.TaskID)
			if s.State != StateQueued {
				t.Fatalf("conflicting task state = %s, want queued while lock held", s.State)
			}
			// And once the slow task finishes, the fast one must run.
			for time.Now().Before(time.Now().Add(5 * time.Second)) {
				s, _ = c.store.Get(ctx, second.TaskID)
				if s.State == StateDone {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("second task never ran after lock freed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("first task never reached running")
}

// TestEnqueueRoutesToPeer verifies the queue path is cross-device: a task
// enqueued on a node that cannot execute it (no gpio:read) is claimed by the
// local scheduler, forwarded to a capable peer, executed there, and the
// result completes the origin's row. Queued tasks must be first-class network
// citizens — the same routing contract as Submit.
func TestEnqueueRoutesToPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	root := newCore(t, "queue-root", "127.0.0.1:17941")
	leaf := newCoreWithNative(t, "queue-leaf", "127.0.0.1:17942", ledger.NativeAbility{
		ID: "gpio:read", Command: "echo", Args: []string{"queue-gpio-ok"},
	})
	if err := root.Register(ctx); err != nil {
		t.Fatalf("root register: %v", err)
	}
	if err := leaf.Register(ctx); err != nil {
		t.Fatalf("leaf register: %v", err)
	}
	go func() { _ = root.Listen(ctx, "127.0.0.1:17941") }()
	go func() { _ = leaf.Listen(ctx, "127.0.0.1:17942") }()
	time.Sleep(200 * time.Millisecond)
	if err := root.DialPeer(ctx, "127.0.0.1:17942"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	root.StartQueueScheduler(ctx)

	tk, err := root.Enqueue(ctx, TaskInput{
		Title: "read gpio via queue", Intent: "read gpio", Requires: []string{"gpio:read"},
	}, DefaultQueueSpec())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := root.store.Get(ctx, tk.TaskID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.State == StateDone {
			if !strings.Contains(got.ResultJSON, "queue-gpio-ok") {
				t.Fatalf("result = %s, want queue-gpio-ok", got.ResultJSON)
			}
			leafRow, err := leaf.store.Get(ctx, tk.TaskID)
			if err != nil || leafRow.State != StateDone {
				t.Fatalf("leaf task state = %s (err %v), want done", leafRow.State, err)
			}
			return
		}
		if got.State == StateFailed || got.State == StateReview {
			t.Fatalf("task ended in %s (result %s)", got.State, got.ResultJSON)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("queued task never reached done via peer within deadline")
}
