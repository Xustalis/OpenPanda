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

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// pinnedTaskJSON is the classified dispatch the fake entry model emits: a task
// hard-pinned to the peer below, whose link is not live in these tests — the
// exact shape that used to park in the queue and end the session.
const pinnedTaskJSON = `{"kind":"task","task":{"title":"在 Mac 上建自我介绍页","context_type":"command",` +
	`"requires":{"abilities":["leaf:probe"]},` +
	`"spec":{"scope":"","target":"写一个自我介绍网页并在本机运行","constraints":[],"success_definition":"curl 返回 200","node":"mac-leaf"},` +
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
		Capacity:      ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2},
	}
	if err := ledger.Register(e.db, leaf, "leaf-inst", 5); err != nil {
		t.Fatalf("register leaf: %v", err)
	}
}

// awaitQueuedTask polls the store until the pinned task parks, then drives it
// the way the daemon's queue consumer would: claim/dispatch, a relayed agent
// progress note, and the remote result. It returns the task id ("" when the
// deadline passes).
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
		time.Sleep(1200 * time.Millisecond) // let the wait begin
		if err := store.Dispatch(ctx, id, "test-node", "leaf-inst"); err != nil {
			t.Errorf("dispatch: %v", err)
			return
		}
		time.Sleep(1200 * time.Millisecond) // a poll tick must observe the claim
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

	var waitStates, toolNotes []string
	cb := StreamCallbacks{OnProgress: func(p Progress) {
		switch p.Kind {
		case ProgressWait:
			waitStates = append(waitStates, p.Name)
		case ProgressTool:
			toolNotes = append(toolNotes, p.Name)
		}
	}}
	started := time.Now()
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
	if d := time.Since(started); d < 1500*time.Millisecond {
		t.Errorf("ask returned after %s; it did not wait for the task to settle", d)
	}
	// The card advanced through the parked states and the relayed work note.
	if !containsStr(waitStates, core.StateQueued) || !containsStr(waitStates, core.StateDispatched) {
		t.Errorf("wait progress = %v, want queued then dispatched", waitStates)
	}
	if !containsStr(toolNotes, "Bash: python3 -m http.server 8000") {
		t.Errorf("tool notes = %v, want the relayed progress note", toolNotes)
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
	awaitQueuedTask(t, e, func(_ context.Context, _ *core.TaskStore, _ string) {
		time.Sleep(1200 * time.Millisecond) // let the wait begin
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

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
