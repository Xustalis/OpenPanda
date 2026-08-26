package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
)

// Every plan stage and every panel-submitted task reaches execution through
// forwardScheduled, so that is where the hardware filter and the load-balancing
// score have to hold. Before this test the function asked "can I do it?" and
// answered locally whenever it could — the pre-v0.0.6 short-circuit that
// scheduler.RouteAt's doc comment says was removed precisely because it makes
// balancing impossible by construction. It was removed from Route only; the
// queue path kept its own copy, which is the path the flagship plan runs on.
//
// The existing hardware-routing coverage (resource_route_test.go) calls
// scheduler.Route directly, so it could not see this: the router was right and
// nobody asked it.
func TestScheduledTaskRoutesByHardware(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Both nodes can *start* a training run; only one can finish it. Two free
	// slots on the Pi so a stay-home decision cannot be explained by capacity.
	train := ledger.NativeAbility{ID: "gpu:train", Command: "sh", Args: []string{"-c", "true"}, Tier: 1}
	pi := newCoreWithResources(t, "pi", train,
		ledger.ResourceProfile{CPU: 4, RAMGB: 2, GPUVRAMGB: 0, DurationHint: "short"}, 2)
	win := newCoreWithResources(t, "win", train,
		ledger.ResourceProfile{CPU: 16, RAMGB: 32, GPUVRAMGB: 12, DurationHint: "long"}, 3)
	pi.SetWorkDir(t.TempDir())
	win.SetWorkDir(t.TempDir())
	startPair(t, ctx, pi, win, "127.0.0.1:17996", "127.0.0.1:17997")

	// enqueued returns the stored row for a task the Pi queued, which is what
	// the queue scheduler hands to runScheduled.
	enqueued := func(requires []string, resourceJSON string) Task {
		t.Helper()
		got, err := pi.Enqueue(ctx, TaskInput{
			Title: "stage", Intent: "run the stage",
			Requires: requires, ResourceJSON: resourceJSON,
		}, DefaultQueueSpec())
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		row, err := pi.store.Get(ctx, got.TaskID)
		if err != nil {
			t.Fatalf("load task: %v", err)
		}
		return row
	}

	const gpu = `{"cpu":4,"ram_gb":8,"gpu_vram_gb":8,"duration_hint":"long"}`

	// R2: the Pi has the ability and 0 GiB of VRAM. It must hand the stage off.
	if !pi.forwardScheduled(ctx, enqueued([]string{"gpu:train"}, gpu)) {
		t.Error("a queued stage needing 8 GiB of VRAM stayed on a node that declares 0")
	}

	// A stage may legally declare hardware and no ability at all (plan.Validate
	// does not require one, and `resources:` is the whole point of v0.0.6). An
	// empty ability list is "no constraint", not "nobody matches".
	if !pi.forwardScheduled(ctx, enqueued(nil, gpu)) {
		t.Error("a queued stage with no ability requirement ignored its declared hardware")
	}

	// The other half: nothing to route away. An idle node keeps its own work
	// rather than paying a hop for it (localBias).
	if pi.forwardScheduled(ctx, enqueued([]string{"gpu:train"}, "")) {
		t.Error("a task the Pi can run, on an idle Pi, was shipped to a peer anyway")
	}
}

// A task pinned to a directory on this machine is local work by definition: the
// delegate payload carries no work dir, so a forwarded copy would run somewhere
// else entirely. Routing may not override that.
func TestScheduledTaskWithWorkDirStaysLocal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	train := ledger.NativeAbility{ID: "gpu:train", Command: "sh", Args: []string{"-c", "true"}, Tier: 1}
	pi := newCoreWithResources(t, "pi", train,
		ledger.ResourceProfile{CPU: 4, RAMGB: 2, GPUVRAMGB: 0, DurationHint: "short"}, 1)
	win := newCoreWithResources(t, "win", train,
		ledger.ResourceProfile{CPU: 16, RAMGB: 32, GPUVRAMGB: 12, DurationHint: "long"}, 3)
	pi.SetWorkDir(t.TempDir())
	win.SetWorkDir(t.TempDir())
	startPair(t, ctx, pi, win, "127.0.0.1:17998", "127.0.0.1:17999")

	dir := t.TempDir()
	got, err := pi.Enqueue(ctx, TaskInput{
		Title: "stage", Intent: "run the stage",
		Requires:     []string{"gpu:train"},
		ResourceJSON: `{"gpu_vram_gb":8}`,
	}, QueueSpec{Priority: PriorityNormal, WorkDir: dir})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	row, err := pi.store.Get(ctx, got.TaskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if row.WorkDir != dir {
		t.Fatalf("work dir = %q, want %q", row.WorkDir, dir)
	}
	if pi.forwardScheduled(ctx, row) {
		t.Error("a task pinned to a local directory was forwarded to a peer")
	}
}

// A plan's whole point is that stages with no dependency between them run at
// once. They could not: with no resource keys of their own every stage fell back
// to queue.DefaultResourceKey ("agent:*"), the single conservative lock, so a
// fan-out plan executed one stage at a time on a node with slots to spare. That
// lock exists because two anonymous tasks would trample one working tree, which
// stages cannot — each derives its own (stageWorkDir).
func TestIndependentPlanStagesDoNotShareOneLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := newCoreWithResources(t, "mac", ledger.NativeAbility{
		ID: "gpu:train", Command: "sh", Args: []string{"-c", "true"}, Tier: 1,
	}, ledger.ResourceProfile{CPU: 8, RAMGB: 16, GPUVRAMGB: 8}, 4)
	c.SetWorkDir(t.TempDir())

	// Two stages, neither needing the other, plus one that waits for both — so
	// the plan is a real fan-out and not two unrelated tasks.
	p := plan.Plan{Goal: "两段并行，最后汇总", Stages: []plan.Stage{
		{ID: "a", Intent: "第一段"},
		{ID: "b", Intent: "第二段"},
		{ID: "sum", Needs: []string{"a", "b"}, Intent: "汇总"},
	}}
	planID, err := c.StartPlan(ctx, p, DefaultQueueSpec())
	if err != nil {
		t.Fatalf("start plan: %v", err)
	}

	ready, err := c.store.ListReady(ctx)
	if err != nil {
		t.Fatalf("list ready: %v", err)
	}
	if len(ready) != 2 {
		t.Fatalf("released %d stages, want the 2 with no dependencies", len(ready))
	}

	// The registry is what the scheduler consults before starting a task: if the
	// second stage cannot take its keys while the first holds them, it waits.
	reg := queue.NewResourceRegistry()
	for _, r := range ready {
		keys := queue.DefaultKeys(r.ResourceKeys, r.Project)
		if !reg.TryAcquire(keys, r.TaskID) {
			t.Fatalf("stage %s could not run alongside its sibling (keys %v)", r.TaskID, keys)
		}
	}

	// And the keys are scoped to this plan, so a second run of the same plan file
	// is not accidentally free of them either.
	for _, r := range ready {
		if len(r.ResourceKeys) != 1 || !strings.HasPrefix(r.ResourceKeys[0], "plan:"+planID+":") {
			t.Errorf("stage keys = %v, want one scoped to plan %s", r.ResourceKeys, planID)
		}
	}
}
