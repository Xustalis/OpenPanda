// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// pinnedTaskJSON is the classified dispatch the fake entry model emits: a task
// hard-pinned to the peer below, whose link is not live in these tests — the
// exact shape that used to park in the queue and end the session.
const pinnedTaskJSON = `{"kind":"task","task":{"title":"在 Mac 上建自我介绍页","context_type":"command",` +
	`"requires":{"abilities":["leaf:probe"]},` +
	`"spec":{"objectives":{"version":1,"revision":1,"dimensions":[{"dimension":"delivery","weight":1,"priority":"NORMAL","requirement":"Complete the fixture task","reason":"The fixture requests a task","criteria":["Observe the expected fixture result"]}]},"scope":"","target":"写一个自我介绍网页并在本机运行","constraints":[],"success_definition":"curl 返回 200","node":"mac-leaf"},` +
	`"complexity":0.3,"risk":"low"}}`

// registerLeafPeer adds the pinned destination to the fixture directory: an
// online row advertising the required ability, with no wire connection — so
// Submit routes forward and parks the task in the queue (link opportunistic).
func registerLeafPeer(t *testing.T, e *Engine) {
	t.Helper()
	leaf := ledger.Card{
		Device:        "mac-leaf",
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "leaf:probe", Command: "true"}},
		Capacity:      ledger.Capacity{ExecutionProtocol: 1, CPUCores: 4, RAMGB: 8, MaxConcurrent: 2},
	}
	if err := ledger.Register(e.db, leaf, "leaf-inst", 5); err != nil {
		t.Fatalf("register leaf: %v", err)
	}
}

