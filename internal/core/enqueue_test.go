// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
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
	c := NewCore(db, "queue-node", card, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	return c
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
	c.SetWorkDir(t.TempDir())
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
	c.SetWorkDir(t.TempDir())
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

	root := newCore(t, "queue-root")
	leaf := newCoreWithNative(t, "queue-leaf", ledger.NativeAbility{
		ID: "gpio:read", Command: "echo", Args: []string{"queue-gpio-ok"},
	})
	if err := root.Register(ctx); err != nil {
		t.Fatalf("root register: %v", err)
	}
	if err := leaf.Register(ctx); err != nil {
		t.Fatalf("leaf register: %v", err)
	}
	startListen(t, ctx, root, "127.0.0.1:0")
	leafAddr := startListen(t, ctx, leaf, "127.0.0.1:0")
	if err := root.DialPeer(ctx, leafAddr); err != nil {
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

// TestAnonymousTasksRunInParallel is the end-to-end proof of the
// serialization fix: two anonymous tasks — no keys, no project, no pinned
// work dir — occupy one private work dir each (prepareRunDir's
// attachedWorkDir branch), so their lock keys are their own task ids and
// nothing makes them queue behind each other. Before the per-task-dir work
// they shared one conservative "agent:*" lock and ran strictly serially on
// a node with free slots.
func TestAnonymousTasksRunInParallel(t *testing.T) {
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "queue-node",
		ResourceClass: "Standard",
		Native: []ledger.NativeAbility{
			{ID: "sys:slow", Command: "sleep", Args: []string{"1"}},
		},
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 4},
	}
	c := NewCore(db, "queue-node", card, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartQueueScheduler(ctx)

	var ids []string
	for _, title := range []string{"anon-a", "anon-b"} {
		tk, err := c.Enqueue(ctx, TaskInput{
			Title: title, Intent: "x", Requires: []string{"sys:slow"}, Authorized: true,
		}, DefaultQueueSpec())
		if err != nil {
			t.Fatalf("enqueue %s: %v", title, err)
		}
		ids = append(ids, tk.TaskID)
	}

	// Both tasks must reach running while the other is still running —
	// overlap, not just eventual completion. sleep(1) keeps the window wide
	// enough that a serialized pair can never satisfy this.
	deadline := time.Now().Add(5 * time.Second)
	overlapped := false
	for time.Now().Before(deadline) {
		running := 0
		done := 0
		for _, id := range ids {
			got, err := c.store.Get(ctx, id)
			if err != nil {
				t.Fatalf("get %s: %v", id, err)
			}
			switch got.State {
			case StateRunning:
				running++
			case StateDone:
				done++
			}
		}
		if running == 2 {
			overlapped = true
			break
		}
		if done == 2 {
			t.Fatal("both finished but never ran concurrently — still serialized")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !overlapped {
		t.Fatal("anonymous tasks never overlapped within deadline")
	}
	// The observed overlap returns with both sleepers still mid-run; their
	// per-task work dirs live inside this test's TempDir, so the rows must
	// settle before t.TempDir cleanup or the removal races their writes.
	waitLocalTasksSettled(t, c)
}

// TestSameWorkDirSerializes pins the other half of the contract: a
// submitter-pinned work directory IS a shared tree, so two tasks pointing at
// it must serialize on the dir's lock — parallelism stops exactly where real
// sharing starts.
func TestSameWorkDirSerializes(t *testing.T) {
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "queue-node",
		ResourceClass: "Standard",
		Native: []ledger.NativeAbility{
			{ID: "sys:slow", Command: "sleep", Args: []string{"1"}},
			{ID: "sys:info", Command: "echo", Args: []string{"ok"}},
		},
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 4},
	}
	c := NewCore(db, "queue-node", card, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartQueueScheduler(ctx)

	pinned := t.TempDir()
	q1 := DefaultQueueSpec()
	q1.WorkDir = pinned
	first, err := c.Enqueue(ctx, TaskInput{
		Title: "slow in pinned", Intent: "x", Requires: []string{"sys:slow"}, Authorized: true,
	}, q1)
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	q2 := DefaultQueueSpec()
	q2.WorkDir = pinned
	second, err := c.Enqueue(ctx, TaskInput{
		Title: "fast in pinned", Intent: "x", Requires: []string{"sys:info"}, Authorized: true,
	}, q2)
	if err != nil {
		t.Fatalf("enqueue second: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f, _ := c.store.Get(ctx, first.TaskID)
		if f.State == StateRunning {
			s, _ := c.store.Get(ctx, second.TaskID)
			if s.State != StateQueued {
				t.Fatalf("same-workdir task state = %s, want queued while dir held", s.State)
			}
			for time.Now().Before(time.Now().Add(5 * time.Second)) {
				s, _ = c.store.Get(ctx, second.TaskID)
				if s.State == StateDone {
					waitLocalTasksSettled(t, c)
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("second task never ran after pinned dir freed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("first task never reached running")
}

// TestDrainFlagPausesQueueAndBeatsDraining covers the maintenance-mode
// mechanism end to end at unit level: the settings flag makes the queue
// store report nothing claimable (in-flight work still finishes — it is
// already claimed) and makes the self heartbeat publish "draining" so peers
// stop routing new work here.
func TestDrainFlagPausesQueueAndBeatsDraining(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "drain-node")
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

// TestResourceKeysForPinnedWorkDir covers the submission-time correction a
// pinned work directory needs: a pin that names the project's OWN tree keeps
// the project key (every other task finds that tree by project, so a dir: key
// would hide the conflict and let two writers into one checkout), while a pin
// anywhere else stands alone on its dir: key — which is what lets two business
// domains run side by side instead of queueing behind one project lock.
func TestResourceKeysForPinnedWorkDir(t *testing.T) {
	c := queueTestCore(t)

	own, err := c.projectWorkDir("p")
	if err != nil {
		t.Fatalf("project work dir: %v", err)
	}
	got := c.resourceKeysFor(TaskInput{Project: "p", WorkDir: own})
	if !slices.Contains(got, "project:p") || !slices.Contains(got, "scope:p") {
		t.Fatalf("pin on the project tree = %v, want project+scope keys", got)
	}
	// A subdirectory of the project's tree is still that tree: the lock must be
	// a scope lane, because a dir: key would be invisible to the project root
	// and a whole-project task could then write alongside it.
	got = c.resourceKeysFor(TaskInput{Project: "p", WorkDir: filepath.Join(own, "feat-a")})
	if want := []string{"scope:p/feat-a"}; !slices.Equal(got, want) {
		t.Fatalf("pin inside the project tree = %v, want %v", got, want)
	}

	elsewhere := t.TempDir()
	got = c.resourceKeysFor(TaskInput{Project: "p", WorkDir: elsewhere})
	if want := []string{"dir:" + queue.WorkDirKey(elsewhere)}; !slices.Equal(got, want) {
		t.Fatalf("pin outside the project tree = %v, want %v", got, want)
	}
	// The tree question is about where the pin RESOLVES, not how it is spelled:
	// a link inside the project pointing out of it is not the project's tree,
	// so the pin must not claim the project's lock — including when the pinned
	// directory itself does not exist yet (--workdir creates it, so the link in
	// its prefix is all there is to read).
	link := filepath.Join(own, "link-out")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatalf("mkdir project tree: %v", err)
	}
	// Creating a link needs a privilege Windows may withhold; the assertions
	// below it do not, so only this block is conditional.
	if err := os.Symlink(elsewhere, link); err == nil {
		got = c.resourceKeysFor(TaskInput{Project: "p", WorkDir: link})
		if want := []string{"dir:" + queue.WorkDirKey(elsewhere)}; !slices.Equal(got, want) {
			t.Fatalf("pin through a link out of the tree = %v, want %v", got, want)
		}
		got = c.resourceKeysFor(TaskInput{Project: "p", WorkDir: filepath.Join(link, "later")})
		if len(got) != 1 || !strings.HasPrefix(got[0], "dir:") {
			t.Fatalf("missing pin under a link out of the tree = %v, want a dir: key", got)
		}
	}
	// A pinned directory with no project at all is still its own identity —
	// nothing else names that tree, so nothing else can be serialized on it.
	got = c.resourceKeysFor(TaskInput{WorkDir: elsewhere})
	if want := []string{"dir:" + queue.WorkDirKey(elsewhere)}; !slices.Equal(got, want) {
		t.Fatalf("projectless pin = %v, want %v", got, want)
	}
}

// TestResourceKeysSessionLane: a task carrying its conversation claims a lane
// one level under the project root instead of the root itself — two
// conversations on one checkout run in parallel — while a task with no
// conversation (CLI submit, plan stage, inbound delegate) keeps the root
// claim that still excludes every lane.
func TestResourceKeysSessionLane(t *testing.T) {
	a := deriveResourceKeys(TaskInput{Project: "p", SessionID: "sess-a"})
	b := deriveResourceKeys(TaskInput{Project: "p", SessionID: "sess-b"})
	root := deriveResourceKeys(TaskInput{Project: "p"})

	if len(a) != 2 || len(b) != 2 || len(root) != 2 {
		t.Fatalf("key sets = %v / %v / %v, want two keys each", a, b, root)
	}
	if want := queue.SessionScopeKey("p", "sess-a"); a[1] != want {
		t.Fatalf("session scope key = %q, want %q", a[1], want)
	}
	if root[0] != queue.ProjectKey("p") || root[1] != queue.ScopeRootKey("p") {
		t.Fatalf("sessionless keys = %v, want the legacy root pair", root)
	}
	if queue.ScopeKeysConflict(a[1], b[1]) {
		t.Fatalf("two conversations must not conflict: %q vs %q", a[1], b[1])
	}
	if !queue.ScopeKeysConflict(root[1], a[1]) {
		t.Fatalf("a whole-project claim must still exclude a lane: %q vs %q", root[1], a[1])
	}

	// A scoped child stays inside its own conversation's lane.
	scoped := childResourceKeys("t1", TaskInput{Project: "p", SessionID: "sess-a", Paths: []string{"src"}})
	if want := queue.SessionScopeLeafKey("p", "sess-a", "src"); len(scoped) != 1 || scoped[0] != want {
		t.Fatalf("scoped child keys = %v, want [%q]", scoped, want)
	}
	if queue.ScopeKeysConflict(scoped[0], b[1]) {
		t.Fatalf("a scoped child must not conflict with another conversation: %q vs %q", scoped[0], b[1])
	}

	// A conversation-less scoped child keeps the bare leaf: it is a sibling of
	// every lane, so it can write the same path a conversation is writing. The
	// alternative — claiming the project root — would restore that exclusion at
	// the cost of serializing every conversation-less scoped task against every
	// other one, which TestScopedProjectChildrenRunInParallel refuses.
	loose := childResourceKeys("t2", TaskInput{Project: "p", Paths: []string{"src"}})
	if want := queue.ScopeLeafKey("p", "src"); len(loose) != 1 || loose[0] != want {
		t.Fatalf("conversation-less scoped keys = %v, want [%q]", loose, want)
	}
}
