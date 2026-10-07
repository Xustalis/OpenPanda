// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"slices"
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

// TestEnqueueDerivesHardwareKey pins the actuator mutex-key contract
// (2026-09-29 audit P0): a "hardware:*" requires token becomes a resource key
// so the queue serializes two tasks on one physical device instead of running
// two drivers on the same pin.
func TestEnqueueDerivesHardwareKey(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	tk, err := c.Enqueue(ctx, TaskInput{
		Title: "servo", Intent: "x", Requires: []string{"hardware:servo_tilt"},
	}, DefaultQueueSpec())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	got, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !slices.Contains(got.ResourceKeys, "hardware:servo_tilt") {
		t.Fatalf("resource keys = %v, want hardware:servo_tilt", got.ResourceKeys)
	}
}

// TestEnqueueActionSpecForcesActuatorKey covers the spec side of the same
// contract: even when requires reaches the device through a vaguer token
// ("servo"), the spec's exact target id is what serializes.
func TestEnqueueActionSpecForcesActuatorKey(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	tk, err := c.Enqueue(ctx, TaskInput{
		Title: "servo", Intent: "x", Requires: []string{"servo"},
		SpecJSON: `{"action_spec":{"target_actuator":"hardware:gpio_servo","action":"rotate","parameters":{"angle":90}}}`,
	}, DefaultQueueSpec())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	got, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !slices.Contains(got.ResourceKeys, "hardware:gpio_servo") {
		t.Fatalf("resource keys = %v, want hardware:gpio_servo forced by spec", got.ResourceKeys)
	}
}

// TestEnqueueExplicitKeysStillMergeActuator: caller-named keys cannot waive
// the physical lock — a task pinned to a node AND driving hardware holds both.
func TestEnqueueExplicitKeysStillMergeActuator(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	q := DefaultQueueSpec()
	q.ResourceKeys = []string{"node:opi"}
	tk, err := c.Enqueue(ctx, TaskInput{
		Title: "servo", Intent: "x", Requires: []string{"hardware:servo_tilt"},
	}, q)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	got, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !slices.Contains(got.ResourceKeys, "node:opi") || !slices.Contains(got.ResourceKeys, "hardware:servo_tilt") {
		t.Fatalf("resource keys = %v, want node:opi + hardware:servo_tilt", got.ResourceKeys)
	}
}

// TestActuatorKeysSerializeInQueue is the audit's exact scenario: a node with
// MaxConcurrent > 1 receiving two tasks for the same hardware device must run
// them one at a time — the second stays queued while the first holds the
// actuator's resource key.
func TestActuatorKeysSerializeInQueue(t *testing.T) {
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "queue-edge",
		ResourceClass: "Edge",
		Actuators: []ledger.ActuatorProfile{{
			ID: "hardware:slow_servo", Type: "hardware", Tier: 1,
			Command: "sleep", Args: []string{"1"},
		}},
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 4},
	}
	c := NewCore(db, "queue-edge", card, 5, testLogger(), config.ModelConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartQueueScheduler(ctx)

	first, err := c.Enqueue(ctx, TaskInput{
		Title: "slow servo", Intent: "x", Requires: []string{"hardware:slow_servo"}, Authorized: true,
	}, DefaultQueueSpec())
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	second, err := c.Enqueue(ctx, TaskInput{
		Title: "servo again", Intent: "x", Requires: []string{"hardware:slow_servo"}, Authorized: true,
	}, DefaultQueueSpec())
	if err != nil {
		t.Fatalf("enqueue second: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f, _ := c.store.Get(ctx, first.TaskID)
		if f.State == StateRunning {
			s, _ := c.store.Get(ctx, second.TaskID)
			if s.State != StateQueued {
				t.Fatalf("same-actuator task state = %s, want queued while device held", s.State)
			}
			for time.Now().Before(time.Now().Add(5 * time.Second)) {
				s, _ = c.store.Get(ctx, second.TaskID)
				if s.State == StateDone {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("second actuator task never ran after device freed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("first actuator task never reached running")
}

// TestEnqueueRoutesToPeer verifies the queue path is cross-device: a task
// enqueued on a node that cannot execute it (no gpio:read) is claimed by the
// local scheduler, forwarded to a capable peer, executed there, and the
// result completes the origin's row. Queued tasks must be first-class network
// citizens — the same routing contract as Submit.
func TestEnqueueRoutesToPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	root := newCore(t, "queue-root", "127.0.0.1:17881")
	leaf := newCoreWithNative(t, "queue-leaf", "127.0.0.1:17882", ledger.NativeAbility{
		ID: "gpio:read", Command: "echo", Args: []string{"queue-gpio-ok"},
	})
	if err := root.Register(ctx); err != nil {
		t.Fatalf("root register: %v", err)
	}
	if err := leaf.Register(ctx); err != nil {
		t.Fatalf("leaf register: %v", err)
	}
	go func() { _ = root.Listen(ctx, "127.0.0.1:17881") }()
	go func() { _ = leaf.Listen(ctx, "127.0.0.1:17882") }()
	time.Sleep(200 * time.Millisecond)
	if err := root.DialPeer(ctx, "127.0.0.1:17882"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	waitPeer(t, root, "queue-leaf")
	waitPeer(t, leaf, "queue-root")
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

// TestDrainFlagPausesQueueAndBeatsDraining covers the maintenance-mode
// mechanism end to end at unit level: the settings flag makes the queue
// store report nothing claimable (in-flight work still finishes — it is
// already claimed) and makes the self heartbeat publish "draining" so peers
// stop routing new work here.
func TestDrainFlagPausesQueueAndBeatsDraining(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "drain-node", "")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := c.db.Exec(`INSERT INTO settings(key, value) VALUES('node_drain','1')`); err != nil {
		t.Fatalf("set drain flag: %v", err)
	}
	if !c.node.draining(ctx) {
		t.Fatal("draining() must read the settings flag")
	}

	adapter := queueStoreAdapter{c: c}
	ready, err := adapter.ListReady(ctx)
	if err != nil || len(ready) != 0 {
		t.Fatalf("drained ListReady = %v, %v — must report nothing claimable", ready, err)
	}

	c.node.beat(ctx)
	nodes, err := ledger.Query(c.db, "", "")
	if err != nil {
		t.Fatalf("query nodes: %v", err)
	}
	var self *ledger.Node
	for i := range nodes {
		if nodes[i].ID == c.nodeID {
			self = &nodes[i]
		}
	}
	if self == nil || self.Status != "draining" {
		t.Fatalf("self row status = %+v, want draining", self)
	}

	// Lifting the flag restores both halves.
	if _, err := c.db.Exec(`UPDATE settings SET value='0' WHERE key='node_drain'`); err != nil {
		t.Fatal(err)
	}
	if c.node.draining(ctx) {
		t.Fatal("drain flag cleared but draining() still true")
	}
	c.node.beat(ctx)
	nodes, _ = ledger.Query(c.db, "", "")
	for _, n := range nodes {
		if n.ID == c.nodeID && n.Status != "online" {
			t.Fatalf("after --off self status = %s, want online", n.Status)
		}
	}
}