// awaitQueuedTask polls the store until the pinned task parks, then drives it
// the way the daemon's queue consumer would: claim/dispatch, a relayed agent
// progress note, and the remote result.
func awaitQueuedTask(t *testing.T, e *Engine, settle func(ctx context.Context, store *core.TaskStore, id string)) {
	t.Helper()
	store := core.NewTaskStore(e.db, nil)
	go func() {
		ctx := context.Background()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			tasks, err := store.ListByState(ctx, core.StateQueued)
			if err == nil {
				for _, task := range tasks {
					if task.Title == "在 Mac 上建自我介绍页" {
						settle(ctx, store, task.TaskID)
						return
					}
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

// awaitWatchArmed blocks until the engine's wait has subscribed to the task
// store's event hub.
//
// The wait used to be given a fixed 1200ms to "let the wait begin": the
// watch subscribes through AddOnEvent, and a store write that lands before
// the subscription exists never wakes it — the row then only moves on the
// five-second poll tick. A write that reaches the hub proves the watch is
// listening, so the test waits for that instead of guessing a duration.
//
// The probe write is a no-op state re-stamp on the same row; it fires the
// hub exactly like a real transition would, and it changes nothing the
// wait can observe.
func awaitWatchArmed(t *testing.T, e *Engine, taskID string) {
	t.Helper()
	store := core.NewTaskStore(e.db, nil)
	// A listener on the same hub: when the watch writes through the store
	// (or the test's own probe does), this fires and proves the hub is
	// live. The watch's own subscription is what matters, and it lands
	// before the row's first transition.
	seen := make(chan struct{}, 1)
	unsub := store.AddOnEvent(func(id, _ string, _ any) {
		if id != taskID {
			return
		}
		select {
		case seen <- struct{}{}:
		default:
		}
	})
	defer unsub()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// Probe: touching the row fires the hub, so the watch's listener
		// sees it too.
		if err := store.RecordEvent(context.Background(), taskID, core.EvProgress,
			map[string]any{"note": "await test probe"}); err != nil {
			t.Fatalf("probe the watch: %v", err)
		}
		select {
		case <-seen:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("the wait never subscribed to task %s", taskID)
}

// awaitRowState blocks until the task row reaches want.
//
// This replaces the sleeps that stood for "a poll tick must observe the
// claim": the watch re-reads the row after the event that changed it, so
// the observable condition is the row's own state, not the passage of
// time.
func awaitRowState(t *testing.T, e *Engine, taskID, want string) {
	t.Helper()
	store := core.NewTaskStore(e.db, nil)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		row, err := store.Get(context.Background(), taskID)
		if err == nil && row.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	row, _ := store.Get(context.Background(), taskID)
	t.Fatalf("task %s state = %q, want %q", taskID, row.State, want)
}

// TestInlineAskFollowsQueuedTaskToOutcome is the reported defect: a task
// dispatched to a pinned node whose link is not live parks in the queue, and
// the session used to end right there — "queued" as the outcome, no sub-agent
// task state ever loaded. The inline round must instead follow the row until
// the queue consumer settles it and report what actually happened.
func TestInlineAskFollowsQueuedTaskToOutcome(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var followUp string
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		n := calls
		if n > 1 {
			followUp = string(body)
		}
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		resp := anthropicText(pinnedTaskJSON)
		if n > 1 {
			resp = anthropicText("Mac 已完成：页面在 ~/self-intro/index.html，服务已起在 8000 端口。")
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})
	e.queueTasks = false // inline: the conversation must wait for the outcome
	registerLeafPeer(t, e)

	awaitQueuedTask(t, e, func(ctx context.Context, store *core.TaskStore, id string) {
		awaitWatchArmed(t, e, id)
		if err := store.Dispatch(ctx, id, "test-node", "leaf-inst"); err != nil {
			t.Errorf("dispatch: %v", err)
			return
		}
		awaitRowState(t, e, id, core.StateDispatched)
		// The executor's relayed progress note — the timeline the round
		// bridges while the work runs on the peer.
		if err := store.RecordEvent(ctx, id, core.EvProgress, map[string]any{"note": "Bash: python3 -m http.server 8000"}); err != nil {
			t.Errorf("record progress: %v", err)
			return
		}
		if err := store.CompleteFromRemote(ctx, id, "test-node", map[string]any{
			"ok": true, "exit_code": 0, "stdout": "service up on :8000\n",
			"agent": "codex", "executor": "leaf-inst",
		}); err != nil {
			t.Errorf("complete: %v", err)
		}
	})

	// Progress reaches these from two goroutines: the submit path's in-flight
	// event bridge and the wait's timeline bridge. Both run while the round is
	// live, so the slices need their own lock — the race detector saw the
	// interleaved appends as a real defect.
	var progressMu sync.Mutex
	var waitStates, toolNotes []string
	cb := StreamCallbacks{OnProgress: func(p Progress) {
		progressMu.Lock()
		defer progressMu.Unlock()
		switch p.Kind {
		case ProgressWait:
			waitStates = append(waitStates, p.Name)
		case ProgressTool:
			toolNotes = append(toolNotes, p.Name)
		}
	}}
	res, err := e.AskTurns(context.Background(), nil, "调度一下mac，写一个自我介绍网页，并且在这台设备上运行", "", false, cb)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != "task" || res.TaskID == "" {
		t.Fatalf("res = %+v, want a task", res)
	}
	if res.TaskState != core.StateDone || !res.OK {
		t.Fatalf("res state = %q ok=%v, want done/true — the round ended before the outcome", res.TaskState, res.OK)
	}
	if !strings.Contains(res.Stdout, "service up on :8000") {
		t.Fatalf("stdout = %q, want the executor's output", res.Stdout)
	}
	if res.Executor != "leaf-inst" {
		t.Errorf("executor = %q, want leaf-inst", res.Executor)
	}
	// The assertions above are the proof that the round followed the task: a
	// wait that returned at "queued" would carry neither the executor's output
	// nor a done state. The old elapsed-time floor only measured the sleeps
	// this test no longer takes.
	// The card advanced out of the parked state and carried the relayed work
	// note. Only the parked state is asserted: the watch announces a state it
	// observes, and a poll that lands after the consumer has already walked
	// the row from dispatched to done sees the end state alone. Which
	// intermediate states a tick happens to catch is a scheduling detail, not
	// a contract — the done result above already proves the wait followed it.
	progressMu.Lock()
	waits := append([]string(nil), waitStates...)
	notes := append([]string(nil), toolNotes...)
	progressMu.Unlock()
	if !containsStr(waits, core.StateQueued) {
		t.Errorf("wait progress = %v, want the parked state announced", waits)
	}
	if !containsStr(notes, "Bash: python3 -m http.server 8000") {
		t.Errorf("tool notes = %v, want the relayed progress note", notes)
	}
	// The converged report must be the model's, about the real outcome: the
	// follow-up call saw the settled observation, not "queued".
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("model calls = %d, want 2 (dispatch + report)", calls)
	}
	if !strings.Contains(followUp, "service up on :8000") || !strings.Contains(followUp, core.StateDone) {
		t.Fatalf("follow-up call did not carry the settled observation: %s", followUp)
	}
	if res.Answer != "Mac 已完成：页面在 ~/self-intro/index.html，服务已起在 8000 端口。" {
		t.Fatalf("answer = %q, want the model's report", res.Answer)
	}
}

// TestInlineAskReleasedWaitLeavesTaskQueued pins the other half of the
// contract: releasing the front end (Esc / Ctrl-C) stops the watch, never the
// work — the row keeps its place in the queue and no cancel is propagated.
func TestInlineAskReleasedWaitLeavesTaskQueued(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText(pinnedTaskJSON))
		_, _ = w.Write(b)
	})
	e.queueTasks = false
	registerLeafPeer(t, e)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	awaitQueuedTask(t, e, func(_ context.Context, _ *core.TaskStore, id string) {
		awaitWatchArmed(t, e, id)
		cancel()
	})

	res, err := e.AskTurns(ctx, nil, "调度一下mac，写一个自我介绍网页，并且在这台设备上运行", "", false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != "task" || res.TaskID == "" || res.TaskState != core.StateQueued {
		t.Fatalf("res = %+v, want the last observed queued task", res)
	}
	row, err := core.NewTaskStore(e.db, nil).Get(context.Background(), res.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if row.State != core.StateQueued {
		t.Fatalf("task state = %q after a released wait, want queued (the work keeps running)", row.State)
	}
}

// TestQueueModeKeepsImmediateReceipt guards the async surfaces: the web
// console's queue mode returns the queued pointer immediately — its session
// finalizer folds the outcome later — so the inline wait must not leak into it.
func TestQueueModeKeepsImmediateReceipt(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText(pinnedTaskJSON))
		_, _ = w.Write(b)
	})
	e.queueTasks = true
	registerLeafPeer(t, e)

	started := time.Now()
	res, err := e.AskTurns(context.Background(), nil, "调度一下mac，写一个自我介绍网页，并且在这台设备上运行", "", false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.TaskState != core.StateQueued {
		t.Fatalf("res state = %q, want queued", res.TaskState)
	}
	if d := time.Since(started); d > 10*time.Second {
		t.Fatalf("queue-mode ask took %s; it must return the queued receipt immediately", d)
	}
}

