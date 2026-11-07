// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/projects"
)

// Spawn generations persist on the row: root 0, child 1, grandchild 2 — and
// a spawn that would exceed delegation.max_depth is refused at the store
// boundary, so the cap holds no matter which node mints the child.
func TestSpawnChildTaskDepthBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := newCore(t, "depth-node")
	c.SetDelegationLimits(config.DelegationConfig{MaxChildren: 8, FanOut: 4, MaxDepth: 2})
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}

	root, _, _, err := c.createTask(ctx, TaskInput{Title: "root", Intent: "root work"})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	if root.SpawnDepth != 0 {
		t.Fatalf("root depth = %d, want 0", root.SpawnDepth)
	}

	child, err := c.SpawnChildTask(ctx, root.TaskID, TaskInput{Title: "c1", Intent: "child work"})
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}
	if child.SpawnDepth != 1 {
		t.Fatalf("child depth = %d, want 1", child.SpawnDepth)
	}
	got, err := c.store.Get(ctx, child.TaskID)
	if err != nil {
		t.Fatalf("reload child: %v", err)
	}
	if got.SpawnDepth != 1 {
		t.Fatalf("persisted child depth = %d, want 1", got.SpawnDepth)
	}

	grand, err := c.SpawnChildTask(ctx, child.TaskID, TaskInput{Title: "g1", Intent: "grandchild"})
	if err != nil {
		t.Fatalf("spawn grandchild: %v", err)
	}
	if grand.SpawnDepth != 2 {
		t.Fatalf("grandchild depth = %d, want 2", grand.SpawnDepth)
	}

	// grand is at max_depth=2: its child would be generation 3 — refused.
	if _, err := c.SpawnChildTask(ctx, grand.TaskID, TaskInput{Title: "gg", Intent: "too deep"}); err == nil {
		t.Fatal("spawn past max_depth accepted")
	} else if !strings.Contains(err.Error(), "max_depth") {
		t.Fatalf("depth refusal error = %v", err)
	}
}