// TestQueuedAskAwaitsOutcome is the queue-and-await contract the interactive
// surfaces run on: the task lands through Enqueue (resource keys, scheduled
// row, survives this process), but the round still follows it to the settled
// verdict and reports the outcome — the turn looks like an inline run while
// the scheduler owns the execution.
func TestQueuedAskAwaitsOutcome(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var followUp string
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		n := calls
		if n > 1 {
			followUp = string(body)
		}
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		resp := anthropicText(pinnedTaskJSON)
		if n > 1 {
			resp = anthropicText("Mac 已完成：页面在 ~/self-intro/index.html，服务已起在 8000 端口。")
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})
	e.queueTasks = true
	e.awaitQueued = true // interactive surface: enqueue, then follow to settle
	registerLeafPeer(t, e)

	awaitQueuedTask(t, e, func(ctx context.Context, store *core.TaskStore, id string) {
		awaitWatchArmed(t, e, id)
		if err := store.Dispatch(ctx, id, "test-node", "leaf-inst"); err != nil {
			t.Errorf("dispatch: %v", err)
			return
		}
		awaitRowState(t, e, id, core.StateDispatched)
		if err := store.CompleteFromRemote(ctx, id, "test-node", map[string]any{
			"ok": true, "exit_code": 0, "stdout": "service up on :8000\n",
			"agent": "codex", "executor": "leaf-inst",
		}); err != nil {
			t.Errorf("complete: %v", err)
		}
	})

	res, err := e.AskTurns(context.Background(), nil, "调度一下mac，写一个自我介绍网页，并且在这台设备上运行", "", false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != "task" || res.TaskID == "" {
		t.Fatalf("res = %+v, want a task", res)
	}
	if res.TaskState != core.StateDone || !res.OK {
		t.Fatalf("res state = %q ok=%v, want done/true — the round ended before the outcome", res.TaskState, res.OK)
	}
	if !strings.Contains(res.Stdout, "service up on :8000") {
		t.Fatalf("stdout = %q, want the executor's output", res.Stdout)
	}
	// The done state and the executor's output are the proof that the round
	// followed the task; elapsed time only measured the removed sleeps.
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("model calls = %d, want 2 (dispatch + report)", calls)
	}
	if !strings.Contains(followUp, "service up on :8000") || !strings.Contains(followUp, core.StateDone) {
		t.Fatalf("follow-up call did not carry the settled observation: %s", followUp)
	}
	if res.Answer != "Mac 已完成：页面在 ~/self-intro/index.html，服务已起在 8000 端口。" {
		t.Fatalf("answer = %q, want the model's report", res.Answer)
	}
	// The enqueued row carries the queue metadata — scheduled, so a real
	// consumer (or the sweep's requeue) can always find it again.
	row, err := core.NewTaskStore(e.db, nil).Get(context.Background(), res.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !row.Scheduled {
		t.Fatalf("enqueued row scheduled=%v, want scheduled for ListReady", row.Scheduled)
	}
}

// TestQueuedAskReleasedWaitKeepsWork pins the detach half of the contract:
// releasing the front end (Esc / Ctrl-C) stops the watch, never the work —
// the queued row keeps its place and no cancel is propagated.
func TestQueuedAskReleasedWaitKeepsWork(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText(pinnedTaskJSON))
		_, _ = w.Write(b)
	})
	e.queueTasks = true
	e.awaitQueued = true
	registerLeafPeer(t, e)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	awaitQueuedTask(t, e, func(_ context.Context, _ *core.TaskStore, id string) {
		awaitWatchArmed(t, e, id)
		cancel()
	})

	res, err := e.AskTurns(ctx, nil, "调度一下mac，写一个自我介绍网页，并且在这台设备上运行", "", false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.TaskID == "" || res.TaskState != core.StateQueued {
		t.Fatalf("res = %+v, want the last observed queued task", res)
	}
	row, err := core.NewTaskStore(e.db, nil).Get(context.Background(), res.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if row.State != core.StateQueued {
		t.Fatalf("task state = %q after a released wait, want queued (the work keeps running)", row.State)
	}
}

// TestAwaitDispatchedSettlesAll covers the tool-round fan-out: several
// task_submit calls in one model round enqueue their rows in parallel, and
// the await must return every settled outcome — in dispatch order — not just
// the first receipt.
func TestAwaitDispatchedSettlesAll(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {})
	store := core.NewTaskStore(e.db, nil)
	ctx := context.Background()
	t1, err := store.Create(ctx, "", "proj-a", "audit panda", "test-node", nil)
	if err != nil {
		t.Fatalf("create t1: %v", err)
	}
	t2, err := store.Create(ctx, "", "proj-b", "audit health", "test-node", nil)
	if err != nil {
		t.Fatalf("create t2: %v", err)
	}
	if err := store.Queue(ctx, t1.TaskID, "test-node"); err != nil {
		t.Fatalf("queue t1: %v", err)
	}
	if err := store.Queue(ctx, t2.TaskID, "test-node"); err != nil {
		t.Fatalf("queue t2: %v", err)
	}

	// The consumer's side: claim and finish both, with t2 — the later
	// dispatch — finishing first so the result order proves it is dispatch
	// order, not completion order.
	go func() {
		time.Sleep(1100 * time.Millisecond)
		_ = store.Dispatch(ctx, t2.TaskID, "test-node", "agent-b")
		_ = store.CompleteFromRemote(ctx, t2.TaskID, "test-node", map[string]any{
			"ok": true, "exit_code": 0, "stdout": "health ok", "agent": "codex",
		})
		time.Sleep(1100 * time.Millisecond)
		_ = store.Dispatch(ctx, t1.TaskID, "test-node", "agent-a")
		_ = store.CompleteFromRemote(ctx, t1.TaskID, "test-node", map[string]any{
			"ok": true, "exit_code": 0, "stdout": "panda ok", "agent": "codex",
		})
	}()

	dispatched := []*Result{
		{Kind: "task", TaskID: t1.TaskID, TaskTitle: t1.Title, TaskState: core.StateQueued},
		{Kind: "task", TaskID: t2.TaskID, TaskTitle: t2.Title, TaskState: core.StateQueued},
	}
	out := e.awaitDispatched(ctx, e.sched.Load(), dispatched, StreamCallbacks{})
	if len(out) != 2 {
		t.Fatalf("outcomes = %d, want 2", len(out))
	}
	if out[0].TaskID != t1.TaskID || out[1].TaskID != t2.TaskID {
		t.Fatalf("outcome order = %s,%s; want dispatch order %s,%s", out[0].TaskID, out[1].TaskID, t1.TaskID, t2.TaskID)
	}
	for i, want := range []string{"panda ok", "health ok"} {
		if out[i].TaskState != core.StateDone || !out[i].OK {
			t.Fatalf("outcome %d state=%s ok=%v, want done/true", i, out[i].TaskState, out[i].OK)
		}
		if !strings.Contains(out[i].Stdout, want) {
			t.Fatalf("outcome %d stdout = %q, want %q", i, out[i].Stdout, want)
		}
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// planDriver walks a stage row through the lifecycle the scheduler would
// give it, on the same store the watch polls — the plan-side counterpart of
// awaitQueuedTask's settle closure.
func planDriver(ctx context.Context, store *core.TaskStore, id, end string) {
	_ = store.Queue(ctx, id, "test-node")
	_ = store.Dispatch(ctx, id, "test-node", "test-node")
	_ = store.Accept(ctx, id, "test-node")
	switch end {
	case "done":
		_ = store.Complete(ctx, id, "test-node", map[string]any{"ok": true, "stdout": "shipped"})
	case "failed":
		_ = store.Fail(ctx, id, "test-node", "stage blew up")
	case "review":
		_ = store.PauseWithDisposition(ctx, id, "test-node", "tier-2 needs consent", core.ApprovalResumeExecution)
	}
}

// seedPlan wires two stage rows under one plan id, the shape StartPlan
// persists before the first wave is released.
func seedPlan(t *testing.T, store *core.TaskStore, planID string) (s1, s2 core.Task) {
	t.Helper()
	ctx := context.Background()
	var err error
	if s1, err = store.Create(ctx, "", "proj", "build stage", "test-node", nil); err != nil {
		t.Fatalf("create s1: %v", err)
	}
	if s2, err = store.Create(ctx, "", "proj", "show stage", "test-node", nil); err != nil {
		t.Fatalf("create s2: %v", err)
	}
	if err := store.SetStage(ctx, s1.TaskID, planID, "build", nil); err != nil {
		t.Fatalf("set s1: %v", err)
	}
	if err := store.SetStage(ctx, s2.TaskID, planID, "show", []string{"build"}); err != nil {
		t.Fatalf("set s2: %v", err)
	}
	return s1, s2
}

// The reported defect: a plan returned at "started" and the turn went quiet
// while the pipeline ran on. The watch must follow the stages until every
// one settles and report the final board.
func TestAwaitPlanFollowsStagesToVerdict(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {})
	sched := e.sched.Load()
	if sched == nil {
		t.Fatal("no scheduler")
	}
	store := core.NewTaskStore(e.db, nil)
	ctx := context.Background()
	s1, s2 := seedPlan(t, store, "plan-follow-1")

	go func() {
		time.Sleep(1100 * time.Millisecond) // let the first poll see submitted rows
		planDriver(ctx, store, s1.TaskID, "done")
		time.Sleep(1100 * time.Millisecond) // a poll must observe the stage finish
		planDriver(ctx, store, s2.TaskID, "done")
	}()

	var waits []string
	cb := StreamCallbacks{OnProgress: func(p Progress) {
		if p.Kind == ProgressWait {
			waits = append(waits, p.Name)
		}
	}}
	started := time.Now()
	board := e.awaitPlanSettled(ctx, sched, "plan-follow-1", cb)
	if d := time.Since(started); d > 15*time.Second {
		t.Fatalf("watch ran %s — it should end at the last stage's verdict", d)
	}
	if len(board) != 2 {
		t.Fatalf("board = %d stages, want 2", len(board))
	}
	for _, st := range board {
		if st.State != core.StateDone {
			t.Fatalf("stage %s state = %s, want done", st.StageID, st.State)
		}
	}
	if len(waits) == 0 {
		t.Fatal("no stage progress reached the card")
	}
	res := &Result{OK: true}
	e.planBoardVerdict(ctx, store, res, board)
	if !res.OK || res.NeedsApproval {
		t.Fatalf("verdict = ok:%v approval:%v, want clean done", res.OK, res.NeedsApproval)
	}
}

// A stage parked for a human ends the watch early: the turn must free its
// input for the approval, and the board folds into the caller's approval
// request — not a failure.
func TestAwaitPlanStageReviewBecomesApproval(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {})
	sched := e.sched.Load()
	store := core.NewTaskStore(e.db, nil)
	ctx := context.Background()
	s1, s2 := seedPlan(t, store, "plan-follow-2")

	go func() {
		time.Sleep(1100 * time.Millisecond)
		planDriver(ctx, store, s1.TaskID, "review")
		_ = s2 // stays submitted behind its predecessor
	}()

	board := e.awaitPlanSettled(ctx, sched, "plan-follow-2", StreamCallbacks{})
	if len(board) != 2 {
		t.Fatalf("board = %d stages, want 2", len(board))
	}
	res := &Result{OK: true}
	e.planBoardVerdict(ctx, store, res, board)
	if !res.NeedsApproval || res.Approval == nil {
		t.Fatalf("review stage did not become an approval request: %+v", res)
	}
	if res.Approval.TaskID != s1.TaskID {
		t.Fatalf("approval task = %s, want parked stage %s", res.Approval.TaskID, s1.TaskID)
	}
	if !res.OK {
		t.Fatal("a parked stage folded as a failure — the human gate is not a verdict")
	}
}