// The wire's Depth field is the spawn generation (§6.1): a delegated child
// lands on the worker already carrying its depth, so the receiving node's
// own max_depth check sees the real generation rather than minting a fresh
// depth-0 subtree. A legacy/zero payload leaves the column default.
func TestDelegateDepthWireAdoption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-depth")
	worker := newCore(t, "worker-depth")
	startPair(t, ctx, entry, worker)

	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-depth", "m-depth", bus.TaskDelegatePayload{
		TaskID: "depth-2-task", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-depth"}, Depth: 2,
	})
	if err := entry.sendTo("worker-depth", env); err != nil {
		t.Fatalf("send depth-2: %v", err)
	}
	env0, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-depth", "m-depth0", bus.TaskDelegatePayload{
		TaskID: "depth-0-task", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-depth"},
	})
	if err := entry.sendTo("worker-depth", env0); err != nil {
		t.Fatalf("send depth-0: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	deep, err := worker.store.Get(ctx, "depth-2-task")
	if err != nil {
		t.Fatalf("depth-2 row: %v", err)
	}
	if deep.SpawnDepth != 2 {
		t.Fatalf("wire depth adopted as %d, want 2", deep.SpawnDepth)
	}
	rootRow, err := worker.store.Get(ctx, "depth-0-task")
	if err != nil {
		t.Fatalf("depth-0 row: %v", err)
	}
	if rootRow.SpawnDepth != 0 {
		t.Fatalf("legacy payload depth = %d, want 0", rootRow.SpawnDepth)
	}
}

// delegateChildren fans out through a semaphore sized by delegation.fanout:
// with fanout=2 and four slow local children the in-flight spawn count peaks
// at 2 — never 4 — while every request still completes.
func TestDelegateChildrenFanOutBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c := newCoreWithNative(t, "fanout-node", ledger.NativeAbility{
		ID: "slow:work", Command: "sleep", Args: []string{"1"}, Tier: 1,
	})
	c.SetDelegationLimits(config.DelegationConfig{MaxChildren: 8, FanOut: 2, MaxDepth: 3})
	projDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "README.md"), []byte("shared project\n"), 0o644); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	ps := projects.NewStore(c.db)
	if _, err := ps.Create("proj", projDir, "fanout test"); err != nil {
		t.Fatalf("register project: %v", err)
	}
	c.SetProjectStores(ps, filepath.Join(t.TempDir(), "projects-root"))
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	c.StartQueueScheduler(ctx)

	parent, _, _, err := c.createTask(ctx, TaskInput{
		Title: "parent", Intent: "coordinate", Project: "proj", SessionID: "fanout-session",
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	drs := []delegateRequest{
		{Title: "s1", Intent: "s1", Requires: []string{"slow:work"}},
		{Title: "s2", Intent: "s2", Requires: []string{"slow:work"}},
		{Title: "s3", Intent: "s3", Requires: []string{"slow:work"}},
		{Title: "s4", Intent: "s4", Requires: []string{"slow:work"}},
	}

	stop := make(chan struct{})
	finished := make(chan struct{})
	var peak atomic.Int64
	var overlapped atomic.Bool
	go func() {
		defer close(finished)
		for {
			select {
			case <-stop:
				return
			default:
			}
			for {
				n := c.delegateActiveCount()
				if n <= peak.Load() || peak.CompareAndSwap(peak.Load(), n) {
					break
				}
			}
			var running int
			_ = c.store.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM tasks WHERE parent_id = ? AND state = 'running'`, parent.TaskID,
			).Scan(&running)
			if running >= 2 {
				overlapped.Store(true)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	var pending []pendingChild
	notes := c.delegateChildren(ctx, parent, drs, &pending)
	close(stop)
	// Wait for the poller's exit so `peak` is final before reading.
	<-finished

	if got := peak.Load(); got > 2 {
		t.Fatalf("fan-out peak = %d, exceeds configured 2", got)
	} else if got == 0 {
		t.Fatal("no delegate children ever in flight")
	}
	if len(notes) != len(drs) {
		t.Fatalf("notes = %d, want %d", len(notes), len(drs))
	}
	if !overlapped.Load() {
		t.Fatal("unscoped project siblings never ran concurrently; expected default isolation")
	}
	var unisolated int
	if err := c.store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE parent_id = ? AND isolate = 0`, parent.TaskID,
	).Scan(&unisolated); err != nil {
		t.Fatalf("count unisolated children: %v", err)
	}
	if unisolated != 0 {
		t.Fatalf("unscoped siblings left without isolation: %d", unisolated)
	}
	// The four one-second children overlap pairwise (peak 2): the whole
	// fan-out takes roughly two batches, not four serial runs. The parent
	// row itself never executes here — count only its children.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var open int
		if err := c.store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tasks WHERE parent_id = ? AND state NOT IN ('done','failed','cancelled','expired')`,
			parent.TaskID).Scan(&open); err != nil {
			t.Fatalf("count children: %v", err)
		}
		if open == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d child tasks never settled", open)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Terminal rows precede the last work-dir/artifact writes by a beat —
	// drain them or the TempDir cleanup races the writes.
	waitArtifactWrites(t, c)
}

// A pending child that can never be claimed must not hold the final fold
// forever: on a node whose capacity slots are all held by waiting parents a
// queued child stays lease-less forever, and the fold's silence bound is
// what turns that deadlock into an annotated outstanding note.
func TestAwaitPendingChildrenSilenceBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c := newCore(t, "fold-node")
	c.SetDelegationLimits(config.DelegationConfig{MaxChildren: 8, FanOut: 4, MaxDepth: 3})
	c.leaseTimeout = 100 * time.Millisecond // silentCap below the 1s slice floor
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	// No queue scheduler: the child row sits in submitted forever — the
	// starvation case the bound exists for.
	parent, _, _, err := c.createTask(ctx, TaskInput{Title: "p", Intent: "x"})
	if err != nil {
		t.Fatalf("parent: %v", err)
	}
	child, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "c", Intent: "y"})
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	pending := []pendingChild{{id: child.TaskID, title: child.Title}}
	start := time.Now()
	notes := c.awaitPendingChildren(ctx, parent, pending)
	if time.Since(start) > 10*time.Second {
		t.Fatal("fold waited far past the silence bound")
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, "outstanding") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an outstanding annotation, got %v", notes)
	}
}

// A child that finishes after its parent reported leaves its outcome on the
// parent's children board — the record a `task board` read or a resumed
// parent run can still find — plus a trace event on the parent row.
func TestLateChildResultPostsBoard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := newCore(t, "late-node")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	parent, _, _, err := c.createTask(ctx, TaskInput{Title: "p", Intent: "x"})
	if err != nil {
		t.Fatalf("parent: %v", err)
	}
	child, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "c", Intent: "y"})
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	// Both terminal: the fold path is gone — the board post is the record.
	if _, err := c.store.db.ExecContext(ctx,
		`UPDATE tasks SET state='done' WHERE task_id IN (?,?)`, parent.TaskID, child.TaskID); err != nil {
		t.Fatalf("mark terminal: %v", err)
	}
	if _, err := c.store.db.ExecContext(ctx,
		`UPDATE tasks SET result_json='{"state":"done","ok":true,"stdout":"child product"}' WHERE task_id=?`,
		child.TaskID); err != nil {
		t.Fatalf("child result: %v", err)
	}

	c.reportLateChildResult(ctx, child.TaskID)

	msgs, err := c.store.TaskMessages(ctx, "task:"+parent.TaskID)
	if err != nil {
		t.Fatalf("board read: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("late result never reached the parent's board")
	}
	if !strings.Contains(msgs[0].Body, "child product") {
		t.Fatalf("board body = %q, want the child's output", msgs[0].Body)
	}
	// A still-running parent must NOT get the post — the live fold machinery
	// owns delivery while it exists.
	parent2, _, _, _ := c.createTask(ctx, TaskInput{Title: "p2", Intent: "x"})
	child2, err := c.SpawnChildTask(ctx, parent2.TaskID, TaskInput{Title: "c2", Intent: "y"})
	if err != nil {
		t.Fatalf("child2: %v", err)
	}
	if _, err := c.store.db.ExecContext(ctx,
		`UPDATE tasks SET state='done' WHERE task_id=?`, child2.TaskID); err != nil {
		t.Fatalf("mark child2 done: %v", err)
	}
	c.reportLateChildResult(ctx, child2.TaskID)
	if msgs, _ := c.store.TaskMessages(ctx, "task:"+parent2.TaskID); len(msgs) != 0 {
		t.Fatalf("running parent's board got %d posts, want 0", len(msgs))
	}
}

// The tightest cap seen along the delegation path governs: a parent whose
// subtree cap is 1 may mint a direct child but no grandchild, even when the
// local config would allow depth 3.
func TestSpawnCapInheritedBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := newCore(t, "cap-node")
	c.SetDelegationLimits(config.DelegationConfig{MaxChildren: 8, FanOut: 4, MaxDepth: 3})
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	root, _, _, err := c.createTask(ctx, TaskInput{Title: "root", Intent: "x"})
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	// The wire delivered a subtree bound of 1 (a stricter upstream node).
	if err := c.store.SetSpawnCap(ctx, root.TaskID, 1); err != nil {
		t.Fatalf("set cap: %v", err)
	}
	child, err := c.SpawnChildTask(ctx, root.TaskID, TaskInput{Title: "c", Intent: "y"})
	if err != nil {
		t.Fatalf("child within cap refused: %v", err)
	}
	if child.SpawnCap != 1 {
		t.Fatalf("child cap = %d, want inherited 1", child.SpawnCap)
	}
	if _, err := c.SpawnChildTask(ctx, child.TaskID, TaskInput{Title: "g", Intent: "z"}); err == nil {
		t.Fatal("spawn past inherited cap accepted")
	}
}

// The payload's max_depth clamps against the receiver's own config — never
// looser: a payload advertising 1 persists min(local 3, wire 1) = 1.
func TestDelegateWireMaxDepthAdoption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-cap")
	worker := newCore(t, "worker-cap")
	worker.SetDelegationLimits(config.DelegationConfig{MaxChildren: 8, FanOut: 4, MaxDepth: 3})
	startPair(t, ctx, entry, worker)

	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-cap", "m-cap", bus.TaskDelegatePayload{
		TaskID: "capped-task", Title: "t", Intent: "x", Requires: []string{"sys:info"},
		Chain: []string{"entry-cap"}, Depth: 1, MaxDepth: 1,
	})
	if err := entry.sendTo("worker-cap", env); err != nil {
		t.Fatalf("send: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	row, err := worker.store.Get(ctx, "capped-task")
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	if row.SpawnCap != 1 {
		t.Fatalf("adopted cap = %d, want min(3,1)=1", row.SpawnCap)
	}
	// The adopted cap enforces locally: depth-1 task may not spawn here.
	if _, err := worker.SpawnChildTask(ctx, "capped-task", TaskInput{Title: "g", Intent: "z"}); err == nil {
		t.Fatal("spawn over adopted cap accepted")
	}
}

// delegation.enabled=false refuses new sub-agent mints outright — the
// operator's switch reaches the spawn boundary, not just the prompt hint.
func TestDelegationDisabledRefusesSpawn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	off := false
	c := newCore(t, "off-node")
	c.SetDelegationLimits(config.DelegationConfig{MaxChildren: 8, FanOut: 4, MaxDepth: 3, Enabled: &off})
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	root, _, _, err := c.createTask(ctx, TaskInput{Title: "r", Intent: "x"})
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if _, err := c.SpawnChildTask(ctx, root.TaskID, TaskInput{Title: "c", Intent: "y"}); err == nil {
		t.Fatal("spawn accepted on a delegation-disabled node")
	} else if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disable refusal error = %v", err)
	}
	// A nil flag (pre-toggle config) keeps orchestration on.
	on := newCore(t, "on-node")
	if !on.delegationEnabled() {
		t.Fatal("nil enabled flag reported disabled")
	}
}

// TestSpawnChildTaskPersistsScopePaths pins the row half of the scope
// protocol: a child spawned with declared paths carries them on its row, so
// a later requeue/forward re-derives the same leaf locks instead of widening
// the claim to the whole project.
func TestSpawnChildTaskPersistsScopePaths(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := newCore(t, "scope-persist")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	root, _, _, err := c.createTask(ctx, TaskInput{Title: "root", Intent: "work", Project: "proj"})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	child, err := c.SpawnChildTask(ctx, root.TaskID, TaskInput{
		Title: "scoped", Intent: "work", Project: "proj",
		Paths: []string{"pkg/api", "./pkg/util", "pkg/api"},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if len(child.ScopePaths) != 2 {
		t.Fatalf("in-memory scope paths = %v", child.ScopePaths)
	}
	got, err := c.store.Get(ctx, child.TaskID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got.ScopePaths) != 2 || got.ScopePaths[0] != "pkg/api" || got.ScopePaths[1] != "pkg/util" {
		t.Fatalf("persisted scope paths = %v, want [pkg/api pkg/util]", got.ScopePaths)
	}
	// An unscoped child carries nothing — the whole-project fallback.
	plain, err := c.SpawnChildTask(ctx, root.TaskID, TaskInput{Title: "plain", Intent: "work", Project: "proj"})
	if err != nil {
		t.Fatalf("spawn plain: %v", err)
	}
	gotPlain, err := c.store.Get(ctx, plain.TaskID)
	if err != nil {
		t.Fatalf("reload plain: %v", err)
	}
	if len(gotPlain.ScopePaths) != 0 {
		t.Fatalf("unscoped child paths = %v, want none", gotPlain.ScopePaths)
	}
}

// TestSpawnChildTaskPersistsIsolate pins the flag's row half: a requeue or
// forward rebuilds the lock set from the row, so the isolation decision must
// survive the row round-trip — without it a re-routed isolated child would
// silently run against the shared tree holding only its task key.
func TestSpawnChildTaskPersistsIsolate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := newCore(t, "iso-persist")
	if err := c.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	root, _, _, err := c.createTask(ctx, TaskInput{Title: "root", Intent: "work", Project: "proj"})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	child, err := c.SpawnChildTask(ctx, root.TaskID, TaskInput{
		Title: "iso", Intent: "work", Project: "proj", Isolate: true,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	got, err := c.store.Get(ctx, child.TaskID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !got.Isolate {
		t.Fatal("persisted isolate flag lost")
	}
}

// The structured schema's advertised ceiling follows the configured
// max_children so the model never sees room the budget doesn't grant.
func TestAgentResultSchemaMaxItemsFollowsConfig(t *testing.T) {
	if s := agentResultSchemaFor(nil, false, 3); !strings.Contains(s, `"maxItems": 3,`) {
		t.Fatal("schema did not adopt max_children=3")
	}
	if s := agentResultSchemaFor(nil, false, 20); !strings.Contains(s, `"maxItems": 20,`) {
		t.Fatal("schema did not adopt max_children=20")
	}
	if s := agentResultSchemaFor(nil, false, 0); !strings.Contains(s, `"maxItems": 8,`) {
		t.Fatal("zero bound did not keep the baked-in 8")
	}
}

// The structured schema advertises both parallelism levers a child may
// declare: paths for same-tree scope keys, isolate for a private copy.
func TestAgentResultSchemaAdvertisesScopeAndIsolate(t *testing.T) {
	s := agentResultSchemaFor(nil, false, 8)
	for _, prop := range []string{`"paths"`, `"isolate"`} {
		if !strings.Contains(s, prop) {
			t.Fatalf("schema missing %s", prop)
		}
	}
}