// A stage's terminal failure is the plan's verdict: the watch settles once
// the cascade closes the board, and the failure reason reaches the caller.
func TestAwaitPlanStageFailureIsTheVerdict(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {})
	sched := e.sched.Load()
	store := core.NewTaskStore(e.db, nil)
	ctx := context.Background()
	s1, s2 := seedPlan(t, store, "plan-follow-3")

	go func() {
		time.Sleep(1100 * time.Millisecond)
		planDriver(ctx, store, s1.TaskID, "failed")
		time.Sleep(1100 * time.Millisecond)
		// the plan cascade cancels the dependent, as AdvancePlan would
		_ = store.Cancel(ctx, s2.TaskID)
	}()

	board := e.awaitPlanSettled(ctx, sched, "plan-follow-3", StreamCallbacks{})
	res := &Result{OK: true}
	e.planBoardVerdict(ctx, store, res, board)
	if res.OK || res.ExitCode == 0 {
		t.Fatalf("failed stage kept ok=%v code=%d", res.OK, res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "stage blew up") {
		t.Fatalf("verdict lost the stage's reason: %q", res.Stderr)
	}
}

// TestAwaitSettledRowDeleted covers the mid-wait delete: a queued row is
// Deletable, so `task rm` or a board clear can remove it while a round is
// following it — the watch must settle (cancelled) instead of polling the
// empty store forever.
func TestAwaitSettledRowDeleted(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {})
	store := core.NewTaskStore(e.db, nil)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "doomed task", "test-node", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Queue(ctx, task.TaskID, "test-node"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		if _, err := store.Delete(ctx, task.TaskID); err != nil {
			t.Errorf("delete: %v", err)
		}
	}()
	started := time.Now()
	cur, res := e.awaitSettled(ctx, e.sched.Load(), task,
		bus.TaskResultPayload{TaskID: task.TaskID, State: task.State}, StreamCallbacks{})
	if d := time.Since(started); d > 8*time.Second {
		t.Fatalf("watch ran %s after the row vanished — it must settle", d)
	}
	if cur.State != core.StateCancelled || res.State != core.StateCancelled {
		t.Fatalf("states = %s/%s, want cancelled/cancelled", cur.State, res.State)
	}
}
